package store

// P3/P4：BenchmarkSet、EvaluationRun、自动发布策略、客户端发布（§7.5/§8）。

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ---- BenchmarkSet ----

type BenchmarkCase struct {
	Name           string `json:"name"`
	Prompt         string `json:"prompt"`
	ExpectContains string `json:"expect_contains,omitempty"`
	TimeoutS       int    `json:"timeout_s,omitempty"`
}

type BenchmarkSetRow struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Version   int             `json:"version"`
	Cases     []BenchmarkCase `json:"cases"`
	CreatedBy string          `json:"created_by"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func (s *Store) CreateBenchmarkSet(b *BenchmarkSetRow) error {
	cases, _ := json.Marshal(b.Cases)
	_, err := s.DB.Exec(`INSERT INTO benchmark_set (id, name, version, cases, created_by) VALUES (?,?,?,?,?)`,
		b.ID, b.Name, b.Version, string(cases), b.CreatedBy)
	return err
}

func (s *Store) GetBenchmarkSet(id string) (*BenchmarkSetRow, error) {
	b, err := scanBenchmark(s.DB.QueryRow(`SELECT id, name, version, cases, created_by, created_at, updated_at
		FROM benchmark_set WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return b, err
}

func (s *Store) LatestBenchmarkSet(name string) (*BenchmarkSetRow, error) {
	b, err := scanBenchmark(s.DB.QueryRow(`SELECT id, name, version, cases, created_by, created_at, updated_at
		FROM benchmark_set WHERE name=? ORDER BY version DESC LIMIT 1`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return b, err
}

func (s *Store) ListBenchmarkSets(limit int) ([]*BenchmarkSetRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.DB.Query(`SELECT id, name, version, cases, created_by, created_at, updated_at
		FROM benchmark_set ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*BenchmarkSetRow
	for rows.Next() {
		b, err := scanBenchmark(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func scanBenchmark(row interface{ Scan(...any) error }) (*BenchmarkSetRow, error) {
	var b BenchmarkSetRow
	var casesRaw []byte
	if err := row.Scan(&b.ID, &b.Name, &b.Version, &casesRaw, &b.CreatedBy, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(casesRaw, &b.Cases)
	return &b, nil
}

// ---- EvaluationRun ----

type EvaluationRunRow struct {
	ID             string         `json:"id"`
	SkillName      string         `json:"skill_name"`
	SkillVersion   string         `json:"skill_version"`
	BenchmarkSetID string         `json:"benchmark_set_id"`
	Status         string         `json:"status"` // running|passed|failed|error
	DeviceIDs      []string       `json:"device_ids"`
	Metrics        map[string]any `json:"metrics,omitempty"`
	Verdict        string         `json:"verdict,omitempty"`
	CreatedBy      string         `json:"created_by"`
	CreatedAt      time.Time      `json:"created_at"`
	FinishedAt     *time.Time     `json:"finished_at,omitempty"`
}

func (s *Store) CreateEvaluationRun(r *EvaluationRunRow) error {
	devices, _ := json.Marshal(r.DeviceIDs)
	_, err := s.DB.Exec(`INSERT INTO evaluation_run (id, skill_name, skill_version, benchmark_set_id, device_ids, created_by)
		VALUES (?,?,?,?,?,?)`, r.ID, r.SkillName, r.SkillVersion, r.BenchmarkSetID, string(devices), r.CreatedBy)
	return err
}

func (s *Store) FinishEvaluationRun(id, status string, metrics map[string]any, verdict string) error {
	mJSON := any(nil)
	if metrics != nil {
		b, _ := json.Marshal(metrics)
		mJSON = string(b)
	}
	_, err := s.DB.Exec(`UPDATE evaluation_run SET status=?, metrics=?, verdict=?, finished_at=NOW(3) WHERE id=?`,
		status, mJSON, verdict, id)
	return err
}

func (s *Store) GetEvaluationRun(id string) (*EvaluationRunRow, error) {
	r, err := scanEvalRun(s.DB.QueryRow(`SELECT id, skill_name, skill_version, benchmark_set_id, status,
		device_ids, metrics, verdict, created_by, created_at, finished_at FROM evaluation_run WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func (s *Store) ListEvaluationRuns(skillName, skillVersion string, limit int) ([]*EvaluationRunRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT id, skill_name, skill_version, benchmark_set_id, status, device_ids, metrics, verdict,
		created_by, created_at, finished_at FROM evaluation_run WHERE skill_name=? AND skill_version=?
		ORDER BY created_at DESC LIMIT ?`
	rows, err := s.DB.Query(q, skillName, skillVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*EvaluationRunRow
	for rows.Next() {
		r, err := scanEvalRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanEvalRun(row interface{ Scan(...any) error }) (*EvaluationRunRow, error) {
	var r EvaluationRunRow
	var devicesRaw, metricsRaw []byte
	var verdict sql.NullString
	var finished sql.NullTime
	if err := row.Scan(&r.ID, &r.SkillName, &r.SkillVersion, &r.BenchmarkSetID, &r.Status,
		&devicesRaw, &metricsRaw, &verdict, &r.CreatedBy, &r.CreatedAt, &finished); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(devicesRaw, &r.DeviceIDs)
	if len(metricsRaw) > 0 {
		_ = json.Unmarshal(metricsRaw, &r.Metrics)
	}
	r.Verdict = verdict.String
	if finished.Valid {
		r.FinishedAt = &finished.Time
	}
	return &r, nil
}

// ---- 自动发布策略（skill.auto_policy）----

type AutoPolicy struct {
	Enabled      bool     `json:"enabled"`
	MinPassRate  float64  `json:"min_pass_rate"`           // 如 1.0 = 全过
	MinRuns      int      `json:"min_runs"`                // 最近 N 次全部达标
	TargetRoot   string   `json:"target_root"`             // 下发目标根目录（skill 装到 <root>/<name>）
	TargetLabels []string `json:"target_labels,omitempty"` // 发布圈定（空=全网）
}

func (s *Store) GetAutoPolicy(skillName string) (*AutoPolicy, error) {
	var raw []byte
	err := s.DB.QueryRow(`SELECT auto_policy FROM skill WHERE name=?`, skillName).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return &AutoPolicy{}, nil
	}
	var p AutoPolicy
	_ = json.Unmarshal(raw, &p)
	return &p, nil
}

func (s *Store) SetAutoPolicy(skillName string, p *AutoPolicy) error {
	b, _ := json.Marshal(p)
	_, err := s.DB.Exec(`UPDATE skill SET auto_policy=? WHERE name=?`, string(b), skillName)
	return err
}

// ---- rationale（进化理由，§7.5 ③）----

func (s *Store) SetVersionRationale(skillName, version, rationale string) error {
	_, err := s.DB.Exec(`UPDATE skill_version v JOIN skill s ON s.id=v.skill_id
		SET v.rationale=? WHERE s.name=? AND v.version=?`, rationale, skillName, version)
	return err
}

// ---- client_release（P4 自更新）----

type ClientReleaseRow struct {
	Version   string    `json:"version"`
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	Notes     string    `json:"notes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) CreateClientRelease(r *ClientReleaseRow) error {
	_, err := s.DB.Exec(`INSERT INTO client_release (version, sha256, size, notes) VALUES (?,?,?,?)`,
		r.Version, r.SHA256, r.Size, r.Notes)
	return err
}

func (s *Store) GetClientRelease(version string) (*ClientReleaseRow, error) {
	var r ClientReleaseRow
	err := s.DB.QueryRow(`SELECT version, sha256, size, notes, created_at FROM client_release WHERE version=?`,
		version).Scan(&r.Version, &r.SHA256, &r.Size, &r.Notes, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

func (s *Store) LatestClientRelease() (*ClientReleaseRow, error) {
	r := new(ClientReleaseRow)
	err := s.DB.QueryRow(`SELECT version, sha256, size, notes, created_at FROM client_release
		ORDER BY created_at DESC LIMIT 1`).Scan(&r.Version, &r.SHA256, &r.Size, &r.Notes, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// ---- RBAC：admin token 角色 ----

type AdminTokenRow struct {
	TokenHash string    `json:"-"` // 不外露
	Label     string    `json:"label"`
	Role      string    `json:"role"` // admin | viewer
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) AddAdminTokenRole(hash, label, role string) error {
	_, err := s.DB.Exec(`INSERT IGNORE INTO admin_token (token_hash, label, role) VALUES (?,?,?)`, hash, label, role)
	return err
}

// GetAdminTokenRole 返回角色（""=不存在）。
func (s *Store) GetAdminTokenRole(hash string) (string, error) {
	var role string
	err := s.DB.QueryRow(`SELECT role FROM admin_token WHERE token_hash=?`, hash).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if role == "" {
		role = "admin" // 0003 之前的存量 token 默认全权
	}
	return role, nil
}
