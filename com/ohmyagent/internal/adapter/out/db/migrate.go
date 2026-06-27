package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// RunMigrations 는 기동 시 임베드된 마이그레이션을 Up 적용한다.
// goose v3.21 Provider API 를 사용하며, dialect 는 driver(mysql|sqlite)로 분기한다.
func RunMigrations(ctx context.Context, driver string, conn *sql.DB) error {
	provider, err := newProvider(driver, conn)
	if err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("db: run migrations up: %w", err)
	}
	return nil
}

// ResetMigrations 는 모든 마이그레이션을 0 까지 Down(drop) 한 뒤 다시 Up(create) 한다.
// 매 기동마다 깨끗한 스키마를 보장하는 파괴적 연산이므로, 로컬 개발 환경에서만 호출한다
// (main.go 의 cfg.ResetsDatabase() 게이트). 최초 기동(미적용 상태)에서는 Down 이 no-op 이다.
func ResetMigrations(ctx context.Context, driver string, conn *sql.DB) error {
	provider, err := newProvider(driver, conn)
	if err != nil {
		return err
	}
	if _, err := provider.DownTo(ctx, 0); err != nil {
		return fmt.Errorf("db: reset migrations down: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("db: reset migrations up: %w", err)
	}
	return nil
}

// newProvider 는 임베드된 마이그레이션 FS 로 goose Provider 를 구성한다.
// 주의: provider.Close() 는 넘긴 *sql.DB 를 닫는다. conn 수명은 main.go(조립 루트)가
// 소유하므로 여기서 Close 하지 않는다.
func newProvider(driver string, conn *sql.DB) (*goose.Provider, error) {
	dialect, err := gooseDialect(driver)
	if err != nil {
		return nil, err
	}

	// embed.FS 는 "migrations/*.sql" 로 적재되어 있으므로, Provider 가 SQL 파일을
	// 루트에서 찾도록 "migrations" 하위로 잘라낸다.
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("db: sub migrations fs: %w", err)
	}

	// SQL 마이그레이션(1..9) + 조건부 Go 마이그레이션(10/11, idempotent column add).
	provider, err := goose.NewProvider(dialect, conn, sub, goose.WithGoMigrations(goMigrations(driver)...))
	if err != nil {
		return nil, fmt.Errorf("db: new goose provider: %w", err)
	}
	return provider, nil
}
