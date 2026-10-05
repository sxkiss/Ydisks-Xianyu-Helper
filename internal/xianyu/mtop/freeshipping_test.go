package mtop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestFreeShippingContextSendsPlatformPayload 验证免拼发货请求使用独立 MTOP API，且订单、商品和买家标识保持平台要求的 JSON 类型。
func TestFreeShippingContextSendsPlatformPayload(t *testing.T) {
	// server 是检查免拼 MTOP 查询、表单和 Cookie 作用域的本地平台替身。
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Query().Get("api") != "mtop.idle.groupon.activity.seller.freeshipping" || request.URL.Query().Get("type") != "originaljson" {
			t.Fatalf("免拼请求元信息错误: method=%s query=%v", request.Method, request.URL.Query())
		}
		if !strings.Contains(request.Header.Get("Cookie"), "_m_h5_tk=token_1") {
			t.Fatalf("免拼请求未携带签名 Cookie: %q", request.Header.Get("Cookie"))
		}
		// parseErr 保存表单解析失败，失败时无法继续验证签名 JSON 负载。
		if parseErr := request.ParseForm(); parseErr != nil {
			t.Fatalf("解析免拼表单失败: %v", parseErr)
		}
		// payload 保存免拼接口接收到的业务标识；数字字段必须保持为 JSON number。
		var payload struct {
			OrderID string `json:"bizOrderId"`
			ItemID  uint64 `json:"itemId"`
			BuyerID uint64 `json:"buyerId"`
		}
		// decodeErr 保存 data 字段 JSON 解码失败。
		if decodeErr := json.Unmarshal([]byte(request.FormValue("data")), &payload); decodeErr != nil {
			t.Fatalf("解析免拼 data 失败: %v", decodeErr)
		}
		if payload.OrderID != "order-1" || payload.ItemID != 10001 || payload.BuyerID != 20002 {
			t.Fatalf("免拼业务负载错误: %+v", payload)
		}
		_, _ = fmt.Fprint(writer, `{"ret":["SUCCESS::调用成功"]}`)
	}))
	defer server.Close()
	// client 是注入本地端点的 MTOP 客户端，避免测试访问真实平台。
	client := &ClientImpl{HTTPClient: server.Client(), FreeShippingURL: server.URL + "/"}
	// ok、ret、updated、callErr 保存免拼请求返回的业务结果与 Cookie 结果。
	ok, ret, updated, callErr := client.FreeShippingContext(context.Background(), consignCookies, "order-1", "10001", "20002")
	if callErr != nil || !ok || len(ret) != 1 || updated != consignCookies {
		t.Fatalf("免拼结果异常: ok=%v ret=%v updated=%q err=%v", ok, ret, updated, callErr)
	}
}

// TestFreeShippingContextRetriesWithRotatedToken 验证免拼端点在平台下发新签名 Cookie 后使用该 Cookie 重试原业务请求。
func TestFreeShippingContextRetriesWithRotatedToken(t *testing.T) {
	// requests 统计平台替身看到的免拼请求次数，首次 Token 过期后应只重试一次。
	var requests atomic.Int32
	// server 在首次请求下发新 Token 并返回过期，在第二次请求确认 Cookie 已轮换后成功。
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// attempt 是当前免拼请求的从一开始的序号。
		attempt := requests.Add(1)
		if attempt == 1 {
			http.SetCookie(writer, &http.Cookie{Name: "_m_h5_tk", Value: "fresh_2", Path: "/"})
			_, _ = fmt.Fprint(writer, `{"ret":["FAIL_SYS_TOKEN_EXOIRED::令牌过期"]}`)
			return
		}
		if !strings.Contains(request.Header.Get("Cookie"), "_m_h5_tk=fresh_2") {
			t.Fatalf("重试未使用平台下发的新 Token: %q", request.Header.Get("Cookie"))
		}
		_, _ = fmt.Fprint(writer, `{"ret":["SUCCESS::调用成功"]}`)
	}))
	defer server.Close()
	// client 是把免拼请求导向本地替身的 MTOP 客户端。
	client := &ClientImpl{HTTPClient: server.Client(), FreeShippingURL: server.URL + "/"}
	// ok、ret、updated、callErr 保存 Token 轮换重试后的最终业务结果。
	ok, ret, updated, callErr := client.FreeShippingContext(context.Background(), consignCookies, "order-2", "10001", "20002")
	if callErr != nil || !ok || len(ret) != 1 || requests.Load() != 2 || !strings.Contains(updated, "_m_h5_tk=fresh_2") {
		t.Fatalf("免拼 Token 重试异常: ok=%v ret=%v updated=%q requests=%d err=%v", ok, ret, updated, requests.Load(), callErr)
	}
}

// TestFreeShippingContextRetriesWhenGrouponInstanceIsNotReady 验证团购实例尚未同步时只等待并重试免拼请求，不触发 Token 刷新。
func TestFreeShippingContextRetriesWhenGrouponInstanceIsNotReady(t *testing.T) {
	// requests 统计平台替身收到的请求次数，前两次模拟团购查询暂未就绪。
	var requests atomic.Int32
	// server 模拟平台先返回团购实例查询错误，随后返回免拼成功。
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// attempt 表示当前请求在本测试中的顺序，用于控制平台替身何时恢复成功。
		attempt := requests.Add(1)
		if attempt < 3 {
			_, _ = fmt.Fprint(writer, `{"ret":["GROUPON_INSTANCE_QUERY_ERROR::团购查询失败"]}`)
			return
		}
		_, _ = fmt.Fprint(writer, `{"ret":["SUCCESS::调用成功"]}`)
	}))
	defer server.Close()
	// client 把免拼请求导向本地替身，避免测试访问真实平台。
	// logs 收集免拼阶段结构化日志，验证重试和成功信息均使用中文业务描述。
	var logs bytes.Buffer
	// client 把免拼请求导向本地替身，并注入测试日志器。
	client := &ClientImpl{HTTPClient: server.Client(), FreeShippingURL: server.URL + "/", Logger: slog.New(slog.NewTextHandler(&logs, nil))}
	// ctx、cancel 限制测试等待时间，确保重试逻辑不会在异常时无限等待。
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	// ok、ret、updated、callErr 保存团购实例同步重试后的最终结果。
	ok, ret, updated, callErr := client.FreeShippingContext(ctx, consignCookies, "order-sync", "10001", "20002")
	if callErr != nil || !ok || len(ret) != 1 || requests.Load() != 3 || updated != consignCookies {
		t.Fatalf("团购实例同步重试异常: ok=%v ret=%v updated=%q requests=%d err=%v", ok, ret, updated, requests.Load(), callErr)
	}
	if !strings.Contains(logs.String(), "免拼发货遇到团购实例暂未同步，等待后重试") || !strings.Contains(logs.String(), "免拼发货重试成功") {
		t.Fatalf("免拼重试中文日志不完整: %s", logs.String())
	}
}

// TestFreeShippingContextReturnsSystemFailureAfterGrouponRetryLimit 验证团购实例查询错误重试耗尽后仍返回系统失败，不伪装成业务拒绝。
func TestFreeShippingContextReturnsSystemFailureAfterGrouponRetryLimit(t *testing.T) {
	// requests 统计平台替身收到的请求次数，所有响应都保持团购实例查询失败。
	var requests atomic.Int32
	// server 模拟平台持续返回暂时性团购查询错误。
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		_, _ = fmt.Fprint(writer, `{"ret":["GROUPON_INSTANCE_QUERY_ERROR::团购查询失败"]}`)
	}))
	defer server.Close()
	// client 把免拼请求导向本地替身，验证达到上限后停止请求。
	client := &ClientImpl{HTTPClient: server.Client(), FreeShippingURL: server.URL + "/"}
	// ctx、cancel 限制测试等待时间，覆盖重试耗尽分支。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// _, _, _, callErr 保存重试耗尽后的系统错误。
	_, _, _, callErr := client.FreeShippingContext(ctx, consignCookies, "order-sync-failed", "10001", "20002")
	if callErr == nil || requests.Load() != 4 {
		t.Fatalf("团购实例同步失败结果异常: requests=%d err=%v", requests.Load(), callErr)
	}
	// kind、classified 保存最终错误分类，必须保持系统错误供上层重试或人工处理。
	kind, classified := MTopErrorKindOf(callErr)
	if !classified || kind != MTopErrorSystem {
		t.Fatalf("团购实例同步失败分类错误: kind=%q classified=%v err=%v", kind, classified, callErr)
	}
}

// TestFreeShippingContextRejectsNonNumericActivityIdentifiers 验证免拼端点拒绝非数字商品或买家标识，避免把未校验数据送往平台。
func TestFreeShippingContextRejectsNonNumericActivityIdentifiers(t *testing.T) {
	// client 是不应发起 HTTP 请求的零值客户端；参数校验必须在网络调用前完成。
	client := &ClientImpl{}
	// _, _, _, itemErr 保存商品标识非法时的本地参数校验错误。
	_, _, _, itemErr := client.FreeShippingContext(context.Background(), consignCookies, "order-3", "item-invalid", "20002")
	if itemErr == nil || !strings.Contains(itemErr.Error(), "商品ID不是数字") {
		t.Fatalf("商品标识非法错误=%v", itemErr)
	}
	// _, _, _, buyerErr 保存买家标识非法时的本地参数校验错误。
	_, _, _, buyerErr := client.FreeShippingContext(context.Background(), consignCookies, "order-3", "10001", "buyer-invalid")
	if buyerErr == nil || !strings.Contains(buyerErr.Error(), "买家ID不是数字") {
		t.Fatalf("买家标识非法错误=%v", buyerErr)
	}
}
