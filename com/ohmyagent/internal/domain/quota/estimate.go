package quota

import "unicode"

// EstimateTokens 는 provider 가 usage 를 주지 않을 때(total_tokens=0) 텍스트에서 토큰 수를 근사한다.
// BPE 의존성 없는 문자 클래스 휴리스틱: CJK(한·중·일)는 ~1토큰/자, 그 외(라틴·기호·공백)는 ~4자/토큰.
// 정밀 과금용이 아니라 **쿼터 누락 방지용 보수적 추정**이다(실측 usage 가 있으면 항상 그쪽 우선).
func EstimateTokens(text string) int {
	var cjk, other int
	for _, r := range text {
		if isCJK(r) {
			cjk++
		} else {
			other++
		}
	}
	return cjk + (other+3)/4 // 비CJK 는 올림(ceil/4)
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hangul, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r)
}
