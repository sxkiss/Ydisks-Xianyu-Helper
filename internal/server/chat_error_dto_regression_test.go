package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// assertOutgoingErrorDTO 验证 recorder 中的错误详情使用前端 snake_case 契约；accountID/chatID 是预期消息归属。
func assertOutgoingErrorDTO(t *testing.T, recorder *httptest.ResponseRecorder, accountID, chatID string) {
	t.Helper()
	// payload 只解析错误信封内的非敏感消息投影。
	var payload struct {
		// Details 按错误契约保存出站消息。
		Details struct {
			// Outgoing 保存待核对的消息字段，拒绝应用模型直接序列化的 PascalCase 名称。
			Outgoing map[string]any `json:"outgoing_message"`
		} `json:"details"`
	}
	// err 保存错误响应 JSON 解码失败原因。
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Details.Outgoing["account_id"] != accountID || payload.Details.Outgoing["chat_id"] != chatID {
		t.Fatal("错误响应未使用 transport 消息归属字段")
	}
	// field 是前端运行时解析待确认消息所需的公共字段。
	for _, field := range []string{"id", "message_key", "direction", "sender_id", "sender_name", "message_type", "content", "status", "sent_at"} {
		// present 标识必需字段是否真实存在，不接受只有应用层大写字段的响应。
		if _, present := payload.Details.Outgoing[field]; !present {
			t.Fatalf("错误消息缺少前端字段 %s", field)
		}
	}
	// present 检查不应对外出现的应用层字段。
	if _, present := payload.Details.Outgoing["AccountID"]; present {
		t.Fatal("错误消息泄露了应用模型字段")
	}
}
