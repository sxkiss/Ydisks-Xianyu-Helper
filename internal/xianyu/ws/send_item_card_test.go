package ws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// TestSendItemCardUsesOfficialNestedEnvelope 验证商品卡片复用 101 外层信封并生成官网 contentType=7 正文。
func TestSendItemCardUsesOfficialNestedEnvelope(t *testing.T) {
	// server 和 received 分别是本地 WebSocket 服务及客户端消息收集通道。
	server, received := startRegServer(t)
	// connection 是直接连接本地服务的聊天 WebSocket。
	connection := dialLocal(t, server, Config{})
	// sendContext 和 cancel 限制商品卡片发送的本地等待时间。
	sendContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// sendErr 表示商品卡片是否已写入 WebSocket。
	if sendErr := connection.SendItemCard(sendContext, "100", "chat-1", "200", "item-1", "测试商品", "https://img.example/item.png", "¥19.90"); sendErr != nil {
		t.Fatalf("发送商品卡片失败: %v", sendErr)
	}
	select {
	case /* envelope 是服务端收到的完整发送信封。 */ envelope := <-received:
		if envelope["lwp"] != "/r/MessageSend/sendByReceiverScope" {
			t.Fatalf("lwp=%v", envelope["lwp"])
		}
		// body 是 sendByReceiverScope 的消息和接收者参数。
		body, _ := envelope["body"].([]any)
		if len(body) != 2 {
			t.Fatalf("body=%v", body)
		}
		// messageBody 是包含外层 contentType=101 的首个参数。
		messageBody, _ := body[0].(map[string]any)
		if messageBody["cid"] != "chat-1@goofish" {
			t.Fatalf("cid=%v", messageBody["cid"])
		}
		// outerContent 是现有聊天协议的 101 外层正文。
		outerContent, _ := messageBody["content"].(map[string]any)
		if outerContent["contentType"] != float64(101) {
			t.Fatalf("outer contentType=%v", outerContent["contentType"])
		}
		// custom 是承载 base64 商品正文的外层自定义对象。
		custom, _ := outerContent["custom"].(map[string]any)
		// encoded 是外层自定义对象中的 base64 文本。
		encoded, _ := custom["data"].(string)
		// decoded 和 decodeErr 是解码后的 contentType=7 JSON 及错误。
		decoded, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if decodeErr != nil {
			t.Fatalf("商品卡片正文不是 base64: %v", decodeErr)
		}
		// inner 是官网个人会话商品卡片的内层正文。
		var inner map[string]any
		// jsonErr 表示 base64 解码结果是否为合法商品 JSON。
		if jsonErr := json.Unmarshal(decoded, &inner); jsonErr != nil {
			t.Fatalf("解析商品卡片正文失败: %v", jsonErr)
		}
		if inner["contentType"] != float64(7) {
			t.Fatalf("inner contentType=%v", inner["contentType"])
		}
		// itemCard 是包含固定提示和商品对象的卡片载荷。
		itemCard, _ := inner["itemCard"].(map[string]any)
		// item 是商品卡片中的平台字段映射。
		item, _ := itemCard["item"].(map[string]any)
		if itemCard["itemTip"] != "我想要" || item["itemId"] != "item-1" || item["mainPic"] != "https://img.example/item.png" || item["price"] != "¥19.90" || item["title"] != "测试商品" {
			t.Fatalf("itemCard=%v", itemCard)
		}
		// receivers 是 sendByReceiverScope 第二个参数中的实际接收者集合。
		receivers, _ := body[1].(map[string]any)
		if receivers["actualReceivers"] == nil {
			t.Fatalf("receivers=%v", receivers)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到商品卡片 WebSocket 信封")
	}
}

// TestSendItemCardRejectsMissingFields 验证缺失商品字段不会写入 WebSocket。
func TestSendItemCardRejectsMissingFields(t *testing.T) {
	// connection 是仅用于参数前置校验的空连接对象。
	connection := &Conn{}
	// sendErr 是缺少商品标识时预期返回的参数错误。
	if sendErr := connection.SendItemCard(context.Background(), "100", "chat-1", "200", "", "标题", "https://img.example/item.png", "1"); sendErr == nil {
		t.Fatal("缺少商品标识时应拒绝发送")
	}
}
