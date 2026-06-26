package quota

// WindowStatus 는 한 윈도우(일/주/월)의 사용 현황이다.
type WindowStatus struct {
	Window      Window
	Limit       int     // 적용 한도(0 = 무제한)
	Used        int     // 이번 기간 사용량
	Remaining   int     // max(0, Limit-Used). 무제한이면 0(Unlimited 로 구분)
	Unlimited   bool    // Limit<=0
	PercentUsed float64 // 0..100(소수 1자리). 무제한이면 0
}

// Status 는 한 사용자의 일/주/월 쿼터 현황(잔여 포함)이다.
type Status struct {
	Period  PeriodKeys
	Windows []WindowStatus // Daily, Weekly, Monthly 순
}
