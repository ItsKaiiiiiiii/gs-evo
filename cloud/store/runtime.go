package store

import (
	"database/sql"
	"encoding/json"

	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

// Runtime 运行时（本机 agent 实例一等实体，设备 1→N）。
type RuntimeRow struct {
	ID        string `json:"runtime_id"`
	DeviceID  string `json:"device_id"`
	Type      string `json:"type"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	OK        bool   `json:"ok"`
	Source    string `json:"source"` // probed | registered
	LastSeen  string `json:"last_seen"`
}

// UpsertRuntime worker 上报的运行时 upsert（hello / runtime.sync）。
func (s *Store) UpsertRuntime(deviceID string, rt proto.RuntimeInfo, source string) error {
	_, err := s.DB.Exec(`INSERT INTO runtime (id, device_id, type, version, path, workspace, ok, source, last_seen)
		VALUES (?,?,?,?,?,?,?,?,NOW(3))
		ON DUPLICATE KEY UPDATE type=VALUES(type), version=VALUES(version), path=VALUES(path),
		workspace=VALUES(workspace), ok=VALUES(ok), source=VALUES(source), last_seen=NOW(3)`,
		rt.RuntimeID, deviceID, rt.Type, rt.Version, rt.Path, rt.Workspace, rt.OK, source)
	return err
}

// DeleteRuntime 运行时删除（生命周期管理）。
func (s *Store) DeleteRuntime(deviceID, runtimeID string) error {
	_, err := s.DB.Exec(`DELETE FROM runtime WHERE device_id=? AND id=?`, deviceID, runtimeID)
	return err
}

// RuntimeWithDevice 全局运行时视图（含设备主机名，任务/操作页选择器用）。
type RuntimeWithDevice struct {
	RuntimeRow
	Hostname  string   `json:"hostname"`
	DevStatus string   `json:"device_status"`
	Labels    []string `json:"labels"`
}

// ListRuntimes 全局运行时列表（跨设备，join 设备主机名）。
func (s *Store) ListRuntimes() ([]*RuntimeWithDevice, error) {
	rows, err := s.DB.Query(`SELECT r.id, r.device_id, r.type, COALESCE(r.version,''), COALESCE(r.path,''),
		COALESCE(r.workspace,''), r.ok, r.source, DATE_FORMAT(r.last_seen, '%Y-%m-%dT%H:%i:%sZ'),
		d.hostname, d.status, COALESCE(d.labels, JSON_ARRAY())
		FROM runtime r JOIN device d ON d.id=r.device_id
		ORDER BY d.hostname, r.type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*RuntimeWithDevice{}
	for rows.Next() {
		r := new(RuntimeWithDevice)
		var labelsRaw []byte
		if err := rows.Scan(&r.ID, &r.DeviceID, &r.Type, &r.Version, &r.Path, &r.Workspace, &r.OK, &r.Source,
			&r.LastSeen, &r.Hostname, &r.DevStatus, &labelsRaw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(labelsRaw, &r.Labels)
		if r.Labels == nil {
			r.Labels = []string{}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRuntime 校验运行时归属于指定设备（任务指派防串）。
func (s *Store) GetRuntime(deviceID, runtimeID string) (*RuntimeRow, error) {
	r := new(RuntimeRow)
	err := s.DB.QueryRow(`SELECT id, device_id, type, COALESCE(version,''), COALESCE(path,''),
		ok, source, DATE_FORMAT(last_seen, '%Y-%m-%dT%H:%i:%sZ') FROM runtime
		WHERE device_id=? AND id=?`, deviceID, runtimeID).
		Scan(&r.ID, &r.DeviceID, &r.Type, &r.Version, &r.Path, &r.OK, &r.Source, &r.LastSeen)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

func (s *Store) ListRuntimesByDevice(deviceID string) ([]*RuntimeRow, error) {
	return s.listRuntimes("WHERE r.device_id=?", deviceID)
}

func (s *Store) listRuntimes(where string, arg any) ([]*RuntimeRow, error) {
	rows, err := s.DB.Query(`SELECT r.id, r.device_id, r.type, COALESCE(r.version,''), COALESCE(r.path,''),
		COALESCE(r.workspace,''), r.ok, r.source, DATE_FORMAT(r.last_seen, '%Y-%m-%dT%H:%i:%sZ')
		FROM runtime r `+where+` ORDER BY r.type, r.id`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*RuntimeRow{}
	for rows.Next() {
		r := new(RuntimeRow)
		if err := rows.Scan(&r.ID, &r.DeviceID, &r.Type, &r.Version, &r.Path, &r.Workspace, &r.OK, &r.Source, &r.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetTaskRuntime 任务归档执行所用运行时。
func (s *Store) SetTaskRuntime(taskID, runtimeID string) error {
	_, err := s.DB.Exec(`UPDATE task SET runtime_id=? WHERE id=?`, runtimeID, taskID)
	return err
}
