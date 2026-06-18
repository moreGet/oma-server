package security

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	domainauth "aiagent/com/ohmyagent/internal/domain/auth"
)

// 컴파일 타임 인터페이스 만족 검증.
var _ domainauth.TokenService = (*JWTTokenService)(nil)

// JWTTokenService 는 HS256 대칭키 기반 domainauth.TokenService 구현이다.
type JWTTokenService struct {
	secret []byte
	expiry time.Duration
}

// NewJWTTokenService 는 비밀키와 만료기간으로 토큰 서비스를 생성한다.
func NewJWTTokenService(secret string, expiry time.Duration) *JWTTokenService {
	return &JWTTokenService{secret: []byte(secret), expiry: expiry}
}

// jwtClaims 는 토큰에 직렬화되는 클레임 구조다(표준 클레임 + 커스텀).
type jwtClaims struct {
	Username string `json:"username"`
	Level    int    `json:"level"`
	jwt.RegisteredClaims
}

// Generate 는 멤버 정보로 서명된 JWT 를 발급한다.
func (s *JWTTokenService) Generate(member domainauth.Member) (string, error) {
	now := time.Now()
	claims := jwtClaims{
		Username: member.Username,
		Level:    int(member.Role.Level),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   member.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.expiry)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.secret)
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return signed, nil
}

// Parse 는 토큰을 검증·파싱하여 Claims 를 반환한다. 실패 시 domainauth.ErrInvalidToken.
// alg 를 HS256 으로 고정하여 alg-confusion 공격을 방지한다.
func (s *JWTTokenService) Parse(token string) (domainauth.Claims, error) {
	var claims jwtClaims
	parsed, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		return domainauth.Claims{}, domainauth.ErrInvalidToken
	}
	return domainauth.Claims{
		MemberID: claims.Subject,
		Username: claims.Username,
		Level:    domainauth.RoleLevel(claims.Level),
	}, nil
}
