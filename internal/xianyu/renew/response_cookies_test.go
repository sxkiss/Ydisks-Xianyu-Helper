package renew

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"xianyu-go/internal/xianyu/cookierefresh"
)

// TestRenewRedirectCookieChain 验证中间跳 Cookie 在下一跳发送并在最终失败时仍保留；t 管理人工传输样本。
func TestRenewRedirectCookieChain(t *testing.T) {
	// outcome 指定最终响应是成功、正文错误、HTTP 错误还是传输错误。
	for _, outcome := range []string{"success", "body-error", "http-error", "network-error"} {
		// t 隔离每种最终响应状态的计数和断言。
		t.Run(outcome, func(t *testing.T) {
			// calls 是当前单一请求协程使用的传输次数，主测试只在请求结束后读取。
			calls := 0
			// client 模拟官方域名上的两跳请求；req 的 Cookie 仅含人工样本。
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"/newlogin/next"}, "Set-Cookie": {"redirect=fixture; Secure; HttpOnly"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				if !strings.Contains(req.Header.Get("Cookie"), "redirect=fixture") || req.URL.Path != "/newlogin/next" {
					t.Error("重定向下一跳没有使用最新 Cookie Jar")
				}
				if outcome == "network-error" {
					return nil, errors.New("fixture network failure")
				}
				// response 模拟最终业务数据，并签发一个跨 goofish 子域可用的新 Cookie。
				response := &http.Response{StatusCode: 200, Header: http.Header{"Set-Cookie": {"final=fixture; Domain=.goofish.com; Path=/; Secure"}}, Body: io.NopCloser(strings.NewReader(`{"content":{"data":{"processFinished":true,"resultCode":100}}}`))}
				if outcome == "body-error" {
					response.Body = failingReadCloser{err: errors.New("fixture body failure")}
				}
				if outcome == "http-error" {
					response.StatusCode = 503
				}
				return response, nil
			})}
			// initial 只包含脚本可见的长登录标记，重定向 Cookie 必须由响应产生。
			initial := []cookierefresh.BrowserCookie{{Name: "havana_lgc_exp", Value: futureMillis(time.Hour), Domain: ".goofish.com", Path: "/", Secure: true}}
			// result、err 保存整条重定向链的执行终态。
			result, err := (Service{HTTPClient: client, RetryDelay: -1}).RenewAPIFirst(context.Background(), "", initial)
			if calls != 2 || result == nil || result.HasPending() || result.Success != (outcome == "success") {
				t.Fatal("重定向执行次数或终态不一致")
			}
			if (err != nil) != (outcome == "body-error" || outcome == "network-error") {
				t.Fatal("最终错误未按原有错误契约传播")
			}
			// found 标记中间跳 HostOnly Cookie 是否保留了默认路径和 HttpOnly。
			found := false
			// cookie 是逐条核验作用域的响应 Cookie，不写入测试输出。
			for _, cookie := range result.CookieSnapshot {
				if cookie.Name == "redirect" {
					found = cookie.Domain == "passport.goofish.com" && cookie.Path == "/newlogin" && cookie.HTTPOnly
				}
			}
			if !found || strings.Contains(result.NewCookies, "redirect=") {
				t.Fatal("中间跳 Cookie 丢失或泄露到 www.goofish.com")
			}
			// latest 模拟 HTTP 请求期间其他流程更新的完整 Jar，必须与响应链同时保留。
			latest := append(initial, cookierefresh.BrowserCookie{Name: "concurrent", Value: "fixture", Domain: ".goofish.com", Path: "/", Secure: true})
			// header、metadata、changed 检查在最新 Jar 上重放完整响应链的结果。
			header, metadata, changed := RebaseResponseCookies("", cookierefresh.MetadataWithSnapshot("", latest), result)
			if !changed || !strings.Contains(header, "concurrent=fixture") || strings.Contains(header, "redirect=") || !strings.Contains(metadata, "redirect") {
				t.Fatal("重放重定向响应覆盖并发 Cookie 或丢失原始作用域")
			}
		})
	}
}

// TestRenewRedirectScopesAndSharedClient 验证跨域重定向不携带原站凭证，且不修改共享 HTTP Client；t 拥有模拟请求。
func TestRenewRedirectScopesAndSharedClient(t *testing.T) {
	// authoritative 区分完整 Jar 和历史扁平凭证，两个模式都不能向外域发送初始 Cookie。
	for _, authoritative := range []bool{false, true} {
		// calls 记录当前请求链的跳数，仅由请求协程更新。
		calls := 0
		// transport 模拟跳到其他站点的响应，req 仅使用测试域名和人工凭证。
		transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://other.example/next"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			if req.Header.Get("Cookie") != "" {
				t.Error("跨域重定向泄露原站 Cookie")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Set-Cookie": {"foreign=fixture; Path=/; Secure"}}, Body: io.NopCloser(strings.NewReader(`{"content":{"data":{"processFinished":true,"resultCode":100}}}`))}, nil
		})
		// client 保留调用方传输配置，执行前后 Transport 类型和 Jar 均不应被替换。
		client := &http.Client{Transport: transport}
		// snapshot 仅在权威模式提供，Cookie 判断标记使用人工未来时刻。
		var snapshot []cookierefresh.BrowserCookie
		if authoritative {
			snapshot = []cookierefresh.BrowserCookie{{Name: "havana_lgc_exp", Value: futureMillis(time.Hour), Domain: ".goofish.com", Path: "/", Secure: true}}
		}
		// result、err 是跨域链最终返回值，主要检查 Cookie 隔离而非 CORS 浏览器策略。
		result, err := (Service{HTTPClient: client, RetryDelay: -1}).RenewAPIFirst(context.Background(), "havana_lgc_exp="+futureMillis(time.Hour), snapshot)
		if err != nil || result == nil || calls != 2 || client.Jar != nil {
			t.Fatal("请求包装修改了共享客户端或丢失重定向")
		}
		if strings.Contains(result.NewCookies, "foreign=") {
			t.Fatal("外站 Cookie 不得混入原站请求头")
		}
		if _, ok := client.Transport.(roundTripFunc); !ok { // ok 检查共享传输仍为调用方的原始实现。
			t.Fatal("共享 Transport 被修改")
		}
	}
}

// TestRequestClientUsesDefaultTransport 验证未指定传输时沿用 Go 默认连接池；t 管理无网络构造测试。
func TestRequestClientUsesDefaultTransport(t *testing.T) {
	// request、err 保存合法的人工 URL，不实际发送请求。
	request, err := http.NewRequest(http.MethodPost, SilentHasLoginURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	// client、transport 验证私有客户端引用默认传输但拥有自己的空权威 Jar。
	client, transport := requestClient(&http.Client{}, request, []cookierefresh.BrowserCookie{})
	if client.Transport != transport || transport.base != http.DefaultTransport || transport.snapshot == nil {
		t.Fatal("默认传输或空权威 Jar 的构造语义错误")
	}
}
