package quota

import "testing"

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"안녕하세요", 5},       // CJK 5자 → 5
		{"hello world", 3}, // 11자 비CJK → ceil(11/4)=3
		{"hi 안녕", 3},       // 비CJK 3(h,i,공백)→1 + CJK 2 = 3
	}
	for _, c := range cases {
		if got := EstimateTokens(c.in); got != c.want {
			t.Errorf("EstimateTokens(%q)=%d, want %d", c.in, got, c.want)
		}
	}
}
