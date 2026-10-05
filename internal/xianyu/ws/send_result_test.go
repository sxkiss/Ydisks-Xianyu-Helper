package ws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

// TestSendTextRequiresExactSuccessCode 验证聊天发送只有严格的 200 回执能够确认成功，且每种结果只发送一次业务帧。
func TestSendTextRequiresExactSuccessCode(t *testing.T) {
	// cases 保存平台回执状态与预期安全分类。
	cases := []struct {
		// name 是当前回执场景名称。
		name string
		// code 是本地平台替身返回的状态码。
		code int
		// want 是调用方应收到的发送结果分类；空值表示明确成功。
		want SendErrorKind
	}{
		{name: "success", code: http.StatusOK},
		{name: "other_2xx", code: http.StatusCreated, want: SendUncertain},
		{name: "redirect", code: http.StatusFound, want: SendUncertain},
		{name: "rejected", code: http.StatusBadRequest, want: SendRejected},
		{name: "request_timeout", code: http.StatusRequestTimeout, want: SendUncertain},
		{name: "last_4xx", code: 499, want: SendRejected},
		{name: "server_error", code: http.StatusInternalServerError, want: SendUncertain},
	}
	// testCase 是当前执行的聊天发送回执场景。
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// connection 和 requests 保存隔离的本地 WebSocket 连接及其业务帧记录。
			connection, requests := newAPIResponseConn(t, nil, testCase.code)
			// sendErr 保存一次文字发送得到的平台确认分类。
			sendErr := connection.SendText(context.Background(), "100", "chat-1", "200", "测试消息")
			// got 保存上层从发送错误中读取的安全结果分类。
			got := SendResultKind(sendErr)
			if got != testCase.want {
				t.Fatalf("code=%d result=%q, want=%q err=%v", testCase.code, got, testCase.want, sendErr)
			}
			if len(requests) != 1 {
				t.Fatalf("code=%d sent %d business frames, want 1", testCase.code, len(requests))
			}
		})
	}
}

// TestStrictChatSendResponseCodeRejectsMalformedValues 验证聊天发送确认不会截断小数或接受带符号、尾随字符的状态码。
func TestStrictChatSendResponseCodeRejectsMalformedValues(t *testing.T) {
	// cases 保存严格状态码解析的输入、结果和有效性。
	cases := []struct {
		// value 是平台响应中的原始 code 字段。
		value any
		// want 是有效输入应解析出的整数状态码。
		want int
		// valid 表示输入是否是完整整数形式。
		valid bool
	}{
		{value: 200, want: 200, valid: true},
		{value: float64(200), want: 200, valid: true},
		{value: json.Number("200"), want: 200, valid: true},
		{value: " 200 ", want: 200, valid: true},
		{value: 200.5},
		{value: json.Number("200.0")},
		{value: "200abc"},
		{value: "+200"},
		{value: nil},
	}
	// testCase 是当前执行的严格解析场景。
	for _, testCase := range cases {
		// got 和 valid 保存被测解析结果及其有效性。
		got, valid := strictChatSendResponseCode(testCase.value)
		if got != testCase.want || valid != testCase.valid {
			t.Fatalf("value=%v result=(%d,%v), want=(%d,%v)", testCase.value, got, valid, testCase.want, testCase.valid)
		}
	}
}

// TestSendTextObservesOnlyVerifiedResponseEcho 验证只有正文与发送者都匹配的成功响应才会唤醒出站观察器。
func TestSendTextObservesOnlyVerifiedResponseEcho(t *testing.T) {
	// cases 保存平台响应正文是否仍与本次文本发送完全一致的两种场景。
	cases := []struct {
		// name 是当前响应校验场景名称。
		name string
		// responseText 是平台响应中的内层文本；不同文本必须拒绝作为本次发送确认。
		responseText string
		// wantObserved 表示期望是否产生经过核验的出站观察事件。
		wantObserved bool
	}{
		{name: "matching_response", responseText: "自动发货内容", wantObserved: true},
		{name: "different_response_payload", responseText: "其他消息", wantObserved: false},
	}
	// testCase 是当前执行的发送响应校验场景。
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// responsePayload 是平台响应中 Base64 编码前的内层消息正文。
			responsePayload := map[string]any{"contentType": 1, "text": map[string]any{"text": testCase.responseText}}
			// responseRaw 和 marshalErr 保存响应正文的稳定 JSON 表示及构造错误。
			responseRaw, marshalErr := json.Marshal(responsePayload)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			// responseBody 是模拟平台 200 回执；它包含消息 ID、发送者和外层 101 消息封装。
			responseBody := map[string]any{
				"messageId": "response-echo.PNM",
				"extension": map[string]any{"senderUserId": "100"},
				"content":   map[string]any{"custom": map[string]any{"data": base64.StdEncoding.EncodeToString(responseRaw)}},
			}
			// connection 和 requests 保存本地协议连接及服务器已收到的发送帧。
			connection, requests := newAPIResponseConn(t, responseBody, http.StatusOK)
			// observed 接收连接在当前调用 goroutine 中发出的非阻塞观察事件。
			observed := make(chan OutgoingEcho, 1)
			connection.cfg.ObserveOutgoing = func(echo OutgoingEcho) {
				observed <- echo
			}
			// requestID 保存本次发送请求的 mid，验证它会随核验回显一起传递。
			requestID := "review-mid"
			// sendErr 保存文本发送的协议结果；即使响应正文不匹配，200 仍保留既有平台发送成功语义。
			sendErr := connection.SendText(WithOutgoingRequestID(context.Background(), requestID), "100", "chat-1", "200", "自动发货内容")
			if sendErr != nil {
				t.Fatal(sendErr)
			}
			if len(requests) != 1 {
				t.Fatalf("business frames=%d，期望 1", len(requests))
			}
			select {
			// echo 是响应校验通过后交给账号等待器的非敏感摘要。
			case echo := <-observed:
				if !testCase.wantObserved {
					t.Fatalf("不匹配响应不应产生观察事件：%+v", echo)
				}
				if echo.RequestID != requestID || echo.ChatID != "chat-1" || echo.BuyerID != "200" || echo.MessageKey != "response-echo.PNM" || echo.MessageType != "text" || echo.Text != "自动发货内容" || echo.Content != "" {
					t.Fatalf("观察事件=%+v", echo)
				}
			default:
				if testCase.wantObserved {
					t.Fatal("匹配响应未产生观察事件")
				}
			}
		})
	}
}

// TestOutgoingEchoContentAcceptsOnlyAutomatedPayloads 验证响应观察仅支持自动化等待器可安全比较的文本与单图正文。
func TestOutgoingEchoContentAcceptsOnlyAutomatedPayloads(t *testing.T) {
	// cases 保存不同内层消息格式及其应产生的比较字段。
	cases := []struct {
		// name 是当前正文解析场景名称。
		name string
		// payload 是已验证的内层 JSON 正文。
		payload string
		// wantType、wantText、wantContent 是成功时供等待器匹配的规范字段。
		wantType, wantText, wantContent string
		// wantOK 表示正文是否属于当前自动化确认支持的安全类型。
		wantOK bool
	}{
		{name: "text", payload: `{"contentType":1,"text":{"text":"赠品链接"}}`, wantType: "text", wantText: "赠品链接", wantOK: true},
		{name: "image", payload: `{"contentType":2,"image":{"pics":[{"url":"https://cdn.example/gift.png"}]}}`, wantType: "image", wantContent: "https://cdn.example/gift.png", wantOK: true},
		{name: "malformed", payload: `{`, wantOK: false},
		{name: "blank_text", payload: `{"contentType":1,"text":{"text":" "}}`, wantOK: false},
		{name: "empty_image_list", payload: `{"contentType":2,"image":{"pics":[]}}`, wantOK: false},
		{name: "unsupported_card", payload: `{"contentType":7}`, wantOK: false},
	}
	// testCase 是当前执行的内层正文解析场景。
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// gotType、gotText、gotContent 和 gotOK 保存正文提取结果及是否可以安全作为自动化确认。
			gotType, gotText, gotContent, gotOK := outgoingEchoContent([]byte(testCase.payload))
			if gotType != testCase.wantType || gotText != testCase.wantText || gotContent != testCase.wantContent || gotOK != testCase.wantOK {
				t.Fatalf("result=(%q,%q,%q,%v)，want=(%q,%q,%q,%v)", gotType, gotText, gotContent, gotOK, testCase.wantType, testCase.wantText, testCase.wantContent, testCase.wantOK)
			}
		})
	}
}
