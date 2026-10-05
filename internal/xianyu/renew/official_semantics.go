package renew

import (
	"context"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// autoLoginCookieValues 按官方插件解析 cookieHeader：分号空格分隔、URI 解码、
// 同名末值覆盖首值。任一非法 URI 编码使整次解析返回空表；结果仅用于内存判断。
func autoLoginCookieValues(cookieHeader string) map[string]string {
	// values 保存脚本解析结果，不用于重建 HTTP Cookie 或输出日志。
	values := make(map[string]string)
	if cookieHeader == "" {
		return values
	}
	// part 为 document.cookie 中的一项；插件只使用等号分隔后的前两段。
	for _, part := range strings.Split(cookieHeader, "; ") {
		// pieces 保留官方 split("=") 的截断语义。
		pieces := strings.Split(part, "=")
		// name、nameErr 保存解码后的名称及百分号编码错误。
		name, nameErr := url.PathUnescape(pieces[0])
		// value 保存缺值时的空串；valueErr 标识值的编码错误。
		value, valueErr := "", error(nil)
		if len(pieces) > 1 {
			value, valueErr = url.PathUnescape(pieces[1])
		}
		if nameErr != nil || valueErr != nil || !utf8.ValidString(name) || !utf8.ValidString(value) {
			return map[string]string{}
		}
		values[name] = value
	}
	return values
}

// strictCookieTimeAfter 判断非空 raw 对应的 JS 日期是否严格晚于 now，供 sdkSilent 疲劳检查。
func strictCookieTimeAfter(raw string, now time.Time) bool {
	if raw == "" {
		return false
	}
	return javascriptDateMillis(raw) > float64(now.UnixMilli())
}

// cookieTimeAfter 对 raw 执行官方“未到期”判断；now 是分支选择时刻，Invalid Date 保留分支资格。
func cookieTimeAfter(raw string, now time.Time) bool {
	if raw == "" {
		return false
	}
	// 官方判断为 !(new Date(Number(raw)) <= now)，NaN 必须保留为可选分支。
	return !(javascriptDateMillis(raw) <= float64(now.UnixMilli()))
}

// javascriptDateMillis 将 raw 按 JavaScript Number 和 Date TimeClip 转为毫秒；
// 无效输入返回 NaN，保留官方对 Invalid Date 的比较语义。
func javascriptDateMillis(raw string) float64 {
	// 使用 ECMAScript 的空白集合；BOM 可以与空格混合，NEL 不属于该集合。
	raw = strings.Trim(raw, "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff")
	if raw == "" {
		return 0
	}
	// number、parseErr 保存十进制解析结果；Go 特有 Inf 和下划线格式不能接受。
	number, parseErr := strconv.ParseFloat(raw, 64)
	if strings.ContainsAny(raw, "_pP") || strings.Contains(raw, "Inf") {
		return math.NaN()
	}
	if len(raw) > 2 && raw[0] == '0' && strings.ContainsAny(raw[1:2], "xXoObB") {
		// integer、integerErr 解析 JavaScript 无符号进制字面量；超出 uint64
		// 的值同样超过 Date 的有效范围，统一返回 Invalid Date。
		integer, integerErr := strconv.ParseUint(raw, 0, 64)
		if integerErr != nil {
			return math.NaN()
		}
		number, parseErr = float64(integer), nil
	}
	if parseErr != nil || math.Abs(number) > 8.64e15 {
		return math.NaN()
	}
	return math.Trunc(number)
}

// waitAutoLoginDelay 按 s 的测试覆盖或官网固定两秒等待发请求；ctx 取消时返回其错误。
func (s Service) waitAutoLoginDelay(ctx context.Context) error {
	// delay 是 SDK 发起请求前的等待时长；负数仅供确定性测试跳过等待。
	delay := 2 * time.Second
	if s.RetryDelay < 0 {
		return ctx.Err()
	}
	if s.RetryDelay > 0 {
		delay = s.RetryDelay
	}
	// timer 由本函数创建并停止，取消时不会遗留后台等待。
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// cookieReceivedAt 返回 r 的响应头时刻；旧调用方构造的无时间结果兼容使用当前时间。
func (r *Result) cookieReceivedAt() time.Time {
	if r.responseReceivedAt.IsZero() {
		return time.Now()
	}
	return r.responseReceivedAt
}
