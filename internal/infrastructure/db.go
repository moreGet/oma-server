package infrastructure

import (
	"database/sql"
	"fmt"

	_ "github.com/go-sql-driver/mysql" // MariaDB 와 와이어 호환되는 mysql 드라이버

	"OhMyAgent.AiAgent.Server/internal/config"
)

// NewMariaDB 는 *sql.DB 를 생성하고 풀 설정 + 헬스 체크(Ping) 까지 수행한다.
//
// 풀 설정:
//   - SetMaxOpenConns      : cfg.MaxConns
//   - SetMaxIdleConns      : cfg.MaxConns / 2
//
// 호출자는 main.go 에서 defer db.Close() 로 정리 책임을 가진다.
func NewMariaDB(cfg config.DatabaseConfig) (*sql.DB, error) {
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	maxOpen := cfg.MaxConns
	if maxOpen <= 0 {
		maxOpen = 10
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxOpen / 2)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}
	return db, nil
}
