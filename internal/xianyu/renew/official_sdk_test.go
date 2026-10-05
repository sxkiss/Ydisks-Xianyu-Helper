package renew

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xianyu-go/internal/xianyu/cookierefresh"
)

// TestOfficialCookieParser 验证官方插件的 URI 解码、末值覆盖和整体解析失败语义；t 管理测试断言。
func TestOfficialCookieParser(t *testing.T) {
	if len(autoLoginCookieValues("")) != 0 {
		t.Fatal("空 document.cookie 必须解析为空表")
	}
	// cases 仅使用人工样本，want 是插件解析后选定的到期值。
	cases := []struct {
		// input 是模拟 document.cookie 的完整文本。
		input string
		// want 是 havana_lgc_exp 的解码结果；空串也覆盖整表解析失败。
		want string
	}{
		{"havana_lgc_exp=1; havana_lgc_exp=2", "2"},
		{"havana_lgc_exp=%32%30%30%30", "2000"},
		{"havana_lgc_exp=+2000", "+2000"},
		{"havana_lgc_exp=2000=ignored", "2000"},
		{"havana_lgc_exp=2000; other=%ZZ", ""},
		{"havana_lgc_exp=2000; other=%FF", ""},
		{"%ZZ=bad; havana_lgc_exp=2000", ""},
		{"havana_lgc_exp=2000; other", "2000"},
		{"havana_lgc_exp=2000;havana_lgc_exp=3000", "2000;havana_lgc_exp"},
	}
	// sample 是当前待核对的无敏感值 Cookie 样本。
	for _, sample := range cases {
		if autoLoginCookieValues(sample.input)["havana_lgc_exp"] != sample.want {
			t.Errorf("官方 Cookie 解析不一致，样本=%q", sample.input)
		}
	}
}

// TestOfficialDateComparison 验证 Number、TimeClip、空值与 Invalid Date 的两种不同判定；t 接收失败。
func TestOfficialDateComparison(t *testing.T) {
	// now 固定在 epoch 后一秒，排除真实时钟对边界结果的影响。
	now := time.UnixMilli(1000)
	// cases 分别指定疲劳窗口（大于）和长登录分支（非小于等于）的预期。
	cases := []struct {
		// raw 是 SDK 读到的原始字符串。
		raw string
		// fatigue 表示严格大于当前时间。
		fatigue bool
		// usable 表示原值非空且不满足到期比较。
		usable bool
	}{
		{"", false, false}, {" ", false, false}, {"1000", false, false},
		{"1000.9", false, false}, {"1001.9", true, true}, {"2e3", true, true},
		{"0x7d0", true, true}, {"0o3720", true, true}, {"0b11111010000", true, true},
		{"+2000", true, true}, {"-2000", false, false},
		{"invalid", false, true}, {"Infinity", false, true}, {"-Infinity", false, true},
		{"NaN", false, true}, {"8640000000000001", false, true},
		{"1_000", false, true}, {"0x1p10", false, true}, {"+0x7d0", false, true},
		{"0xzz", false, true},
		{"\ufeff2000\ufeff", true, true},
		{" \ufeff 2000 \ufeff ", true, true}, {"\u00852000", false, true},
	}
	// sample 是当前被测试的数值文本及两种比较预期。
	for _, sample := range cases {
		if strictCookieTimeAfter(sample.raw, now) != sample.fatigue || cookieTimeAfter(sample.raw, now) != sample.usable {
			t.Errorf("官方日期比较不一致，样本=%q", sample.raw)
		}
	}
}

// TestOfficialSuccessIsStrict 验证严格布尔、数值相等及嵌套 content 优先级；t 管理全部人工 JSON 样本。
func TestOfficialSuccessIsStrict(t *testing.T) {
	// success 是官方唯一成功数据结构的人工文本。
	const success = `{"data":{"processFinished":true,"resultCode":100}}`
	// cases 保存输入响应及期望，不发送平台请求。
	cases := []struct {
		// body 是被解析的 JSON。
		body string
		// want 为官方 Promise 是否 resolve。
		want bool
	}{
		{`{"content":` + success + `}`, true},
		{`{"content":{"data":{"processFinished":true,"resultCode":100.5}}}`, false},
		{`{"content":{"data":{"processFinished":true,"resultCode":1e2}}}`, true},
		{`{"content":{"data":{"processFinished":"true","resultCode":100}}}`, false},
		{`{"content":{"data":{"processFinished":true,"resultCode":"100"}}}`, false},
		{`{"data":{"content":true},"content":` + success + `}`, false},
		{`{"data":{"content":[]},"content":` + success + `}`, false},
		{`{"data":{"content":{}},"content":` + success + `}`, false},
		{`{"data":{"content":"nonempty"},"content":` + success + `}`, false},
		{`{"data":{"content":0},"content":` + success + `}`, true},
		{`{"data":{"content":false},"content":` + success + `}`, true},
		{`{"data":{"content":""},"content":` + success + `}`, true},
		{`{"data":{"content":null},"content":` + success + `}`, true},
	}
	// sample 是当前人工业务响应。
	for _, sample := range cases {
		if renewBusinessOK([]byte(sample.body)) != sample.want {
			t.Errorf("官方成功判定不一致，样本=%s", sample.body)
		}
	}
}

// TestOfficialPromiseStopsTimingAtHeaders 验证响应头在期限内到达后，慢正文仍可成功；t 拥有本地服务。
func TestOfficialPromiseStopsTimingAtHeaders(t *testing.T) {
	// server 返回即时响应头，但正文延迟超过注入的 Promise 窗口。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Set-Cookie", "rotation=fixture; Path=/")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, `{"content":{"data":{"processFinished":true,"resultCode":100}}}`)
	}))
	defer server.Close()
	// service 使用本地网络，缩短头部等待窗口而不改变正文阶段。
	service := Service{HTTPClient: server.Client(), SilentHasLoginURL: server.URL, RetryDelay: -1, PromiseTimeout: 30 * time.Millisecond}
	// result、err 是完整正文到达后的续期状态，禁止出现 pending 或丢失 Cookie。
	result, err := service.RenewAPIFirst(context.Background(), "havana_lgc_exp="+futureMillis(time.Hour))
	if err != nil || result.HasPending() || !result.Success || len(result.SetCookies) != 1 {
		t.Fatalf("响应头应赢得竞速，err=%v", err)
	}
}

// TestOfficialBranchChosenAfterDelay 验证等待期间 Havana 到期后改走 Cookie3；t 拥有请求断言。
func TestOfficialBranchChosenAfterDelay(t *testing.T) {
	// client 的回调只检查分支参数，req 不含真实账号凭证。
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("c2r") != "true" || req.URL.Query().Has("ltl") {
			t.Error("Havana 到期后未选择 Cookie3")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"content":{"data":{"processFinished":true,"resultCode":100}}}`))}, nil
	})}
	// service 等待时间显著大于 Havana 剩余时间，以覆盖跨到期边界。
	service := Service{HTTPClient: client, RetryDelay: 80 * time.Millisecond}
	// result、err 保存 SDK 分支调用的终态。
	result, err := service.RenewAPIFirst(context.Background(), "havana_lgc_exp="+futureMillis(20*time.Millisecond)+"; cookie3_bak_exp="+futureMillis(time.Hour))
	if err != nil || !result.Success || result.RequestCount != 1 {
		t.Fatalf("等待后续期失败，err=%v", err)
	}
}

// TestOfficialHTTPOnlyIsSentButNotRead 验证脚本不读取 HttpOnly 疲劳标记、HTTP 仍发送它；t 拥有合成快照。
func TestOfficialHTTPOnlyIsSentButNotRead(t *testing.T) {
	// snapshot 同时携带脚本可见长登录标记和脚本不可见疲劳标记。
	snapshot := []cookierefresh.BrowserCookie{
		{Name: "havana_lgc_exp", Value: futureMillis(time.Hour), Domain: ".goofish.com", Path: "/", Secure: true},
		{Name: "sdkSilent", Value: futureMillis(time.Hour), Domain: ".goofish.com", Path: "/", Secure: true, HTTPOnly: true},
	}
	// client 检查请求 Cookie 是否保留 HttpOnly 标记；req 仅含人工值。
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.Contains(req.Header.Get("Cookie"), "sdkSilent=") {
			t.Error("HTTP 请求错误丢弃 HttpOnly Cookie")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"content":{"data":{"processFinished":true,"resultCode":100}}}`))}, nil
	})}
	// result、err 用于确认 HttpOnly 疲劳标记没有阻止脚本续期。
	result, err := (Service{HTTPClient: client, RetryDelay: -1}).RenewAPIFirst(context.Background(), "", snapshot)
	if err != nil || !result.Success || result.Skipped {
		t.Fatalf("HttpOnly 标记不应参与 SDK 分支，err=%v", err)
	}
}

// TestOfficialQueryEncoding 验证 encodeURIComponent 和去掉页面 query 的 documentReferer；t 管理固定样本。
func TestOfficialQueryEncoding(t *testing.T) {
	if encodeURIComponent(" !'()*~+/") != "%20!'()*~%2B%2F" {
		t.Fatal("编码与 encodeURIComponent 不一致")
	}
	if (Service{DocumentReferer: "https://www.goofish.com/im?spm=fixture#hash"}).documentReferer() != goofishIMDocumentURL {
		t.Fatal("documentReferer 不应包含页面 query")
	}
}

// TestOfficialDelayCancellation 验证等待前取消不会发起网络请求；t 管理取消场景断言。
func TestOfficialDelayCancellation(t *testing.T) {
	// ctx、cancel 是已取消的请求生命周期，覆盖正常等待与测试跳过等待两种设置。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// delay 是两种允许的等待配置，均不得吞掉调用方取消。
	for _, delay := range []time.Duration{0, -1} {
		if (Service{RetryDelay: delay}).waitAutoLoginDelay(ctx) != context.Canceled {
			t.Fatal("等待前取消未被传播")
		}
	}
}

// TestOfficialCookieMaxAgeUsesHeaderTime 验证晚到响应重放不从持久化时刻重新计算有效期；t 管理合成快照。
func TestOfficialCookieMaxAgeUsesHeaderTime(t *testing.T) {
	// received 是人工响应头时间，足以让一秒有效期在重放时已经到期。
	received := time.Now().Add(-time.Minute)
	// result 保留短期 Cookie 和响应头时间，不包含真实账号数据。
	result := &Result{SetCookies: []string{"short=fixture; Domain=.goofish.com; Path=/; Max-Age=1; Secure"}, responseReceivedAt: received}
	// metadata 标记权威空 Jar，避免因历史扁平格式丢失属性。
	metadata := cookierefresh.MetadataWithSnapshot("", []cookierefresh.BrowserCookie{})
	// header、updated、changed 是基于当前 Jar 的迟到响应重放结果。
	header, updated, changed := RebaseResponseCookies("", metadata, result)
	if !changed || strings.Contains(header, "short=") {
		t.Fatal("迟到 Cookie 不能重新获得 Max-Age 有效期")
	}
	// snapshot、complete 验证存储的到期时间与收到响应头的时刻相符。
	snapshot, complete := cookierefresh.SnapshotFromMetadataOK(updated)
	if !complete || len(snapshot) != 1 || snapshot[0].Expires != float64(received.Add(time.Second).Unix()) {
		t.Fatal("响应头时间没有用于 Max-Age 计算")
	}
}
