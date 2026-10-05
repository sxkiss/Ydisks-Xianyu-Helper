package adapter

import (
	"context"
	"strings"

	"xianyu-go/internal/db"
	"xianyu-go/internal/engine"
)

// HandleOutgoingChatMessage 将成功的人工、自动化或跨端出站消息作为旁路观察落库；本方法不参与平台投递。
func (a *Adapter) HandleOutgoingChatMessage(ctx context.Context, message engine.OutgoingChatMessage) error {
	if a.chat == nil {
		return nil
	}
	// messageType 和 content 将新的富消息观察与历史文本字段兼容转换。
	messageType, content := strings.TrimSpace(message.MessageType), strings.TrimSpace(message.Content)
	if messageType == "" {
		messageType = "text"
	}
	if content == "" {
		content = message.Text
	}
	// err 是出站消息状态收口或幂等新增错误。
	_, err := a.chat.RecordOutgoingMessageSent(ctx, db.ChatSession{CookieID: message.AccountID, ChatID: message.ChatID,
		BuyerID: message.BuyerID}, message.MessageKey, messageType, content, message.ObservedAt)
	return err
}
