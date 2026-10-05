package mtop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestSearchChatItemsUsesOfficialPersonalConversationContract 验证个人会话商品查询的平台参数、分页和字段归一化。
func TestSearchChatItemsUsesOfficialPersonalConversationContract(t *testing.T) {
	// requestCount 记录成功页与空页的请求次数。
	var requestCount atomic.Int32
	// server 是断言官网 MTOP 请求并返回确定性商品页的本地服务。
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		if request.Method != http.MethodPost || request.URL.Query().Get("api") != "mtop.taobao.idlemessage.pc.tool.item.search" || request.URL.Query().Get("v") != "1.0" {
			t.Fatalf("请求方法或 MTOP API 不符: method=%s query=%v", request.Method, request.URL.Query())
		}
		if request.Referer() != "https://www.goofish.com/im" {
			t.Fatalf("Referer=%q", request.Referer())
		}
		// parseErr 表示商品查询表单是否可以正常解析。
		if parseErr := request.ParseForm(); parseErr != nil {
			t.Fatalf("解析商品查询表单失败: %v", parseErr)
		}
		// payload 是官网 data 表单字段解码后的查询参数。
		var payload map[string]any
		// decodeErr 表示 data 字段是否为合法 JSON 对象。
		if decodeErr := json.Unmarshal([]byte(request.Form.Get("data")), &payload); decodeErr != nil {
			t.Fatalf("解析商品查询正文失败: %v", decodeErr)
		}
		if payload["sessionId"] != "chat-1" || payload["pageSize"] != float64(20) || payload["searchItemRole"] != "other" || payload["queryWord"] != "麦克风" {
			t.Fatalf("商品查询正文=%v", payload)
		}
		if payload["pageNumber"] == float64(1) {
			http.SetCookie(responseWriter, &http.Cookie{Name: "trace_cookie", Value: "fresh", Path: "/"})
			_, _ = fmt.Fprint(responseWriter, `{"ret":["SUCCESS::调用成功"],"data":{"items":[{"itemId":"item-1","title":"测试商品","absoluteMajorPicture":"//img.example/item.png","reservePrice":"19.90","desc":"商品摘要"},{"itemId":"","title":"无效商品"}],"nextPage":true}}`)
			return
		}
		_, _ = fmt.Fprint(responseWriter, `{"ret":["SUCCESS::调用成功"],"data":{"items":[],"nextPage":false,"currentPage":99,"hasMore":true}}`)
	}))
	defer server.Close()
	// client 是将聊天商品查询端点指向本地服务的 MTOP 客户端。
	client := &ClientImpl{HTTPClient: server.Client(), ChatItemSearchURL: server.URL}
	// firstPage 和 firstErr 是包含下一页标记和 Cookie 写回的第一页结果。
	firstPage, firstErr := client.SearchChatItems(context.Background(), "_m_h5_tk=token_1; _m_h5_tk_enc=enc", "chat-1", "other", "麦克风", 1)
	if firstErr != nil || firstPage == nil || len(firstPage.Items) != 1 || !firstPage.HasMore || firstPage.Page != 1 || !strings.Contains(firstPage.UpdatedCookies, "trace_cookie=fresh") {
		t.Fatalf("firstPage=%+v err=%v", firstPage, firstErr)
	}
	if firstPage.Items[0].ImageURL != "https://img.example/item.png" || firstPage.Items[0].Description != "商品摘要" {
		t.Fatalf("item=%+v", firstPage.Items[0])
	}
	// emptyPage 和 emptyErr 验证官网 nextPage 优先且空 items 是正常空页。
	emptyPage, emptyErr := client.SearchChatItems(context.Background(), "_m_h5_tk=token_1; _m_h5_tk_enc=enc", "chat-1", "other", "麦克风", 2)
	if emptyErr != nil || emptyPage == nil || len(emptyPage.Items) != 0 || emptyPage.HasMore || emptyPage.Page != 2 || requestCount.Load() != 2 {
		t.Fatalf("emptyPage=%+v calls=%d err=%v", emptyPage, requestCount.Load(), emptyErr)
	}
}

// TestSearchChatItemsRetriesUpdatedToken 验证 Token 过期时使用响应 Cookie 重签并返回最终商品页。
func TestSearchChatItemsRetriesUpdatedToken(t *testing.T) {
	// requestCount 记录首次过期和第二次成功请求。
	var requestCount atomic.Int32
	// server 首次返回 Token 过期并下发新 Cookie，第二次验证新 Cookie 后成功。
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		// attempt 是当前请求从 1 开始的轮次。
		attempt := requestCount.Add(1)
		if attempt == 1 {
			http.SetCookie(responseWriter, &http.Cookie{Name: "_m_h5_tk", Value: "fresh_2", Path: "/"})
			http.SetCookie(responseWriter, &http.Cookie{Name: "_m_h5_tk_enc", Value: "fresh-enc", Path: "/"})
			_, _ = fmt.Fprint(responseWriter, `{"ret":["FAIL_SYS_TOKEN_EXOIRED::令牌过期"],"data":{}}`)
			return
		}
		if !strings.Contains(request.Header.Get("Cookie"), "_m_h5_tk=fresh_2") {
			t.Fatalf("第二次请求未使用更新 Cookie: %q", request.Header.Get("Cookie"))
		}
		_, _ = fmt.Fprint(responseWriter, `{"ret":["SUCCESS::调用成功"],"data":{"items":[],"nextPage":false}}`)
	}))
	defer server.Close()
	// client 是执行 Token 过期重试的本地 MTOP 客户端。
	client := &ClientImpl{HTTPClient: server.Client(), ChatItemSearchURL: server.URL}
	// result 和 searchErr 是 Token 恢复后的空商品页结果。
	result, searchErr := client.SearchChatItems(context.Background(), "_m_h5_tk=old_1; _m_h5_tk_enc=old-enc", "chat-1", "own", "", 1)
	if searchErr != nil || result == nil || requestCount.Load() != 2 || !strings.Contains(result.UpdatedCookies, "_m_h5_tk=fresh_2") {
		t.Fatalf("result=%+v calls=%d err=%v", result, requestCount.Load(), searchErr)
	}
}

// TestSearchChatItemsRejectsInvalidAndMalformedResponses 验证无效参数和异常平台响应不会伪装为空页。
func TestSearchChatItemsRejectsInvalidAndMalformedResponses(t *testing.T) {
	// server 是返回畸形 JSON 的本地商品查询服务。
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(responseWriter, `{`) }))
	defer server.Close()
	// client 是把商品查询指向畸形响应服务的 MTOP 客户端。
	client := &ClientImpl{HTTPClient: server.Client(), ChatItemSearchURL: server.URL}
	// invalidErr 是不合法会话、角色和页码产生的前置校验错误。
	if _, invalidErr := client.SearchChatItems(context.Background(), "", "", "peer", "", 0); invalidErr == nil {
		t.Fatal("无效商品查询参数应被拒绝")
	}
	// malformedErr 是平台返回畸形 JSON 时的解析错误。
	if _, malformedErr := client.SearchChatItems(context.Background(), "_m_h5_tk=token_1", "chat-1", "own", "", 1); malformedErr == nil {
		t.Fatal("畸形平台响应应返回错误")
	}
}
