// Package money 提供不使用浮点数的金额解析能力。
package money

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// ErrInvalidYuan 表示金额文本不符合元到分的严格格式。
var ErrInvalidYuan = errors.New("金额格式无效")

// ParseYuanToCents 将最多两位小数的元金额转换为整数分，并在溢出前拒绝输入。
func ParseYuanToCents(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, ErrInvalidYuan
	}
	// wholeText 和 fracText 保存整数部分与小数部分文本。
	wholeText, fracText := raw, ""
	// dot 保存小数点在原始金额中的位置。
	if dot := strings.IndexByte(raw, '.'); dot >= 0 {
		wholeText, fracText = raw[:dot], raw[dot+1:]
	}
	if wholeText == "" || fracText == "" && strings.Contains(raw, ".") || len(fracText) > 2 {
		return 0, ErrInvalidYuan
	}
	// digit 是当前待校验的金额数字字符。
	for _, digit := range wholeText + fracText {
		if digit < '0' || digit > '9' {
			return 0, ErrInvalidYuan
		}
	}
	// whole 和 err 保存整数元解析结果及解析错误。
	whole, err := strconv.ParseInt(wholeText, 10, 64)
	if err != nil || whole < 0 || whole > math.MaxInt64/100 {
		return 0, ErrInvalidYuan
	}
	// frac 保存换算为分的小数部分。
	frac := int64(0)
	if fracText == "1" || len(fracText) == 1 {
		frac = int64(fracText[0]-'0') * 10
	} else if len(fracText) == 2 {
		frac = int64(fracText[0]-'0')*10 + int64(fracText[1]-'0')
	}
	// cents 保存最终整数分结果，并用于溢出检查。
	cents := whole*100 + frac
	if cents < 0 || (whole == math.MaxInt64/100 && cents < whole*100) {
		return 0, ErrInvalidYuan
	}
	return cents, nil
}
