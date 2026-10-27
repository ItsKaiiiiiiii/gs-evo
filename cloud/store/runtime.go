package store

import (
	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

// Runtime 运行时（本机 agent 实例一等实体，设备 1→N）。
type RuntimeRow struct {
	ID       string `json:"runtime_id"`
	DeviceID string `json:"device_id"`
	Type     string `json:"type"`
	Version  string `json:"version,omitempty"`
	Path     string `json:"path,omitempty"`
	OK       bool   `json:"ok"`
	Source   string `json:"source"` // probed | registered
	LastSeen string `json:"last_seen"`
}

// UpsertRuntime worker 上报的运行时 upsert（hello / runtime.sync）。
func (s *Store) UpsertRuntime(deviceID string, rt proto.RuntimeInfo, source string) error {
	_, err := s.DB.Exec(`INSERT INTO runtime (id, device_id, type, version, path, ok, source, last_seen)
		VALUES (?,?,?,?,?,?,?,NOW(3))
		ON DUPLICATE KEY UPDATE type=VALUES(type), version=VALUES(version), path=VALUES(path),
		ok=VALUES(ok), source=VALUES(source), last_seen=NOW(3)`,
		rt.RuntimeID, deviceID, rt.Type, rt.Version, rt.Path, rt.OK, source)
	return err
}

func (s *Store) ListRuntimesByDevice(deviceID string) ([]*RuntimeRow, error) {
	rows, err := s.DB.Query(`SELECT id, device_id, type, COALESCE(version,''), COALESCE(path,''),
		ok, source, DATE_FORMAT(last_seen, '%Y-%m-%dT%H:%i:%sZ') FROM runtime
		WHERE device_id=? ORDER BY type, id`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*RuntimeRow{}
	for rows.Next() {
		r := new(RuntimeRow)
		if err := rows.Scan(&r.ID, &r.DeviceID, &r.Type, &r.Version, &r.Path, &r.OK, &r.Source, &r.LastSeen); err != nil {
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
