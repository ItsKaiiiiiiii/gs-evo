package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

// TaskRow / TaskEventRow：§8 任务表（events_ref 在 v1 物化为 task_event 表）。
type TaskRow struct {
	ID            string         `json:"id"`
	DeviceID      string         `json:"device_id"`
	Prompt        string         `json:"prompt"`
	AgentType     string         `json:"agent_type,omitempty"`
	RuntimeID     string         `json:"runtime_id,omitempty"`
	MaxPermission string         `json:"max_permission,omitempty"`
	Status        string         `json:"status"`
	CreatedBy     string         `json:"created_by"`
	CmdID         string         `json:"cmd_id,omitempty"`
	Result        map[string]any `json:"result,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	FinishedAt    *time.Time     `json:"finished_at,omitempty"`
}

const taskCols = `id, device_id, prompt, agent_type, runtime_id, max_permission, status, created_by, cmd_id, result, created_at, finished_at`

func scanTask(row interface{ Scan(...any) error }) (*TaskRow, error) {
	var t TaskRow
	var resultRaw []byte
	var finished sql.NullTime
	if err := row.Scan(&t.ID, &t.DeviceID, &t.Prompt, &t.AgentType, &t.RuntimeID, &t.MaxPermission, &t.Status,
		&t.CreatedBy, &t.CmdID, &resultRaw, &t.CreatedAt, &finished); err != nil {
		return nil, err
	}
	if len(resultRaw) > 0 {
		_ = json.Unmarshal(resultRaw, &t.Result)
	}
	if finished.Valid {
		t.FinishedAt = &finished.Time
	}
	return &t, nil
}

func (s *Store) CreateTask(t *TaskRow) error {
	_, err := s.DB.Exec(`INSERT INTO task (id, device_id, prompt, agent_type, runtime_id, max_permission, created_by, cmd_id)
		VALUES (?,?,?,?,?,?,?,?)`, t.ID, t.DeviceID, t.Prompt, t.AgentType, t.RuntimeID, t.MaxPermission, t.CreatedBy, t.CmdID)
	return err
}

func (s *Store) GetTask(id string) (*TaskRow, error) {
	t, err := scanTask(s.DB.QueryRow(`SELECT `+taskCols+` FROM task WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// SetTaskCmdID 回填指令 id（task 行先于指令创建，见 api.handleCreateTask）。
func (s *Store) SetTaskCmdID(id, cmdID string) error {
	_, err := s.DB.Exec(`UPDATE task SET cmd_id=? WHERE id=?`, cmdID, id)
	return err
}

func (s *Store) ListTasks(deviceID string, limit int) ([]*TaskRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + taskCols + ` FROM task`
	args := []any{}
	if deviceID != "" {
		q += ` WHERE device_id=?`
		args = append(args, deviceID)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TaskRow
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetTaskResult 强制写入终态结果（cmd_result 权威，晚于 status 事件到达也不丢）。
func (s *Store) SetTaskResult(id, status string, result map[string]any) error {
	var resultJSON any
	if result != nil {
		b, _ := json.Marshal(result)
		resultJSON = string(b)
	}
	_, err := s.DB.Exec(`UPDATE task SET status=?, result=?, finished_at=NOW(3) WHERE id=?`,
		status, resultJSON, id)
	return err
}

// UpdateTaskStatus 归态落库（cmd_result 驱动；幂等：已终态不回退）。
func (s *Store) UpdateTaskStatus(id, status string, result map[string]any) error {
	terminal := map[string]bool{proto.TaskCompleted: true, proto.TaskFailed: true,
		proto.TaskCancelled: true, proto.TaskTimeout: true, "expired": true}
	if terminal[status] {
		var resultJSON any
		if result != nil {
			b, _ := json.Marshal(result)
			resultJSON = string(b)
		}
		res, err := s.DB.Exec(`UPDATE task SET status=?, result=?, finished_at=NOW(3)
			WHERE id=? AND status NOT IN ('completed','failed','cancelled','timeout','expired')`,
			status, resultJSON, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
		return nil
	}
	_, err := s.DB.Exec(`UPDATE task SET status=? WHERE id=? AND status='queued'`, status, id)
	return err
}

// ---- task_event ----

// InsertTaskEvent 幂等（UNIQUE(task_id,seq)）：重放重复事件静默忽略。
func (s *Store) InsertTaskEvent(ev *proto.TaskEvent) error {
	var dataJSON any
	if ev.Data != nil {
		b, _ := json.Marshal(ev.Data)
		dataJSON = string(b)
	}
	_, err := s.DB.Exec(`INSERT IGNORE INTO task_event (task_id, seq, kind, data, ts) VALUES (?,?,?,?,?)`,
		ev.TaskID, ev.Seq, ev.Kind, dataJSON, ev.TS)
	return err
}

type TaskEventRow struct {
	TaskID string         `json:"task_id"`
	Seq    int64          `json:"seq"`
	Kind   string         `json:"kind"`
	Data   map[string]any `json:"data,omitempty"`
	TS     time.Time      `json:"ts"`
}

// ExpireStaleTasks task.start 指令已过期的任务置 expired（防僵尸 queued，NEW-1）。
func (s *Store) ExpireStaleTasks() (int64, error) {
	res, err := s.DB.Exec(`UPDATE task SET status='expired', finished_at=NOW(3),
		result=JSON_OBJECT('error','command expired before delivery/ack')
		WHERE status IN ('queued','running')
		  AND id IN (
		    SELECT JSON_UNQUOTE(JSON_EXTRACT(payload,'$.task_id')) FROM command
		    WHERE cmd_type='task.start' AND status='expired'
		  )`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) ListTaskEvents(taskID string, fromSeq int64, limit int) ([]*TaskEventRow, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	rows, err := s.DB.Query(`SELECT task_id, seq, kind, data, ts FROM task_event
		WHERE task_id=? AND seq>=? ORDER BY seq LIMIT ?`, taskID, fromSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TaskEventRow
	for rows.Next() {
		r := new(TaskEventRow)
		var dataRaw []byte
		if err := rows.Scan(&r.TaskID, &r.Seq, &r.Kind, &dataRaw, &r.TS); err != nil {
			return nil, err
		}
		if len(dataRaw) > 0 {
			_ = json.Unmarshal(dataRaw, &r.Data)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
