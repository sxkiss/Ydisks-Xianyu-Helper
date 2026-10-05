package renew

import (
	"strings"

	"xianyu-go/internal/xianyu/cookierefresh"
)

// ResponseCookiesConflict 判断 result 是否会覆盖请求开始后被并发修改的凭证。
// initialValue/initialMetadata 是请求起点，currentValue/currentMetadata 是锁内重读结果；所有值仅限内存，不允许日志输出。
// 完整 Jar 按名称、域、路径和分区比较全部属性；扁平兼容路径按名称比较，快照权威性变化则拒绝旧响应。
func ResponseCookiesConflict(initialValue, initialMetadata, currentValue, currentMetadata string, result *Result) bool {
	if result == nil || len(result.SetCookies) == 0 {
		return false
	}
	// initial、initialComplete 表示请求起点的权威快照及其可用性。
	initial, initialComplete := cookierefresh.SnapshotFromMetadataOK(initialMetadata)
	// current、currentComplete 表示提交前最新的权威快照及其可用性。
	current, currentComplete := cookierefresh.SnapshotFromMetadataOK(currentMetadata)
	if initialComplete != currentComplete {
		return true
	}
	if !initialComplete {
		// initialValues、currentValues 是历史扁平 Cookie 的按名称视图，不推断已经丢失的作用域。
		initialValues, currentValues := cookierefresh.ParseCookieString(initialValue), cookierefresh.ParseCookieString(currentValue)
		for _, header := range result.SetCookies { // header 是当前响应链中的一个 Set-Cookie，不输出明文。
			// name 是响应可能修改的 Cookie 名称。
			name := strings.TrimSpace(strings.SplitN(header, "=", 2)[0])
			if initialValues[name] != currentValues[name] {
				return true
			}
		}
		return false
	}
	// before、latest、rebased 按完整作用域保存起点、最新值和响应重放后的最终值。
	before, latest, rebased := scopedResponseCookieMap(initial), scopedResponseCookieMap(current), scopedResponseCookieMap(result.applyResponseCookies(current))
	// key、cookie 覆盖当前仍存在的 Cookie；只有并发变化且本响应又会改写时才发生冲突。
	for key, cookie := range latest {
		if before[key] != cookie && rebased[key] != cookie {
			return true
		}
	}
	// key 覆盖被并发流程删除的 Cookie；旧响应不得重新恢复它们。
	for key := range before {
		if _, exists := latest[key]; !exists { // exists 表示该作用域的 Cookie 是否仍在最新 Jar 中。
			if _, restored := rebased[key]; restored { // restored 表示响应是否重新创建了已经被并发删除的凭证。
				return true
			}
		}
	}
	return false
}

// scopedResponseCookieMap 为 snapshot 构造与现有 Cookie Jar 一致的作用域索引，值保留全部属性以检测过期时间等变化。
// 返回的键和值均属于敏感内存视图，仅供续期冲突比较，不得持久化为明文或写入日志。
func scopedResponseCookieMap(snapshot []cookierefresh.BrowserCookie) map[string]cookierefresh.BrowserCookie {
	// result 保存归一后的每个 Cookie，沿用 Jar 对重复身份的末值语义。
	result := make(map[string]cookierefresh.BrowserCookie, len(snapshot))
	for _, cookie := range cookierefresh.NormalizeSnapshot(snapshot) { // cookie 是默认路径已经归一的一个完整 Cookie。
		cookie.Domain = strings.ToLower(cookie.Domain)
		result[cookie.Name+"\x00"+cookie.Domain+"\x00"+cookie.Path+"\x00"+cookie.PartitionKey] = cookie
	}
	return result
}
