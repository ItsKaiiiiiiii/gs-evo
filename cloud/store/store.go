// Package store 封装 MySQL 访问与内嵌迁移。
// 说明：设计文档 §4.5 规划 sqlc，当前开发环境无 sqlc 工具链，
// 先以 database/sql 手写等价查询层（方法签名与 sqlc 生成物一致，便于后续替换）。
package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

//go:embed migrations/*.up.sql
var migrationsFS embed.FS

type Store struct {
	DB *sql.DB
}

// ErrNotFound 通用“不存在”（包内各查询返回 nil, nil 表示未命中）。
var ErrNotFound = errors.New("not found")

// Open 连接 MySQL 并应用内嵌迁移。
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{DB: db}, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version VARCHAR(255) PRIMARY KEY,
		applied_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
	) ENGINE=InnoDB`); err != nil {
		return err
	}
	entries, err := fs.Glob(migrationsFS, "migrations/*.up.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)
	for _, name := range entries {
		var applied string
		err := db.QueryRow(`SELECT version FROM schema_migrations WHERE version=?`, name).Scan(&applied)
		if err == nil {
			continue
		}
		if err != sql.ErrNoRows {
			return err
		}
		body, readErr := migrationsFS.ReadFile(name)
		if readErr != nil {
			return readErr
		}
		tx, txErr := db.Begin()
		if txErr != nil {
			return txErr
		}
		if _, execErr := tx.Exec(string(body)); execErr != nil {
			_ = tx.Rollback()
			return fmt.Errorf("%s: %w", name, execErr)
		}
		if _, execErr := tx.Exec(`INSERT INTO schema_migrations(version) VALUES(?)`, name); execErr != nil {
			_ = tx.Rollback()
			return execErr
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// UUID 转 BINARY(16) / 反向（skill id 用）。
func UUIDToBytes(u string) []byte {
	hexStr := strings.ReplaceAll(u, "-", "")
	if len(hexStr) != 32 {
		return nil
	}
	b := make([]byte, 16)
	for i := 0; i < 16; i++ {
		hi, ok1 := hexVal(hexStr[i*2])
		lo, ok2 := hexVal(hexStr[i*2+1])
		if !ok1 || !ok2 {
			return nil
		}
		b[i] = hi<<4 | lo
	}
	return b
}

func BytesToUUID(b []byte) string {
	if len(b) != 16 {
		return ""
	}
	const h = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, v := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, h[v>>4], h[v&0x0f])
	}
	return string(out)
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
