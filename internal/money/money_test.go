package money

import "testing"

// TestParseYuanToCentsAcceptsStrictValues 验证合法金额在整数分单位下保持精确。
func TestParseYuanToCentsAcceptsStrictValues(t *testing.T) {
	// testCase 保存一组合法金额及其精确分值。
	for _, testCase := range []struct {
		input string
		want  int64
	}{
		{input: "0", want: 0},
		{input: "001.2", want: 120},
		{input: "19.90", want: 1990},
	} {
		// got 和 err 保存金额转换结果及格式错误。
		got, err := ParseYuanToCents(testCase.input)
		if err != nil || got != testCase.want {
			t.Fatalf("ParseYuanToCents(%q)=%d,%v，期望 %d,nil", testCase.input, got, err, testCase.want)
		}
	}
}

// TestParseYuanToCentsRejectsAmbiguousOrOverflowValues 验证危险格式和整数溢出不会进入改价请求。
func TestParseYuanToCentsRejectsAmbiguousOrOverflowValues(t *testing.T) {
	// input 是当前应被拒绝的危险金额文本。
	for _, input := range []string{"", "1.", ".1", "1.234", "+1", "1e2", "¥1", "1,000", "1 0", "92233720368547758.08"} {
		// got 和 err 保存拒绝结果及错误分类。
		if got, err := ParseYuanToCents(input); err == nil || got != 0 {
			t.Fatalf("ParseYuanToCents(%q)=%d,%v，期望拒绝", input, got, err)
		}
	}
}
