package chat

import (
	"context"
	"errors"
	"strings"
	"time"

	"xianyu-go/internal/db"
)

// RecordOutgoingSent 记录已被平台接受的自动化、本程序或官方客户端出站消息。
// ctx 控制持久化生命周期；session 提供非敏感会话摘要；key 可能为本地待发送键或平台 PNM 键；text 是用户可见正文；返回已保存消息或持久化错误。
func (s *Service) RecordOutgoingSent(ctx context.Context, session db.ChatSession, key, text string) (*db.ChatMessage, error) {
	return s.RecordOutgoingMessageSent(ctx, session, key, "text", text)
}

// RecordOutgoingMessageSent 记录已被平台接受的指定类型出站消息。
// messageType 和 content 只允许已由协议解析器归一化的非敏感展示值；observedValues 可携带平台事件被本机接纳的毫秒时间，省略时兼容旧调用方并使用当前时间。
func (s *Service) RecordOutgoingMessageSent(ctx context.Context, session db.ChatSession, key, messageType, content string, observedValues ...int64) (*db.ChatMessage, error) {
	// observedAt 优先使用引擎在平台确认或回显接纳时记录的时间，兼容旧调用方时在入口补齐。
	observedAt := time.Now().UTC().UnixMilli()
	if len(observedValues) > 0 && observedValues[0] > 0 {
		observedAt = observedValues[0]
	}
	// normalizedType 和 normalizedContent 是准备持久化的消息类型与正文。
	normalizedType, normalizedContent := strings.TrimSpace(messageType), strings.TrimSpace(content)
	if normalizedType == "" {
		normalizedType = "text"
	}
	// normalizedKey 保存去除空白后的幂等键；本程序发送使用本地键，官方客户端回显使用平台 PNM 键。
	normalizedKey := strings.TrimSpace(key)
	if normalizedKey != "" {
		// existing、updateErr 保存本地待发送记录的状态更新结果；找不到本地键时才把官方回显作为新的权威出站消息写入。
		existing, updateErr := s.SetOutgoingStatus(ctx, session.CookieID, normalizedKey, "sent")
		if updateErr == nil {
			return existing, nil
		}
		if !errors.Is(updateErr, db.ErrNotFound) {
			return nil, updateErr
		}
	}
	// messageKey 保存新出站记录的稳定键；官方回显优先保留平台键以便重复回显幂等，自动化消息仍使用本地生成键。
	messageKey := normalizedKey
	if messageKey == "" {
		messageKey = "sent-" + randomID()
	}
	// message 保存已被平台接受、可立即展示的出站消息；它不增加会话未读数。
	message := db.ChatMessage{MessageKey: messageKey, Direction: "outgoing", SenderID: session.CookieID,
		SenderName: "我", MessageType: normalizedType, Content: normalizedContent, Status: "sent",
		SentAt: time.Now().UTC().UnixMilli(), ObservedAt: observedAt}
	// stored、saveErr 保存幂等写入的出站消息及其错误；只有首次写入才广播创建事件。
	stored, inserted, saveErr := s.repository.SaveMessage(ctx, session, message, false)
	if saveErr == nil && inserted {
		s.PublishContext(ctx, session.CookieID, Event{Type: "message.created", Message: stored, Session: &session})
	}
	return stored, saveErr
}
