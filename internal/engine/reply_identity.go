package engine

import (
	"context"
	"strings"

	"xianyu-go/internal/db"
	"xianyu-go/internal/xianyu/protocol"
)

// replyLocalItemOwnership 是生产适配器提供的本地商品归属核验能力。
// 本地商品表是当前账号为卖家的确定证据；自动回复不再使用平台商品详情推断角色。
type replyLocalItemOwnership interface {
	// ItemBelongsToAccount 判断商品是否仍属于当前账号的有效本地商品集合；不读取账号凭证明文。
	ItemBelongsToAccount(ctx context.Context, accountID, itemID string) (bool, error)
}

// replySessionRoleStore 是消息处理器读取和保存会话级买卖角色所需的最小本地能力。
type replySessionRoleStore interface {
	// ChatSessionRole 读取账号、会话和商品共同绑定的角色结论。
	ChatSessionRole(ctx context.Context, accountID, chatID, itemID string) (db.ChatSession, error)
	// SaveChatSessionRole 保存首次本地商品归属结果，商品发生变化时不得覆盖新会话状态。
	SaveChatSessionRole(ctx context.Context, accountID, chatID, itemID, accountRole, buyerUserID, sellerUserID, roleSource string) error
}

// canAutoReply 核验 message 的会话商品是否仍属于当前账号的本地商品库；ctx 来自账号生命周期。
// 本地查询失败、商品缺失、缺少身份或查询期间账号切换均禁止自动回复，聊天消息仍正常观察和保存。
func (d *messageDispatcher) canAutoReply(ctx context.Context, message ChatMessage) bool {
	if strings.TrimSpace(message.ItemID) == "" {
		return false
	}
	// handler 是已经完成消息落库的业务处理器；缺少角色仓储时采用拒绝回复的保守语义。
	handler := d.currentHandler()
	// roleStore、supported 保存会话角色仓储及能力标识。
	roleStore, supported := handler.(replySessionRoleStore)
	if !supported {
		return false
	}
	// selfID 是凭证快照中的非敏感平台用户标识。
	selfID := protocol.TransCookies(d.currentCookie())["unb"]
	if strings.TrimSpace(selfID) == "" {
		return false
	}
	// session、roleErr 是与当前商品绑定的本地角色结论及查询错误。
	session, roleErr := roleStore.ChatSessionRole(ctx, message.AccountID, message.ChatID, message.ItemID)
	if roleErr != nil {
		d.logger.Warn("读取聊天会话角色失败，跳过自动回复", "chat_id", message.ChatID)
		return false
	}
	// localAllowed、localHandled 表示本地商品归属是否已经给出确定的自动回复结论；任何角色都必须先通过本地商品库门禁。
	localAllowed, localHandled := d.resolveLocalItemOwnership(ctx, message, session, roleStore, selfID)
	if !localHandled || !localAllowed {
		return false
	}
	if session.AccountRole == "seller" {
		return isSelfUserID(session.SellerUserID, selfID)
	}
	if session.AccountRole == "buyer" {
		return false
	}
	// 未知角色经本地商品归属写入 seller 后，允许当前账号继续执行自动回复。
	return isSelfUserID(selfID, protocol.TransCookies(d.currentCookie())["unb"])
}

// resolveLocalItemOwnership 使用当前账号的本地商品集合恢复未知会话的卖家角色。
// ctx 控制本地查询和角色写入；message、session、roleStore、selfID 分别提供会话身份、已有角色快照、持久化端口和当前账号标识。
// 返回值中的 handled 表示实现是否提供本地归属能力；缺少该能力时也必须拒绝自动回复，不能猜测身份。
func (d *messageDispatcher) resolveLocalItemOwnership(ctx context.Context, message ChatMessage, session db.ChatSession, roleStore replySessionRoleStore, selfID string) (allowed, handled bool) {
	// handler 是当前账号的业务适配器；缺少本地归属扩展时保持自动回复关闭。
	handler := d.currentHandler()
	// owner、supported 保存本地商品归属端口及其是否由当前适配器提供。
	owner, supported := handler.(replyLocalItemOwnership)
	if !supported {
		return false, false
	}
	// owned、ownershipErr 保存本地商品集合的归属结论；查询失败不能猜测卖家身份。
	owned, ownershipErr := owner.ItemBelongsToAccount(ctx, message.AccountID, message.ItemID)
	if ownershipErr != nil {
		d.logger.Warn("读取本地商品归属失败，跳过自动回复", "chat_id", message.ChatID)
		return false, true
	}
	if !owned {
		if session.AccountRole == "seller" || session.AccountRole == "buyer" {
			return false, true
		}
		if session.RoleSource == "platform_unresolved" {
			return false, true
		}
		// claimErr 固定当前商品暂未能以本地事实确认；后续商品同步成功后仍可在此分支恢复卖家角色。
		claimErr := roleStore.SaveChatSessionRole(ctx, message.AccountID, message.ChatID, message.ItemID, "unknown", "", "", "platform_unresolved")
		if claimErr != nil {
			d.logger.Warn("保存本地商品归属核验标记失败，跳过自动回复", "chat_id", message.ChatID)
			return false, true
		}
		d.logger.Warn("商品不在当前账号本地商品清单，跳过自动回复", "chat_id", message.ChatID)
		return false, true
	}
	// currentSelfID 复核本地查询期间的账号身份，防止凭证切换后把旧账号商品写入当前会话。
	currentSelfID := protocol.TransCookies(d.currentCookie())["unb"]
	if !isSelfUserID(selfID, currentSelfID) {
		return false, true
	}
	if session.AccountRole == "seller" || session.AccountRole == "buyer" {
		return true, true
	}
	// saveErr 保存本地商品事实对应的卖家角色；该证据不依赖任何平台商品详情响应。
	saveErr := roleStore.SaveChatSessionRole(ctx, message.AccountID, message.ChatID, message.ItemID, "seller", message.SenderUserID, selfID, "local_item")
	if saveErr != nil {
		d.logger.Warn("保存本地商品卖家身份失败，跳过自动回复", "chat_id", message.ChatID)
		return false, true
	}
	return true, true
}
