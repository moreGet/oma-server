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
	dialect, err := gooseDialect(driver)
	if err != nil {
		return err
	}

	// embed.FS 는 "migrations/*.sql" 로 적재되어 있으므로, Provider 가 SQL 파일을
	// 루트에서 찾도록 "migrations" 하위로 잘라낸다.
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("db: sub migrations fs: %w", err)
	}

	provider, err := goose.NewProvider(dialect, conn, sub)
	if err != nil {
		return fmt.Errorf("db: new goose provider: %w", err)
	}
	// 주의: provider.Close() 는 NewProvider 에 넘긴 *sql.DB 를 닫는다.
	// conn 수명은 main.go(조립 루트)가 소유하므로 여기서 닫지 않는다.

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("db: run migrations up: %w", err)
	}
	return nil
}
