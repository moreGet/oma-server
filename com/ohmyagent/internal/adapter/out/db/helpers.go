package db

import (
	"database/sql"
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

// inPlaceholders 는 IN 절용 `?,?,?` 문자열과 (선행 인자 + ids) 인자 목록을 만든다.
//
// `WHERE member_id=? AND period IN (...)` 처럼 IN 앞에 다른 조건이 오는 질의를 위해
// lead 로 선행 인자를 받는다. 인자 순서는 lead → ids 이므로 SQL 의 ? 순서와 맞춰 쓴다.
// ids 가 비면 빈 문자열을 돌려주므로, 호출부가 먼저 빈 목록을 걸러야 한다
// (`IN ()` 은 유효한 SQL 이 아니다).
func inPlaceholders(ids []string, lead ...any) (string, []any) {
	ph := make([]string, len(ids))
	args := make([]any, 0, len(lead)+len(ids))
	args = append(args, lead...)
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	return strings.Join(ph, ","), args
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
