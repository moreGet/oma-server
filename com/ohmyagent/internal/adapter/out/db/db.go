// Package db 는 database/sql 연결·마이그레이션·레포지토리 구현(out 어댑터)을 담는다.
package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/pressly/goose/v3"

	// 드라이버 등록(database/sql 용). mysql/sqlite 모두 database/sql 로 다룬다.
	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
)

const (
	// sqliteMaxOpenConns: sqlite 는 단일 writer 이므로 1 로 고정(락 충돌 방지).
	sqliteMaxOpenConns = 1
	// defaultMaxOpenConns: mysql 등에서 maxOpenConns 인자가 <=0 일 때의 기본값.
	defaultMaxOpenConns = 10
	// connMaxLifetime: 커넥션 최대 수명. mysql wait_timeout/LB 재분배로 인한 스테일 커넥션 방지.
	connMaxLifetime = 30 * time.Minute
	// connMaxIdleTime: 유휴 커넥션 정리 한도(피크 후 풀 축소).
	connMaxIdleTime = 5 * time.Minute
)

// Open 은 driver/dsn 으로 *sql.DB 를 열고 풀을 설정한다.
// sqlite→MaxOpenConns=1(단일 writer, 락 충돌 방지), mysql→maxOpenConns(<=0 이면 기본 10).
func Open(driver, dsn string, maxOpenConns int) (*sql.DB, error) {
	driverName, err := sqlDriverName(driver)
	if err != nil {
		return nil, err
	}

	conn, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", driver, err)
	}

	var maxOpen int
	switch driver {
	case "sqlite":
		maxOpen = sqliteMaxOpenConns
	default: // mysql 등
		maxOpen = maxOpenConns
		if maxOpen <= 0 {
			maxOpen = defaultMaxOpenConns
		}
	}
	conn.SetMaxOpenConns(maxOpen)
	// 유휴 풀을 MaxOpen 과 동일하게 두어 고동접에서 커넥션 open/close churn 을 줄인다
	// (database/sql 기본 MaxIdleConns=2 는 부하 시 병목). 수명/유휴 한도로 스테일 커넥션 정리.
	conn.SetMaxIdleConns(maxOpen)
	conn.SetConnMaxLifetime(connMaxLifetime)
	conn.SetConnMaxIdleTime(connMaxIdleTime)

	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("db: ping %s: %w", driver, err)
	}
	return conn, nil
}

// sqlDriverName 은 config 드라이버명(mysql/sqlite)을 database/sql 드라이버명으로 매핑한다.
func sqlDriverName(driver string) (string, error) {
	switch driver {
	case "mysql":
		return "mysql", nil
	case "sqlite":
		return "sqlite", nil // modernc.org/sqlite 등록명
	default:
		return "", fmt.Errorf("db: unsupported driver %q (want mysql|sqlite)", driver)
	}
}

// gooseDialect 은 config 드라이버명을 goose.Dialect 로 매핑한다.
func gooseDialect(driver string) (goose.Dialect, error) {
	switch driver {
	case "mysql":
		return goose.DialectMySQL, nil
	case "sqlite":
		return goose.DialectSQLite3, nil
	default:
		return "", fmt.Errorf("db: unsupported goose dialect for driver %q", driver)
	}
}
