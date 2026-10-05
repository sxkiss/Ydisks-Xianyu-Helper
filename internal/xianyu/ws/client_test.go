package ws

import (
	"encoding/base64"
	"testing"

	"xianyu-go/internal/xianyu"
)

// TestWebsocketHeadersMatchBrowserHandshake 封装TestWebsocketHeadersMatch浏览器Handshake业务协调。
func TestWebsocketHeadersMatchBrowserHandshake(t *testing.T) {
	xianyu.SetBrowserFingerprint(xianyu.BrowserFingerprint{UserAgent: "runtime-browser-ua"})
	// got 用于本次流程后续判断的got
	got := websocketHeaders()
	if got.Get("Origin") != "https://www.goofish.com" || got.Get("User-Agent") != "runtime-browser-ua" {
		t.Fatalf("websocket headers = %#v", got)
	}
	if got.Get("Cookie") != "" {
		t.Fatalf("dingtalk WebSocket 不应收到 goofish Cookie: %#v", got)
	}
}

// TestOfficialRegistrationUAUsesRuntimeBrowserVersion 封装TestOfficialRegistrationUAUsesRuntime浏览器Version业务协调。
func TestOfficialRegistrationUAUsesRuntimeBrowserVersion(t *testing.T) {
	// raw 用于本次流程后续判断的原始
	raw := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/138.0.7204.92 Safari/537.36"
	// want 用于本次流程后续判断的want
	want := raw + " DingTalk(2.2.0) OS(Mac OS/10.15.7) Browser(Chrome/138.0.7204.92) DingWeb/2.2.0 IMPaaS DingWeb/2.2.0"
	if // got 用于本次流程后续判断的got
	got := OfficialRegistrationUA(raw); got != want {
		t.Fatalf("OfficialRegistrationUA() = %q, want %q", got, want)
	}
}

// TestOfficialRegistrationUARecognizesHeadlessChrome 封装TestOfficialRegistrationUARecognizesHeadlessChrome业务协调。
func TestOfficialRegistrationUARecognizesHeadlessChrome(t *testing.T) {
	// raw 用于本次流程后续判断的原始
	raw := "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 HeadlessChrome/138.0.7204.92 Safari/537.36"
	// want 用于本次流程后续判断的want
	want := raw + " DingTalk(2.2.0) OS(Linux/other) Browser(Chrome Headless/138.0.7204.92) DingWeb/2.2.0 IMPaaS DingWeb/2.2.0"
	if // got 用于本次流程后续判断的got
	got := OfficialRegistrationUA(raw); got != want {
		t.Fatalf("OfficialRegistrationUA() = %q, want %q", got, want)
	}
}

// TestExtractSyncPayloads 验证同一同步帧中的每个条目都被保留，避免后续付款卡片被 data[0] 遮蔽。
func TestExtractSyncPayloads(t *testing.T) {
	// msg 用于本次流程后续判断的msg
	msg := map[string]any{"body": map[string]any{"syncPushPackage": map[string]any{
		"data": []any{map[string]any{"data": "first"}, map[string]any{"data": "second"}, map[string]any{"data": 1}},
	}}}
	// got 和 ok 分别保存完整条目列表及同步包形状是否有效。
	got, ok := extractSyncPayloads(msg)
	if !ok || len(got) != 3 || !got[0].valid || got[0].data != "first" || !got[1].valid || got[1].data != "second" || got[2].valid {
		t.Fatalf("extractSyncPayloads() = %#v, %v", got, ok)
	}
	// invalid 表示当前遍历过程中的缺少同步条目的帧；空数组沿用旧行为按非同步帧跳过。
	for _, invalid := range []map[string]any{{}, {"body": map[string]any{}}, {"body": map[string]any{"syncPushPackage": map[string]any{"data": []any{}}}}} {
		if // ok 用于本次流程后续判断的ok
		_, ok := extractSyncPayloads(invalid); ok {
			t.Fatalf("invalid payload accepted: %#v", invalid)
		}
	}
}

// TestDecodeSyncDataJSONAndInvalid 封装TestDecodeSync数据JSONAndInvalid业务协调。
func TestDecodeSyncDataJSONAndInvalid(t *testing.T) {
	// raw 用于本次流程后续判断的原始
	raw := base64.StdEncoding.EncodeToString([]byte(`{"event":"paid","count":2}`))
	// got、err 用于本次流程后续判断的got、err
	got, err := decodeSyncData(raw)
	if err != nil || got["event"] != "paid" || got["count"] != float64(2) {
		t.Fatalf("decodeSyncData() = %#v, %v", got, err)
	}
	if // err 用于本次流程后续判断的err
	_, err := decodeSyncData("not-base64"); err == nil {
		t.Fatal("invalid payload should fail")
	}
}

// TestWSHelpers 封装TestWSHelpers业务协调。
func TestWSHelpers(t *testing.T) {
	if // got 用于本次流程后续判断的got
	got := stripGoofish(" 123@goofish "); got != "123" {
		t.Fatalf("stripGoofish = %q", got)
	}
}
