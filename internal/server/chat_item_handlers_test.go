package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	chatapp "xianyu-go/internal/application/chat"
)

// chatItemHandlerPort 在完整聊天测试端口上覆盖商品查询与发送结果。
type chatItemHandlerPort struct {
	// contractChatPort 提供与当前商品场景无关的聊天端口默认实现。
	contractChatPort
	// catalogUnavailable 和 sendingUnavailable 分别模拟查询与发送能力未装配。
	catalogUnavailable, sendingUnavailable bool
	// listErr 和 sendErr 分别是商品查询与发送用例返回的错误。
	listErr, sendErr error
	// outgoing 是商品发送失败时返回的本地状态消息。
	outgoing *chatapp.Message
}

// ItemCatalogAvailable 报告测试商品查询能力是否装配。
func (port *chatItemHandlerPort) ItemCatalogAvailable() bool { return !port.catalogUnavailable }

// ItemSendingAvailable 报告测试商品发送能力是否装配。
func (port *chatItemHandlerPort) ItemSendingAvailable() bool { return !port.sendingUnavailable }

// ListChatItems 返回固定商品页或预设应用错误。
func (port *chatItemHandlerPort) ListChatItems(context.Context, chatapp.ChatItemQuery) (chatapp.ChatItemPage, error) {
	return chatapp.ChatItemPage{Items: []chatapp.ChatItem{{ItemID: "item-1", Title: "测试商品", ImageURL: "https://img.example/item.png", Price: "10"}}, Page: 1}, port.listErr
}

// SendItemCard 返回预设本地消息和应用错误。
func (port *chatItemHandlerPort) SendItemCard(context.Context, chatapp.ItemCardInput) (*chatapp.Message, error) {
	if port.outgoing != nil || port.sendErr != nil {
		return port.outgoing, port.sendErr
	}
	return &chatapp.Message{AccountID: "acc1", ChatID: "chat", MessageKey: "item-message", Direction: "outgoing", MessageType: "item", Content: `{"item_id":"item-1","title":"测试商品","image_url":"https://img.example/item.png","price":"10"}`, Status: "sent"}, nil
}

// TestChatItemHandlersMapUnifiedErrors 验证商品查询和发送端点的参数、归属、离线及状态错误映射。
func TestChatItemHandlersMapUnifiedErrors(t *testing.T) {
	// server、_ 和 cleanup 分别是独立 HTTP 服务、无需读取的存储及资源释放函数。
	server, _, cleanup := newTestServer(t)
	defer cleanup()
	// handler 是挂载真实认证和商品路由的 HTTP Router。
	handler := server.Router()
	// sessionCookie 是访问商品端点的管理员认证 Cookie。
	sessionCookie := loginHelper(t, handler)
	// validItemBody 是不包含 buyer_id 的合法商品发送请求。
	validItemBody := `{"account_id":"acc1","chat_id":"chat","item":{"item_id":"item-1","title":"测试商品","image_url":"https://img.example/item.png","price":"10"}}`
	// failedMessage 是平台失败后应随 502 返回的本地 failed 商品消息。
	failedMessage := &chatapp.Message{AccountID: "acc1", ChatID: "chat", MessageKey: "failed-item", Direction: "outgoing", MessageType: "item", Content: `{"item_id":"item-1","title":"测试商品","image_url":"https://img.example/item.png","price":"10"}`, Status: "failed"}
	// cases 是商品端点必须映射为稳定状态码和统一错误代码的场景。
	cases := []struct {
		// name 是当前错误分支名称。
		name string
		// method 和 path 是当前 HTTP 请求方法与地址。
		method, path string
		// body 是可选 JSON 请求正文。
		body string
		// port 是当前分支注入的商品应用端口。
		port *chatItemHandlerPort
		// wantStatus 和 wantCode 是预期 HTTP 状态与统一错误代码。
		wantStatus int
		wantCode   string
	}{
		{name: "invalid list query", method: http.MethodGet, path: "/api/v1/chat/items?account_id=acc1&chat_id=chat&role=invalid", port: &chatItemHandlerPort{}, wantStatus: http.StatusBadRequest, wantCode: "chat_item_invalid"},
		{name: "forbidden list", method: http.MethodGet, path: "/api/v1/chat/items?account_id=acc1&chat_id=chat&role=peer", port: &chatItemHandlerPort{listErr: chatapp.ErrChatItemForbidden}, wantStatus: http.StatusForbidden, wantCode: "chat_item_forbidden"},
		{name: "missing session", method: http.MethodGet, path: "/api/v1/chat/items?account_id=acc1&chat_id=chat&role=peer", port: &chatItemHandlerPort{listErr: chatapp.ErrChatSessionNotFound}, wantStatus: http.StatusNotFound, wantCode: "chat_session_not_found"},
		{name: "catalog unavailable", method: http.MethodGet, path: "/api/v1/chat/items?account_id=acc1&chat_id=chat&role=peer", port: &chatItemHandlerPort{catalogUnavailable: true}, wantStatus: http.StatusServiceUnavailable, wantCode: "chat_item_catalog_unavailable"},
		{name: "invalid send json", method: http.MethodPost, path: "/api/v1/chat/item-cards", body: "{", port: &chatItemHandlerPort{}, wantStatus: http.StatusBadRequest, wantCode: "chat_item_invalid"},
		{name: "offline send", method: http.MethodPost, path: "/api/v1/chat/item-cards", body: validItemBody, port: &chatItemHandlerPort{sendErr: chatapp.ErrOffline}, wantStatus: http.StatusConflict, wantCode: "chat_account_offline"},
		{name: "sender unavailable", method: http.MethodPost, path: "/api/v1/chat/item-cards", body: validItemBody, port: &chatItemHandlerPort{sendingUnavailable: true}, wantStatus: http.StatusServiceUnavailable, wantCode: "chat_item_sending_unavailable"},
		{name: "uncertain send", method: http.MethodPost, path: "/api/v1/chat/item-cards", body: validItemBody, port: &chatItemHandlerPort{sendErr: chatapp.ErrSendUncertain, outgoing: failedMessage}, wantStatus: http.StatusBadGateway, wantCode: "chat_item_card_send_uncertain"},
		{name: "platform send failed", method: http.MethodPost, path: "/api/v1/chat/item-cards", body: validItemBody, port: &chatItemHandlerPort{sendErr: chatapp.ErrSend, outgoing: failedMessage}, wantStatus: http.StatusBadGateway, wantCode: "chat_item_card_send_failed"},
		{name: "unexpected send failed", method: http.MethodPost, path: "/api/v1/chat/item-cards", body: validItemBody, port: &chatItemHandlerPort{sendErr: errors.New("unexpected send error")}, wantStatus: http.StatusBadGateway, wantCode: "chat_item_card_send_failed"},
		{name: "pending save failed", method: http.MethodPost, path: "/api/v1/chat/item-cards", body: validItemBody, port: &chatItemHandlerPort{sendErr: chatapp.ErrChatItemCreate}, wantStatus: http.StatusInternalServerError, wantCode: "chat_item_message_save_failed"},
		{name: "status save failed", method: http.MethodPost, path: "/api/v1/chat/item-cards", body: validItemBody, port: &chatItemHandlerPort{sendErr: chatapp.ErrStatusSave, outgoing: failedMessage}, wantStatus: http.StatusInternalServerError, wantCode: "chat_send_status_save_failed"},
	}
	for _ /* testCase 是当前待执行的商品 HTTP 错误场景。 */, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server.applications.chat = testCase.port
			// request 是带认证 Cookie 的当前商品 API 请求。
			request := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(testCase.body))
			if testCase.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			request.AddCookie(sessionCookie)
			// recorder 保存真实 handler 返回的状态与错误信封。
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			// payload 是当前统一错误信封的最小解码结果。
			var payload struct {
				// Code 是稳定机器错误代码。
				Code string `json:"code"`
				// Details 保存平台发送失败时附带的本地消息。
				Details map[string]any `json:"details"`
			}
			// decodeErr 表示统一错误响应是否能按最小测试结构解析。
			if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &payload); decodeErr != nil || payload.Code != testCase.wantCode {
				t.Fatalf("payload=%+v err=%v body=%s", payload, decodeErr, recorder.Body.String())
			}
			if testCase.port.outgoing != nil && payload.Details["outgoing_message"] == nil {
				t.Fatalf("平台发送失败缺少 outgoing_message: %s", recorder.Body.String())
			}
			if testCase.port.outgoing != nil {
				assertOutgoingErrorDTO(t, recorder, "acc1", "chat")
			}
			assertOpenAPIResponse(t, request, recorder)
		})
	}
}

// TestChatItemHandlersRequireAuthentication 验证两个商品端点都由版本化认证中间件保护。
func TestChatItemHandlersRequireAuthentication(t *testing.T) {
	// server、_ 和 cleanup 分别是独立服务、无需读取的存储及资源释放函数。
	server, _, cleanup := newTestServer(t)
	defer cleanup()
	server.applications.chat = &chatItemHandlerPort{}
	// handler 是不附带登录 Cookie 时使用的真实 Router。
	handler := server.Router()
	// requests 是商品查询与发送的未认证请求。
	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/chat/items?account_id=acc1&chat_id=chat&role=peer", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/chat/item-cards", strings.NewReader(`{"account_id":"acc1"}`)),
	}
	for _ /* request 是当前待验证的未认证商品请求。 */, request := range requests {
		// recorder 保存认证中间件的实际响应。
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d body=%s", request.Method, request.URL.Path, recorder.Code, recorder.Body.String())
		}
	}
}
