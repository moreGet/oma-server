package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// rowScanner 는 *sql.Row 와 *sql.Rows 가 공통으로 만족하는 스캔 인터페이스다.
// scanX 헬퍼들이 단건/다건 조회 모두에서 재사용할 수 있게 한다.
type rowScanner interface {
	Scan(dest ...any) error
}

// nullString 은 빈 문자열을 SQL NULL 로 변환한다(감사필드 created_by/updated_by 용).
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// strFromNull 은 sql.NullString 을 평문 문자열로 복원한다(NULL → "").
func strFromNull(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

// affectedOrNotFound 는 UPDATE/DELETE 결과의 0행 영향을 도메인 notFound 에러로 변환한다.
func affectedOrNotFound(res sql.Result, notFound error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return notFound
	}
	return nil
}

// queryEach 는 다건 조회의 공통 골격이다(쿼리 → rows 순회 → Close). 행마다 onRow 를 호출하므로
// 슬라이스가 아닌 결과 형태(맵 누적 등)도 담을 수 있다. 모든 실패는 what 접두사로 래핑한다(%w 로 원인 보존).
func queryEach(ctx context.Context, db *sql.DB, what string, onRow func(rowScanner) error, query string, args ...any) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%s: query: %w", what, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := onRow(rows); err != nil {
			return fmt.Errorf("%s: scan: %w", what, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%s: rows: %w", what, err)
	}
	return nil
}

// queryList 는 다건 조회 결과를 슬라이스로 모은다. scan 은 한 행을 도메인 값으로 변환한다.
// 결과가 0행이면 nil 슬라이스를 반환한다(호출부가 빈 슬라이스를 보장해야 하면 직접 감싼다).
func queryList[T any](ctx context.Context, db *sql.DB, what string, scan func(rowScanner) (T, error), query string, args ...any) ([]T, error) {
	var out []T
	err := queryEach(ctx, db, what, func(s rowScanner) error {
		v, scanErr := scan(s)
		if scanErr != nil {
			return scanErr
		}
		out = append(out, v)
		return nil
	}, query, args...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// queryOne 은 단건 조회의 공통 골격이다: 0행이면 도메인 notFound, 그 외 실패는 what 접두사로 래핑.
func queryOne[T any](ctx context.Context, db *sql.DB, what string, notFound error, scan func(rowScanner) (T, error), query string, args ...any) (T, error) {
	v, err := scan(db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		var zero T
		return zero, notFound
	}
	if err != nil {
		var zero T
		return zero, fmt.Errorf("%s: %w", what, err)
	}
	return v, nil
}

// inPlaceholders 는 IN 절용 플레이스홀더 목록("?,?,?")과 대응 인자 슬라이스를 만든다.
// 값 개수가 가변인 IN 조회에서 자리표시자 수와 인자 수가 어긋나는 실수를 한곳에 가둔다.
func inPlaceholders[T any](vals []T) (string, []any) {
	ph := make([]string, len(vals))
	args := make([]any, len(vals))
	for i, v := range vals {
		ph[i] = "?"
		args[i] = v
	}
	return strings.Join(ph, ","), args
}

// encodeJSONList 는 슬라이스를 JSON 텍스트 컬럼 값으로 직렬화한다(빈 슬라이스·실패는 빈 문자열).
func encodeJSONList[T any](v []T) string {
	if len(v) == 0 {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// decodeJSONList 는 JSON 텍스트 컬럼을 슬라이스로 복원한다(빈 값·깨진 값은 nil — 조회를 실패시키지 않는다).
func decodeJSONList[T any](s string) []T {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []T
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}
