// Package security 는 인증(JWT)·라우트 레벨 인가(RBAC)·미들웨어 체인을 담는다.
package security

import (
	"context"

	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

// ctxKey 는 context 충돌 방지용 비공개 키 타입이다.
type ctxKey struct{}

// claimsKey 는 claims 를 context 에 저장하는 키다.
var claimsKey = ctxKey{}

// withClaims 는 claims 를 context 에 주입한 새 context 를 반환한다.
func withClaims(ctx context.Context, claims domainauth.Claims) context.Context {
	return context.WithValue(ctx, claimsKey, claims)
}

// WithClaims 는 claims 를 context 에 주입한 새 context 를 반환한다(쿠키 기반 웹 어댑터 등 외부 인증 경로용).
func WithClaims(ctx context.Context, claims domainauth.Claims) context.Context {
	return withClaims(ctx, claims)
}

// ClaimsFrom 은 context 에서 claims 를 추출한다. 없으면 ok=false.
func ClaimsFrom(ctx context.Context) (domainauth.Claims, bool) {
	claims, ok := ctx.Value(claimsKey).(domainauth.Claims)
	return claims, ok
}
