package infrastructure

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"OhMyAgent.AiAgent.Server/migrations"
)

// RunMigrations 는 Flyway 와 동일한 방식으로 미적용 마이그레이션을 순서대로 실행한다.
//
//   - 마이그레이션 SQL 파일은 바이너리에 embed 되어 외부 경로에 의존하지 않는다.
//   - schema_migrations 테이블로 적용 이력을 관리한다 (Flyway의 flyway_schema_history 역할).
//   - 이미 적용된 버전은 건너뛴다 (ErrNoChange 는 정상으로 처리).
func RunMigrations(db *sql.DB) error {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("migrations source: %w", err)
	}

	driver, err := mysql.WithInstance(db, &mysql.Config{})
	if err != nil {
		return fmt.Errorf("migrations driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "mysql", driver)
	if err != nil {
		return fmt.Errorf("migrations init: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrations up: %w", err)
	}

	return nil
}
