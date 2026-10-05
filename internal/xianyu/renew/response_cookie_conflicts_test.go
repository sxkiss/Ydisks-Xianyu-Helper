package renew

import (
	"testing"
	"time"

	"xianyu-go/internal/xianyu/cookierefresh"
)

// TestResponseCookiesConflictScopes 验证并发修改、删除、新增、属性和各作用域隔离；t 只使用合成凭证，不访问网络。
func TestResponseCookiesConflictScopes(t *testing.T) {
	// original 是响应默认作用域中的原始凭证；并发变更通过副本构造。
	original := cookierefresh.BrowserCookie{Name: "session", Value: "old", Domain: "passport.goofish.com", Path: "/", Secure: true, HTTPOnly: true}
	// changed 是并发登录已经替换的同作用域新值。
	changed := original
	changed.Value = "new"
	// attributes 只更改过期时间，扁平头完全相同时也必须检测。
	attributes := original
	attributes.Expires = float64(time.Now().Add(time.Hour).Unix())
	// pathCookie、domainCookie、partitionCookie 是同名但不受默认响应影响的三个独立凭证。
	pathCookie, domainCookie, partitionCookie := changed, changed, changed
	pathCookie.Path = "/other"
	domainCookie.Domain = "www.goofish.com"
	partitionCookie.PartitionKey = "https://goofish.com"
	// scenarios 覆盖响应会改写与不会改写的并发 Cookie，expected 指明是否应拒绝。
	scenarios := []struct {
		// name 是不含凭证的场景标识。
		name string
		// initial、current 是独立的起点和提交前快照。
		initial, current []cookierefresh.BrowserCookie
		// headers 是仅包含合成值的平台响应头。
		headers []string
		// expected 表示本响应是否会覆盖并发修改。
		expected bool
	}{
		{"same-scope", []cookierefresh.BrowserCookie{original}, []cookierefresh.BrowserCookie{changed}, []string{"session=stale; Path=/; Secure; HttpOnly"}, true},
		{"attributes", []cookierefresh.BrowserCookie{original}, []cookierefresh.BrowserCookie{attributes}, []string{"session=old; Path=/; Secure; HttpOnly"}, true},
		{"deleted", []cookierefresh.BrowserCookie{original}, []cookierefresh.BrowserCookie{}, []string{"session=old; Path=/"}, true},
		{"new-cookie", []cookierefresh.BrowserCookie{}, []cookierefresh.BrowserCookie{changed}, []string{"session=stale; Path=/"}, true},
		{"response-delete", []cookierefresh.BrowserCookie{original}, []cookierefresh.BrowserCookie{changed}, []string{"session=; Path=/; Max-Age=0"}, true},
		{"unchanged", []cookierefresh.BrowserCookie{original}, []cookierefresh.BrowserCookie{original}, []string{"session=new; Path=/"}, false},
		{"same-result", []cookierefresh.BrowserCookie{original}, []cookierefresh.BrowserCookie{changed}, []string{"session=new; Path=/; Secure; HttpOnly"}, false},
		{"other-name", []cookierefresh.BrowserCookie{original}, []cookierefresh.BrowserCookie{changed}, []string{"different=new; Path=/"}, false},
		{"other-path", []cookierefresh.BrowserCookie{original}, []cookierefresh.BrowserCookie{original, pathCookie}, []string{"session=updated; Path=/"}, false},
		{"other-domain", []cookierefresh.BrowserCookie{original}, []cookierefresh.BrowserCookie{original, domainCookie}, []string{"session=updated; Path=/"}, false},
		{"other-partition", []cookierefresh.BrowserCookie{original}, []cookierefresh.BrowserCookie{original, partitionCookie}, []string{"session=updated; Path=/"}, false},
		{"partition-match", []cookierefresh.BrowserCookie{original}, []cookierefresh.BrowserCookie{original, partitionCookie}, []string{"session=updated; Path=/; Secure; Partitioned"}, true},
		{"invalid-header", []cookierefresh.BrowserCookie{original}, []cookierefresh.BrowserCookie{changed}, []string{"invalid"}, false},
		{"deleted-unrelated", []cookierefresh.BrowserCookie{original}, []cookierefresh.BrowserCookie{}, []string{"other=new"}, false},
	}
	for _, scenario := range scenarios { // scenario 是独立的凭证冲突用例，不共享可变 Cookie 状态。
		t.Run(scenario.name, func(t *testing.T) { // t 管理当前纯函数断言，失败时只报告布尔结果。
			// result 保留固定响应接收时间和作用域，与生产增量重放一致。
			result := &Result{SetCookies: scenario.headers, responseReceivedAt: time.Now()}
			// actual 保存冲突判定，不记录输入凭证。
			actual := ResponseCookiesConflict("", cookierefresh.MetadataWithSnapshot("", scenario.initial), "", cookierefresh.MetadataWithSnapshot("", scenario.current), result)
			if actual != scenario.expected {
				t.Fatalf("conflict=%v, want=%v", actual, scenario.expected)
			}
		})
	}
}

// TestResponseCookiesConflictCompatibility 验证空结果、扁平账号及快照权威性切换；t 无需真实平台。
func TestResponseCookiesConflictCompatibility(t *testing.T) {
	// result 是扁平兼容场景的合成响应。
	result := &Result{SetCookies: []string{"session=response; Path=/"}}
	if ResponseCookiesConflict("", "", "", "", nil) || ResponseCookiesConflict("", "", "", "", &Result{}) {
		t.Fatal("空响应不应产生冲突")
	}
	if !ResponseCookiesConflict("session=old", "", "session=new", "", result) {
		t.Fatal("扁平同名并发更新未被拒绝")
	}
	if ResponseCookiesConflict("session=old; other=1", "", "session=old; other=2", "", result) {
		t.Fatal("扁平无关 Cookie 不应阻止合并")
	}
	// metadata 表示真实存在的权威空 Jar，不能被误认为没有快照。
	metadata := cookierefresh.MetadataWithSnapshot("", []cookierefresh.BrowserCookie{})
	if !ResponseCookiesConflict("", "", "", metadata, result) || !ResponseCookiesConflict("", metadata, "", "", result) {
		t.Fatal("快照权威性切换必须拒绝旧响应")
	}
}

// TestResponseCookiesConflictRedirectChain 验证重定向前一跳的作用域也参与冲突判断；t 使用固定响应链而不发网络请求。
func TestResponseCookiesConflictRedirectChain(t *testing.T) {
	// initial、current 固定重定向起点域的凭证变化，最终域不同不能掩盖该冲突。
	initial := []cookierefresh.BrowserCookie{{Name: "session", Value: "old", Domain: "first.goofish.com", Path: "/"}}
	// current 是并发登录后的完整快照副本。
	current := append([]cookierefresh.BrowserCookie(nil), initial...)
	current[0].Value = "new"
	// result 保存两跳响应及其原始 URL，第一跳将尝试覆盖并发凭证。
	result := &Result{SetCookies: []string{"session=stale; Path=/", "other=value; Path=/"}, responseCookies: []responseCookieBatch{
		{url: "https://first.goofish.com/start", headers: []string{"session=stale; Path=/"}, receivedAt: time.Now()},
		{url: SilentHasLoginURL, headers: []string{"other=value; Path=/"}, receivedAt: time.Now()},
	}}
	if !ResponseCookiesConflict("", cookierefresh.MetadataWithSnapshot("", initial), "", cookierefresh.MetadataWithSnapshot("", current), result) {
		t.Fatal("遗漏重定向第一跳凭证冲突")
	}
}
