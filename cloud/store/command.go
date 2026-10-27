package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

// Command 指令审计行（§8 Command）。
type Command struct {
	ID        string         `json:"cmd_id"`
	CmdSeq    int64          `json:"cmd_seq"`
	DeviceID  string         `json:"device_id"`
	CmdType   string         `json:"cmd_type"`
	Payload   map[string]any `json:"payload"`
	Status    string         `json:"status"`
	CreatedBy string         `json:"created_by"`
	Result    map[string]any `json:"result"`
	ResultErr string         `json:"result_error,omitempty"`
	IssuedAt  time.Time      `json:"issued_at"`
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
	AckedAt   *time.Time     `json:"acked_at,omitempty"`
	ResultAt  *time.Time     `json:"result_at,omitempty"`
}

// AllocateCmdSeq 每设备单调递增（§6.2），事务内自增后读取。
func (s *Store) AllocateCmdSeq(tx *sql.Tx, deviceID string) (int64, error) {
	if _, err := tx.Exec(`UPDATE device SET next_cmd_seq=next_cmd_seq+1 WHERE id=?`, deviceID); err != nil {
		return 0, err
	}
	var seq int64
	err := tx.QueryRow(`SELECT next_cmd_seq FROM device WHERE id=? FOR UPDATE`, deviceID).Scan(&seq)
	return seq, err
}

// CreateCommand 插入指令（status=pending），返回分配的 cmd_seq。
func (s *Store) CreateCommand(tx *sql.Tx, c *Command) (int64, error) {
	var (
		err error
		seq int64
	)
	if tx == nil {
		var inner *sql.Tx
		if inner, err = s.DB.Begin(); err != nil {
			return 0, err
		}
		seq, err = s.CreateCommand(inner, c)
		if err != nil {
			_ = inner.Rollback()
			return 0, err
		}
		return seq, inner.Commit()
	}
	if seq, err = s.AllocateCmdSeq(tx, c.DeviceID); err != nil {
		return 0, err
	}
	payload, _ := json.Marshal(c.Payload)
	var expires any
	if c.ExpiresAt != nil {
		expires = *c.ExpiresAt
	}
	_, err = tx.Exec(`INSERT INTO command (id, cmd_seq, device_id, cmd_type, payload, created_by, expires_at)
		VALUES (?,?,?,?,?,?,?)`, c.ID, seq, c.DeviceID, c.CmdType, string(payload), c.CreatedBy, expires)
	return seq, err
}

// ToProto 落库行 → 线上指令。
func (c *Command) ToProto() *proto.Cmd {
	return &proto.Cmd{
		Type:      proto.TypeCmd,
		CmdID:     c.ID,
		CmdSeq:    c.CmdSeq,
		CmdType:   c.CmdType,
		ExpiresAt: c.ExpiresAt,
		Payload:   c.Payload,
	}
}

// MarkSent 指令已推送至连接。
func (s *Store) MarkSent(cmdID string) error {
	_, err := s.DB.Exec(`UPDATE command SET status='sent' WHERE id=? AND status='pending'`, cmdID)
	return err
}

// PendingCommands 未 ack 且未过期（重连补发，§6.2 at-least-once）。
func (s *Store) PendingCommands(deviceID string) ([]*Command, error) {
	rows, err := s.DB.Query(`SELECT id, cmd_seq, device_id, cmd_type, payload, status, created_by,
		result, issued_at, expires_at, acked_at, result_at FROM command
		WHERE device_id=? AND status IN ('pending','sent')
		  AND (expires_at IS NULL OR expires_at > NOW(3))
		ORDER BY cmd_seq`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Command
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanCommand(row interface{ Scan(...any) error }) (*Command, error) {
	var c Command
	var payloadRaw, resultRaw []byte
	var resultErr sql.NullString
	var expires, acked, resultAt sql.NullTime
	if err := row.Scan(&c.ID, &c.CmdSeq, &c.DeviceID, &c.CmdType, &payloadRaw, &c.Status, &c.CreatedBy,
		&resultRaw, &c.IssuedAt, &expires, &acked, &resultAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(payloadRaw, &c.Payload)
	if len(resultRaw) > 0 {
		_ = json.Unmarshal(resultRaw, &c.Result)
	}
	if expires.Valid {
		t := expires.Time
		c.ExpiresAt = &t
	}
	if acked.Valid {
		t := acked.Time
		c.AckedAt = &t
	}
	if resultAt.Valid {
		t := resultAt.Time
		c.ResultAt = &t
	}
	c.ResultErr = resultErr.String
	return &c, nil
}

// AckCommand 收到 cmd_result：落完整结果信封 {ok,data,error} + 置 acked + 推进 last_acked_cmd_seq。
func (s *Store) AckCommand(cmdID string, ok bool, data map[string]any, errStr string) (*Command, error) {
	envelope := map[string]any{"ok": ok}
	if data != nil {
		envelope["data"] = data
	}
	if errStr != "" {
		envelope["error"] = errStr
	}
	b, _ := json.Marshal(envelope)
	resultJSON := string(b)
	res, err := s.DB.Exec(`UPDATE command SET status='acked', acked_at=NOW(3), result_at=NOW(3), result=?
		WHERE id=? AND status IN ('pending','sent')`, resultJSON, cmdID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil // 重复结果（重发后的重复 cmd_result）：幂等忽略
	}
	var seq int64
	var deviceID string
	err = s.DB.QueryRow(`SELECT cmd_seq, device_id FROM command WHERE id=?`, cmdID).Scan(&seq, &deviceID)
	if err != nil {
		return nil, err
	}
	_, _ = s.DB.Exec(`UPDATE device SET last_acked_cmd_seq=GREATEST(last_acked_cmd_seq,?) WHERE id=?`, seq, deviceID)
	return s.GetCommand(cmdID)
}

func (s *Store) GetCommand(cmdID string) (*Command, error) {
	c, err := scanCommand(s.DB.QueryRow(`SELECT id, cmd_seq, device_id, cmd_type, payload, status, created_by,
		result, issued_at, expires_at, acked_at, result_at FROM command WHERE id=?`, cmdID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// ListCommands 审计查询。
func (s *Store) ListCommands(deviceID string, limit int) ([]*Command, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.Query(`SELECT id, cmd_seq, device_id, cmd_type, payload, status, created_by,
		result, issued_at, expires_at, acked_at, result_at FROM command
		WHERE device_id=? ORDER BY cmd_seq DESC LIMIT ?`, deviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Command
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ExpireStale 过期指令置 expired（§6.2，防陈旧指令迟到执行）。
func (s *Store) ExpireStale() (int64, error) {
	res, err := s.DB.Exec(`UPDATE command SET status='expired'
		WHERE status IN ('pending','sent') AND expires_at IS NOT NULL AND expires_at < NOW(3)`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
