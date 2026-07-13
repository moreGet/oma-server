package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

// 버전 10/11 은 조건부(idempotent) Go 마이그레이션이다. 과거 in-place 수정으로 DB 마다 컬럼이 있을 수도/없을 수도 있어,
// ALTER 전에 컬럼 존재를 확인해 누락분만 추가한다(중복 컬럼 에러 방지).

// goMigrations 는 driver(sqlite|mysql)에 맞춘 Go 마이그레이션 목록을 반환한다.
func goMigrations(driver string) []*goose.Migration {
	return []*goose.Migration{
		quotaPeriodLimitsMigration(driver),   // 10: 일/주 토큰 한도 컬럼
		transcriptRetentionMigration(driver), // 11: 대화이력 보존/스트립 컬럼
	}
}

type colSpec struct{ table, column, ddl string }

func quotaPeriodLimitsMigration(driver string) *goose.Migration {
	cols := []colSpec{
		{"member_token_limits", "daily_limit", "BIGINT NOT NULL DEFAULT 0"},
		{"member_token_limits", "weekly_limit", "BIGINT NOT NULL DEFAULT 0"},
		{"quota_config", "default_daily_limit", "BIGINT NOT NULL DEFAULT 0"},
		{"quota_config", "default_weekly_limit", "BIGINT NOT NULL DEFAULT 0"},
	}
	return columnAddMigration(10, driver, cols)
}

func transcriptRetentionMigration(driver string) *goose.Migration {
	cols := []colSpec{
		{"transcript_settings", "retention_days", "INT NOT NULL DEFAULT 0"},
		{"transcript_settings", "strip_attachments", "BOOLEAN NOT NULL DEFAULT FALSE"},
	}
	return columnAddMigration(11, driver, cols)
}

// columnAddMigration 은 누락 컬럼만 추가(up)/존재 컬럼만 제거(down)하는 Go 마이그레이션을 만든다.
func columnAddMigration(version int64, driver string, cols []colSpec) *goose.Migration {
	up := func(ctx context.Context, tx *sql.Tx) error {
		for _, c := range cols {
			if err := addColumnIfMissing(ctx, tx, driver, c); err != nil {
				return err
			}
		}
		return nil
	}
	down := func(ctx context.Context, tx *sql.Tx) error {
		for i := len(cols) - 1; i >= 0; i-- {
			if err := dropColumnIfExists(ctx, tx, driver, cols[i].table, cols[i].column); err != nil {
				return err
			}
		}
		return nil
	}
	return goose.NewGoMigration(version, &goose.GoFunc{RunTx: up}, &goose.GoFunc{RunTx: down})
}

func columnExists(ctx context.Context, tx *sql.Tx, driver, table, column string) (bool, error) {
	var (
		query string
		args  []any
	)
	if driver == "mysql" {
		query = "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name=?"
		args = []any{table, column}
	} else { // sqlite (table 명은 내부 상수라 인터폴레이션 안전)
		query = fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name=?", table)
		args = []any{column}
	}
	var n int
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return false, fmt.Errorf("check column %s.%s: %w", table, column, err)
	}
	return n > 0, nil
}

func addColumnIfMissing(ctx context.Context, tx *sql.Tx, driver string, c colSpec) error {
	exists, err := columnExists(ctx, tx, driver, c.table, c.column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", c.table, c.column, c.ddl)); err != nil {
		return fmt.Errorf("add column %s.%s: %w", c.table, c.column, err)
	}
	return nil
}

func dropColumnIfExists(ctx context.Context, tx *sql.Tx, driver, table, column string) error {
	exists, err := columnExists(ctx, tx, driver, table, column)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", table, column)); err != nil {
		return fmt.Errorf("drop column %s.%s: %w", table, column, err)
	}
	return nil
}
