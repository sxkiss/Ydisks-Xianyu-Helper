package engine

import (
	"context"
	"testing"
)

// TestReplyImageURLIsPassedToCompleteDelivery 验证引擎只传递图片 URL，不再自行读取或拼装像素尺寸。
func TestReplyImageURLIsPassedToCompleteDelivery(t *testing.T) {
	// store、cleanup 保存图片关键词回复使用的隔离数据库。
	store, cleanup := newReplyStore(t)
	defer cleanup()
	// setupErr 保存图片关键词规则的配置写入结果。
	_, setupErr := store.DB.ExecContext(context.Background(), `INSERT INTO keywords (cookie_id,keyword,reply,image_url,type) VALUES ('cid','照片','','https://origin.example/photo.png','image')`)
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	// delivery 记录完整消息端口收到的原始图片地址。
	delivery := &recordingReplyDelivery{result: ReplySendResult{ImageSent: true}}
	// reply 使用生产完整消息构造路径。
	reply := NewReplyService("cid", store, delivery, nil, nil, nil)
	// sendErr 保存完整消息端口处理图片回复的结果。
	sendErr := reply.Handle(context.Background(), chatMsg("给我照片", "", "chat-image"))
	if sendErr != nil {
		t.Fatalf("Handle: %v", sendErr)
	}
	if len(delivery.messages) != 1 || delivery.messages[0].ImageURL != "https://origin.example/photo.png" || delivery.messages[0].Text != "" {
		t.Fatalf("完整消息内容错误=%+v", delivery.messages)
	}
}

// TestReplyWithoutDeliveryDoesNotInvokeProtocolSender 验证未装配聊天应用端口时引擎不会旁路调用 WebSocket。
func TestReplyWithoutDeliveryDoesNotInvokeProtocolSender(t *testing.T) {
	// store、cleanup 保存默认回复规则使用的隔离数据库。
	store, cleanup := newReplyStore(t)
	defer cleanup()
	// setupErr 保存默认回复规则的配置写入结果。
	_, setupErr := store.DB.ExecContext(context.Background(), `INSERT INTO default_replies (cookie_id,enabled,reply_content) VALUES ('cid',1,'不应发送')`)
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	// reply 没有完整消息端口，只能生成回复而不能执行发送副作用。
	reply := NewReplyService("cid", store, nil, nil, nil, nil)
	// sendErr 保存缺少完整消息端口时的处理结果。
	sendErr := reply.Handle(context.Background(), chatMsg("你好", "", "chat-no-delivery"))
	if sendErr != nil {
		t.Fatalf("缺少发送端口不应返回错误: %v", sendErr)
	}
}
