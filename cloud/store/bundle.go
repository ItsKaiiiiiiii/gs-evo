package store

import (
	"database/sql"
	"errors"
	"time"
)

// BundleTicket / bundle 表操作（§6.6 一次性票据）。

func (s *Store) CreateBundleTicket(id, tokenHash, deviceID, sha256Hex, action string, expiresAt time.Time) error {
	_, err := s.DB.Exec(`INSERT INTO bundle_ticket (id, token_hash, device_id, sha256, action, expires_at)
		VALUES (?,?,?,?,?,?)`, id, tokenHash, deviceID, sha256Hex, action, expiresAt)
	return err
}

// ConsumeBundleTicket 原子消费：匹配 hash/action/过期，成功置 used_at 并返回 device_id。
func (s *Store) ConsumeBundleTicket(tokenHash, sha256Hex, action string) (deviceID string, ok bool, err error) {
	res, err := s.DB.Exec(`UPDATE bundle_ticket SET used_at=NOW(3)
		WHERE token_hash=? AND sha256=? AND action=? AND used_at IS NULL AND expires_at > NOW(3)`,
		tokenHash, sha256Hex, action)
	if err != nil {
		return "", false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", false, nil
	}
	err = s.DB.QueryRow(`SELECT device_id FROM bundle_ticket WHERE token_hash=?`, tokenHash).Scan(&deviceID)
	return deviceID, err == nil, err
}

func (s *Store) UpsertBundle(sha256Hex string, size int64) error {
	_, err := s.DB.Exec(`INSERT INTO bundle (sha256, size) VALUES (?,?)
		ON DUPLICATE KEY UPDATE size=VALUES(size)`, sha256Hex, size)
	return err
}

func (s *Store) BundleExists(sha256Hex string) (bool, int64, error) {
	var size int64
	err := s.DB.QueryRow(`SELECT size FROM bundle WHERE sha256=?`, sha256Hex).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, nil
	}
	return err == nil, size, err
}
