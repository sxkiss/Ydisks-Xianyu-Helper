package renew

import (
	"net/http"
	"time"

	"xianyu-go/internal/xianyu/cookierefresh"
)

// responseCookieBatch 保存一次 HTTP 响应的 Cookie 及其原始作用域、头部接收时间。
// 所有字段只在内存与凭证持久化路径使用，不允许作为诊断文本输出。
type responseCookieBatch struct {
	// url 决定 HostOnly Cookie 和默认 Path，不能使用重定向链的最终 URL 替代。
	url string
	// headers 是当前响应的原始 Set-Cookie 列表，包含明文凭证。
	headers []string
	// receivedAt 固定 Max-Age 的起算时刻，不能在迟到重放时重新计时。
	receivedAt time.Time
}

// renewalCookieTransport 为一次续期拥有独立 Jar；只由该次 hc.Do 协程串行访问，
// 不与其他续期共享可变状态。HTTP 重定向在下一跳发送前即应用上一跳 Cookie。
type renewalCookieTransport struct {
	// base 是只读共享的底层传输，保留调用方代理、TLS 和测试注入配置。
	base http.RoundTripper
	// snapshot 非 nil 表示权威 Jar，保留 Domain/Path/HttpOnly/PartitionKey。
	snapshot []cookierefresh.BrowserCookie
	// flat 是历史扁平 Cookie 的降级视图，不能推断丢失的域或 HttpOnly 属性。
	flat string
	// initialHost 限制扁平 Cookie 只发送给最初主机，防止跨域重定向泄露凭证。
	initialHost string
	// responses 按接收顺序记录每跳响应，只在请求协程结束后交给持久化方读取。
	responses []responseCookieBatch
}

// RoundTrip 按 t 的当前 Jar 设置 req 的 Cookie，并在返回响应头前合并服务端更新。
// 返回底层响应及错误；不持锁、不读取正文，遵守 req 的取消与续期总时限。
func (t *renewalCookieTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// outgoing 是本跳独占的请求副本，不修改 http.Client 保存的重定向初始请求。
	outgoing := req.Clone(req.Context())
	if t.snapshot != nil {
		// header 是当前目标 URL 和顶层站点可携带的 Cookie，不包含越域凭证。
		header, _ := cookierefresh.ScopedCookieHeaderForRequest(t.snapshot, req.URL.String(), goofishTopSite, time.Now())
		outgoing.Header.Set("Cookie", header)
	} else if req.URL.Hostname() == t.initialHost {
		outgoing.Header.Set("Cookie", t.flat)
	} else {
		outgoing.Header.Del("Cookie")
	}
	// response、err 保留底层 HTTP 结果；有响应时先保存其 Cookie，再交给 Client 处理重定向。
	response, err := t.base.RoundTrip(outgoing)
	if response != nil {
		// batch 记录本跳原始 URL、响应头与实际接收时刻，正文读取失败也不丢失。
		batch := responseCookieBatch{url: req.URL.String(), headers: filterValidSetCookies(response.Header.Values("Set-Cookie")), receivedAt: time.Now()}
		if t.snapshot == nil && req.URL.Hostname() != t.initialHost {
			// 历史扁平格式无法保存外域属性，不能把外站 Cookie 混入原账号凭证。
			batch.headers = nil
		}
		t.responses = append(t.responses, batch)
		if t.snapshot != nil {
			t.snapshot = cookierefresh.ApplySetCookies(t.snapshot, batch.url, batch.headers, batch.receivedAt, goofishTopSite)
		} else if req.URL.Hostname() == t.initialHost {
			t.flat = MergeSetCookies(t.flat, batch.headers)
		}
	}
	return response, err
}

// requestClient 为 hc 创建只属于 req 的 Cookie 传输包装，并返回用于读取响应链的状态。
// snapshot 为权威完整 Jar 或 nil；不修改共享 Client、Transport 或 CookieJar。
func requestClient(hc *http.Client, req *http.Request, snapshot []cookierefresh.BrowserCookie) (*http.Client, *renewalCookieTransport) {
	// transport 的可变状态由当前请求协程独占，生命周期不超过底层请求硬超时。
	transport := &renewalCookieTransport{base: hc.Transport, flat: req.Header.Get("Cookie"), initialHost: req.URL.Hostname()}
	if transport.base == nil {
		transport.base = http.DefaultTransport
	}
	if snapshot != nil {
		transport.snapshot = append([]cookierefresh.BrowserCookie{}, snapshot...)
	}
	// client 复制调用方网络配置；Cookie 仅由完整快照管理，不能混入共享 Jar 的其他账号数据。
	client := *hc
	client.Transport, client.Jar = transport, nil
	return &client, transport
}

// applyResponseCookies 将 r 的每跳 Set-Cookie 按原始域、路径和接收时间重放到 current。
// 返回独立完整快照，不覆盖响应以外的并发 Cookie；旧构造结果兼容单响应格式。
func (r *Result) applyResponseCookies(current []cookierefresh.BrowserCookie) []cookierefresh.BrowserCookie {
	if len(r.responseCookies) == 0 {
		return cookierefresh.ApplySetCookies(current, r.cookieResponseURL(), r.SetCookies, r.cookieReceivedAt(), goofishTopSite)
	}
	// batch 是按响应头接收顺序重放的 Cookie 更新，不输出明文。
	for _, batch := range r.responseCookies {
		current = cookierefresh.ApplySetCookies(current, batch.url, batch.headers, batch.receivedAt, goofishTopSite)
	}
	return current
}

// cookieResponseURL 返回 r 的单响应作用域；历史构造结果默认来自官方静默续期端点。
func (r *Result) cookieResponseURL() string {
	if r.responseCookieURL == "" {
		return SilentHasLoginURL
	}
	return r.responseCookieURL
}
