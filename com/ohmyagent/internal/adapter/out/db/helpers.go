package db

import (
	"database/sql"
	"fmt"
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
