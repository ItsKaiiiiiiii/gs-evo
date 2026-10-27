package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Device 设备档案（§8）。
type Device struct {
	ID              string
	Owner           string
	TokenHash       string
	TokenExpiresAt  time.Time
	Hostname        string
	OS              string
	Arch            string
	WorkerVersion   string
	Labels          []string
	NextCmdSeq      int64
	LastAckedCmdSeq int64
	LastSeen        *time.Time
	Status          string
	CreatedAt       time.Time
}

func (s *Store) UpsertDeviceOnEnroll(tx *sql.Tx, d *Device) (isNew bool, err error) {
	var exists string
	err = tx.QueryRow(`SELECT id FROM device WHERE id=?`, d.ID).Scan(&exists)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.Exec(`INSERT INTO device
			(id, owner, token_hash, token_expires_at, hostname, os, arch, worker_version, labels, status)
			VALUES (?,?,?,?,?,?,?,?,?,'offline')`,
			d.ID, d.Owner, d.TokenHash, d.TokenExpiresAt, d.Hostname, d.OS, d.Arch, d.WorkerVersion, labelsJSON(d.Labels))
		return true, err
	case err != nil:
		return false, err
	default:
		// 防抢注（§6.7）：重 enroll 覆盖旧 token；owner 变为本次 enroll 的所有者
		_, err = tx.Exec(`UPDATE device SET owner=?, token_hash=?, token_expires_at=?,
			hostname=?, os=?, arch=?, worker_version=?, labels=? WHERE id=?`,
			d.Owner, d.TokenHash, d.TokenExpiresAt, d.Hostname, d.OS, d.Arch, d.WorkerVersion, labelsJSON(d.Labels), d.ID)
		return false, err
	}
}

const deviceCols = `id, owner, token_hash, token_expires_at, hostname, os, arch, worker_version,
	labels, next_cmd_seq, last_acked_cmd_seq, last_seen, status, created_at`

func scanDevice(row interface{ Scan(...any) error }) (*Device, error) {
	var d Device
	var labelsRaw []byte
	var lastSeen sql.NullTime
	if err := row.Scan(&d.ID, &d.Owner, &d.TokenHash, &d.TokenExpiresAt, &d.Hostname, &d.OS, &d.Arch,
		&d.WorkerVersion, &labelsRaw, &d.NextCmdSeq, &d.LastAckedCmdSeq, &lastSeen, &d.Status, &d.CreatedAt); err != nil {
		return nil, err
	}
	if len(labelsRaw) > 0 {
		_ = json.Unmarshal(labelsRaw, &d.Labels)
	}
	if lastSeen.Valid {
		d.LastSeen = &lastSeen.Time
	}
	return &d, nil
}

// GetDeviceByTokenHash 鉴权主路径（SQL 自带过期过滤，§6.7）。
func (s *Store) GetDeviceByTokenHash(hash string) (*Device, error) {
	d, err := scanDevice(s.DB.QueryRow(`SELECT `+deviceCols+` FROM device
		WHERE token_hash=? AND token_expires_at > NOW(3) AND status != 'revoked'`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

func (s *Store) GetDevice(id string) (*Device, error) {
	d, err := scanDevice(s.DB.QueryRow(`SELECT `+deviceCols+` FROM device WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

func (s *Store) ListDevices() ([]*Device, error) {
	rows, err := s.DB.Query(`SELECT ` + deviceCols + ` FROM device ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetDeviceLabels 设置设备标签（rollout 圈定）。
func (s *Store) SetDeviceLabels(id string, labels []string) error {
	b, err := json.Marshal(labels)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`UPDATE device SET labels=? WHERE id=?`, string(b), id)
	return err
}

// RevokeDevice 吊销：置 revoked + token 立即失效（哈希列替换为占位值，唯一索引不冲突）。
func (s *Store) RevokeDevice(id string) error {
	_, err := s.DB.Exec(`UPDATE device SET status='revoked',
		token_hash=CONCAT('revoked-', REPLACE(id,'-','')), token_expires_at=NOW(3) WHERE id=?`, id)
	return err
}

// TouchSeen 任意帧刷新 last_seen；上线时置 online。
func (s *Store) TouchSeen(id string, online bool) error {
	st := "offline"
	if online {
		st = "online"
	}
	_, err := s.DB.Exec(`UPDATE device SET last_seen=NOW(3), status=? WHERE id=? AND status!='revoked'`, st, id)
	return err
}

func (s *Store) SetOnline(id string) error {
	_, err := s.DB.Exec(`UPDATE device SET last_seen=NOW(3), status='online' WHERE id=? AND status!='revoked'`, id)
	return err
}

func (s *Store) SetOffline(id string) error {
	_, err := s.DB.Exec(`UPDATE device SET status='offline' WHERE id=? AND status='online'`, id)
	return err
}

// UpdateRuntime hello 上报的运行时档案。
func (s *Store) UpdateRuntime(id string, hostname, osName, arch, version string, agentsJSON []byte) error {
	_, err := s.DB.Exec(`UPDATE device SET hostname=?, os=?, arch=?, worker_version=?, agents=? WHERE id=?`,
		hostname, osName, arch, version, agentsJSON, id)
	return err
}

func labelsJSON(labels []string) any {
	if len(labels) == 0 {
		return nil
	}
	b, _ := json.Marshal(labels)
	return string(b)
}

// ---- enroll token ----

type EnrollToken struct {
	ID        int64
	Label     string
	CreatedBy string
	ExpiresAt time.Time
	UsedAt    *time.Time
}

func (s *Store) CreateEnrollToken(hash, label, createdBy string, expiresAt time.Time) error {
	_, err := s.DB.Exec(`INSERT INTO enroll_token (token_hash, label, created_by, expires_at) VALUES (?,?,?,?)`,
		hash, label, createdBy, expiresAt)
	return err
}

// ConsumeEnrollToken 一次性消费：未用未过期才有效（事务内 FOR UPDATE 防并发复用）。
func (s *Store) ConsumeEnrollToken(tx *sql.Tx, hash string) (*EnrollToken, error) {
	var t EnrollToken
	var used sql.NullTime
	err := tx.QueryRow(`SELECT id, label, created_by, expires_at, used_at FROM enroll_token
		WHERE token_hash=? FOR UPDATE`, hash).Scan(&t.ID, &t.Label, &t.CreatedBy, &t.ExpiresAt, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if used.Valid {
		return nil, nil
	}
	if time.Now().After(t.ExpiresAt) {
		return nil, nil
	}
	if _, err := tx.Exec(`UPDATE enroll_token SET used_at=NOW(3) WHERE id=?`, t.ID); err != nil {
		return nil, err
	}
	return &t, nil
}

// ---- admin token ----

func (s *Store) AddAdminToken(hash, label string) error {
	_, err := s.DB.Exec(`INSERT IGNORE INTO admin_token (token_hash, label) VALUES (?,?)`, hash, label)
	return err
}

func (s *Store) GetAdminToken(hash string) (bool, error) {
	var one string
	err := s.DB.QueryRow(`SELECT token_hash FROM admin_token WHERE token_hash=?`, hash).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
