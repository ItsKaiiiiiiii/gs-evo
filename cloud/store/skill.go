package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ItsKaiiiiiiii/gs-evo/pkg/proto"
)

func newUUIDBytes() []byte {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return b
}

// SkillRow / SkillVersionRow：§8 数据模型。
type SkillRow struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	LatestStable string   `json:"latest_stable"`
	LatestAny    string   `json:"latest_any"`
	CreatedAt    NullTime `json:"created_at"`
}

type SkillVersionRow struct {
	ID            string          `json:"id"`
	SkillID       string          `json:"skill_id"`
	SkillName     string          `json:"skill_name"`
	Version       string          `json:"version"`
	Manifest      json.RawMessage `json:"manifest"`
	ContentSHA256 string          `json:"content_sha256"`
	Size          int64           `json:"size"`
	Signature     string          `json:"signature,omitempty"`
	OriginDevice  string          `json:"origin_device,omitempty"`
	Source        string          `json:"source"`
	ParentVersion string          `json:"parent_version,omitempty"`
	Status        string          `json:"status"`
	Rationale     string          `json:"rationale,omitempty"`
	CreatedAt     NullTime        `json:"created_at"`
}

const skillCols = `s.id, s.name, s.latest_stable, s.latest_any, s.created_at`

func scanSkill(row interface{ Scan(...any) error }) (*SkillRow, error) {
	var sk SkillRow
	var id []byte
	var stable, anyV sql.NullString
	if err := row.Scan(&id, &sk.Name, &stable, &anyV, &sk.CreatedAt); err != nil {
		return nil, err
	}
	sk.ID = BytesToUUID(id)
	sk.LatestStable = stable.String
	sk.LatestAny = anyV.String
	return &sk, nil
}

// UpsertSkillCollected 采集入库：skill 不存在则建，返回 skill 行。
func (s *Store) UpsertSkillCollected(tx *sql.Tx, name string) (*SkillRow, error) {
	if _, err := tx.Exec(`INSERT IGNORE INTO skill (id, name, agent_compat) VALUES (?,?, JSON_ARRAY('claude-code'))`,
		newUUIDBytes(), name); err != nil {
		return nil, err
	}
	return scanSkill(tx.QueryRow(`SELECT `+skillCols+` FROM skill s WHERE s.name=?`, name))
}

func (s *Store) GetSkillByName(name string) (*SkillRow, error) {
	sk, err := scanSkill(s.DB.QueryRow(`SELECT `+skillCols+` FROM skill s WHERE s.name=?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return sk, err
}

func (s *Store) ListSkills() ([]*SkillRow, error) {
	rows, err := s.DB.Query(`SELECT ` + skillCols + ` FROM skill s ORDER BY s.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SkillRow
	for rows.Next() {
		sk, err := scanSkill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sk)
	}
	return out, rows.Err()
}

// ContentExists 版本内容去重（D5：同 hash 跳过）。
func (s *Store) ContentExists(tx *sql.Tx, skillID, sha string) (bool, error) {
	var one int
	err := tx.QueryRow(`SELECT 1 FROM skill_version WHERE skill_id=? AND content_sha256=?`,
		uuidBytes(skillID), sha).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// InsertVersion 新版本（status=draft 起，§7.5）。
func (s *Store) InsertVersion(tx *sql.Tx, v *SkillVersionRow) error {
	if v.ID == "" {
		v.ID = BytesToUUID(newUUIDBytes())
	}
	if tx != nil {
		if _, err := tx.Exec(`INSERT INTO skill_version
			(id, skill_id, version, manifest, content_sha256, size, origin_device, source, parent_version, status)
			VALUES (?,?,?,?,?,?,?,?,?, 'draft')`,
			uuidBytes(v.ID), uuidBytes(v.SkillID), v.Version, string(v.Manifest), v.ContentSHA256,
			v.Size, v.OriginDevice, v.Source, v.ParentVersion); err != nil {
			return err
		}
		// 采集总是追加最新版本，直接刷新 latest_any（避免 MySQL UPDATE 同表子查询限制）
		_, err := tx.Exec(`UPDATE skill SET latest_any=? WHERE id=?`, v.Version, uuidBytes(v.SkillID))
		return err
	}
	return nil
}

func uuidBytes(u string) []byte {
	b := UUIDToBytes(u)
	if b == nil {
		return newUUIDBytes()
	}
	return b
}

const svCols = `v.id, v.skill_id, s.name, v.version, v.manifest, v.content_sha256, v.size,
	v.signature, v.origin_device, v.source, v.parent_version, v.rationale, v.status, v.created_at`

func scanVersion(row interface{ Scan(...any) error }) (*SkillVersionRow, error) {
	var v SkillVersionRow
	var id, skillID []byte
	var sig, origin, parent, rationale sql.NullString
	if err := row.Scan(&id, &skillID, &v.SkillName, &v.Version, &v.Manifest, &v.ContentSHA256, &v.Size,
		&sig, &origin, &v.Source, &parent, &rationale, &v.Status, &v.CreatedAt); err != nil {
		return nil, err
	}
	v.Rationale = rationale.String
	v.ID = BytesToUUID(id)
	v.SkillID = BytesToUUID(skillID)
	v.Signature = sig.String
	v.OriginDevice = origin.String
	v.ParentVersion = parent.String
	return &v, nil
}

// GetVersion 按 (skill name, version) 取版本行。
func (s *Store) GetVersion(skillName, version string) (*SkillVersionRow, error) {
	v, err := scanVersion(s.DB.QueryRow(`SELECT `+svCols+` FROM skill_version v
		JOIN skill s ON s.id=v.skill_id WHERE s.name=? AND v.version=?`, skillName, version))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

func (s *Store) ListVersions(skillName string) ([]*SkillVersionRow, error) {
	rows, err := s.DB.Query(`SELECT `+svCols+` FROM skill_version v
		JOIN skill s ON s.id=v.skill_id WHERE s.name=? ORDER BY v.created_at DESC, v.version DESC`, skillName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SkillVersionRow
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// UpdateVersionStatus 状态机迁移（合法性在 skillsvc 校验）；转 stable 时写入签名并刷新 latest_stable。
func (s *Store) UpdateVersionStatus(skillName, version, status, signature string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE skill_version v JOIN skill s ON s.id=v.skill_id
		SET v.status=?, v.signature=? WHERE s.name=? AND v.version=?`,
		status, nilIfEmpty(signature), skillName, version); err != nil {
		_ = tx.Rollback()
		return err
	}
	if status == "stable" {
		if _, err := tx.Exec(`UPDATE skill s SET latest_stable=? WHERE s.name=?`, version, skillName); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// RecordInstall 落 DeviceSkillInstall（§8）；evaluationRunID 非空表示评估用途的受控安装。
func (s *Store) RecordInstall(deviceID string, v *SkillVersionRow, result, prevVersion, evaluationRunID string) error {
	var prev any
	if prevVersion != "" {
		prevRow, err := s.GetVersion(v.SkillName, prevVersion)
		if err == nil && prevRow != nil {
			prev = uuidBytes(prevRow.ID)
		}
	}
	var run any
	if evaluationRunID != "" {
		run = evaluationRunID
	}
	_, err := s.DB.Exec(`INSERT INTO device_skill_install
		(device_id, skill_ver_id, skill_name, version, result, prev_ver_id, evaluation_run_id) VALUES (?,?,?,?,?,?,?)`,
		deviceID, uuidBytes(v.ID), v.SkillName, v.Version, result, prev, run)
	return err
}

// ListDeviceSkillInstalls 查询指定设备集上某版本的安装记录（评估灰度等待用）。
func (s *Store) ListDeviceSkillInstalls(skillName, version string, deviceIDs []string) ([]map[string]any, error) {
	if len(deviceIDs) == 0 {
		return nil, nil
	}
	q := `SELECT device_id, result FROM device_skill_install WHERE skill_name=? AND version=? AND device_id IN (`
	args := []any{skillName, version}
	for i, d := range deviceIDs {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, d)
	}
	q += `)`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var dev, result string
		if err := rows.Scan(&dev, &result); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"device_id": dev, "result": result})
	}
	return out, rows.Err()
}

// ManifestOf 把版本行 manifest 还原为结构体。
func (v *SkillVersionRow) ManifestOf() proto.Manifest {
	var m proto.Manifest
	_ = json.Unmarshal(v.Manifest, &m)
	return m
}
