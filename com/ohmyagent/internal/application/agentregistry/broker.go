package agentregistryapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	domainagentregistry "aiagent/com/ohmyagent/internal/domain/agentregistry"
)

// broker 는 A2A 토큰 브로커 상태다(활성 서명 키 + TTL).
// v1 은 단일 활성 키를 기동 시 1회 적재해 메모리에 캐시한다(회전은 수동:
// 새 키를 active 로 저장 후 재기동 — 수신측은 미지의 kid 수신 시 공개키 재취득으로 추종).
type broker struct {
	signer   domainagentregistry.TokenSigner
	tokenTTL time.Duration
	key      domainagentregistry.A2AKey // 평문 PEM(메모리 전용). 로그 금지
}

// EnableBroker 는 A2A 토큰 브로커를 초기화한다(기동 시 1회).
// 활성 키가 없으면 P-256 키쌍을 생성해 개인키를 AES-GCM 암호화 저장하고(bootstrap),
// 있으면 복호화해 재사용한다(재기동 시 kid 유지).
func (s *Service) EnableBroker(ctx context.Context, keys domainagentregistry.KeyRepository, signer domainagentregistry.TokenSigner, cipher domainagentregistry.Cipher, tokenTTL time.Duration) error {
	if tokenTTL <= 0 {
		tokenTTL = defaultTokenTTL
	}
	key, err := keys.GetActive(ctx)
	switch {
	case errors.Is(err, domainagentregistry.ErrNotFound):
		key, err = signer.GenerateKey()
		if err != nil {
			return fmt.Errorf("a2a broker: generate key: %w", err)
		}
		key.Active = true
		key.CreatedAt = s.now().UTC().Truncate(time.Second)
		enc, encErr := cipher.Encrypt(key.PrivateKeyPEM)
		if encErr != nil {
			return fmt.Errorf("a2a broker: encrypt private key: %w", encErr)
		}
		stored := key
		stored.PrivateKeyPEM = enc // 저장본만 암호화(메모리 캐시는 평문 유지)
		if err := keys.Save(ctx, stored); err != nil {
			return fmt.Errorf("a2a broker: save key: %w", err)
		}
		slog.Info("a2a broker: signing key generated", "kid", key.KID)
	case err != nil:
		return fmt.Errorf("a2a broker: load key: %w", err)
	default:
		plain, decErr := cipher.Decrypt(key.PrivateKeyPEM)
		if decErr != nil {
			return fmt.Errorf("a2a broker: decrypt private key (APP_ENCRYPTION_SECRET changed?): %w", decErr)
		}
		key.PrivateKeyPEM = plain
	}
	s.broker = &broker{signer: signer, tokenTTL: tokenTTL, key: key}
	return nil
}

// MintToken 은 대상 에이전트 호출용 단명 ES256 토큰을 발급한다(§공유 계약 클레임).
// 대상 미존재 → ErrNotFound. 대상별 호출 ACL 은 v2(지금은 인증 멤버 누구나 발급 가능).
func (s *Service) MintToken(ctx context.Context, callerMemberID, targetAgentID string) (domainagentregistry.A2AToken, error) {
	if s.broker == nil {
		return domainagentregistry.A2AToken{}, errors.New("a2a broker is not configured")
	}
	target, err := s.repo.Get(ctx, targetAgentID)
	if err != nil {
		return domainagentregistry.A2AToken{}, err
	}
	now := s.now().UTC()
	claims := domainagentregistry.A2AClaims{
		Issuer:        domainagentregistry.A2AIssuer,
		Subject:       callerMemberID,
		CallerAgentID: s.callerAgentID(ctx, callerMemberID),
		Audience:      target.ID,
		IssuedAt:      now,
		ExpiresAt:     now.Add(s.broker.tokenTTL),
		JTI:           uuid.NewString(),
	}
	token, err := s.broker.signer.Mint(s.broker.key, claims)
	if err != nil {
		return domainagentregistry.A2AToken{}, fmt.Errorf("a2a broker: mint: %w", err)
	}
	return domainagentregistry.A2AToken{
		Token:           token,
		ExpiresIn:       s.broker.tokenTTL,
		AudienceAgentID: target.ID,
	}, nil
}

// callerAgentID 는 cid 클레임(호출자 agent_id)을 해석한다.
// 요청 본문이 없는 계약이라 호출자의 소유 에이전트가 정확히 1개일 때만 특정 가능하다 —
// 여럿이거나 미등록이면 빈 값(cid 는 로그 상관관계용이라 검증에 영향 없음).
func (s *Service) callerAgentID(ctx context.Context, callerMemberID string) string {
	owned, err := s.repo.List(ctx, domainagentregistry.Filter{OwnerID: callerMemberID})
	if err != nil || len(owned) != 1 {
		return ""
	}
	return owned[0].ID
}

// PublicKey 는 활성 서명 키의 공개 정보를 반환한다(수신 에이전트가 기동 시 1회 취득·캐시).
func (s *Service) PublicKey() (kid, alg, publicKeyPEM string) {
	if s.broker == nil {
		return "", "", ""
	}
	return s.broker.key.KID, domainagentregistry.A2AAlg, s.broker.key.PublicKeyPEM
}
