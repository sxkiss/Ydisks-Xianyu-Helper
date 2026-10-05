package chat

import (
	"context"
	"errors"

	"xianyu-go/internal/db"
)

// SessionRole 读取指定会话和商品的本地角色结论；缺少能力时返回 unknown。
func (s *Service) SessionRole(ctx context.Context, accountID, chatID, itemID string) (db.ChatSession, error) {
	// repository、ok 是生产环境提供的会话角色仓储及能力标识。
	repository, ok := s.repository.(sessionRoleRepository)
	if !ok {
		return db.ChatSession{CookieID: accountID, ChatID: chatID, ItemID: itemID, AccountRole: "unknown"}, nil
	}
	return repository.SessionRole(ctx, accountID, chatID, itemID)
}

// UpdateSessionRole 保存平台首次核验得到的会话角色，商品变化时仓储会拒绝旧结果覆盖。
func (s *Service) UpdateSessionRole(ctx context.Context, accountID, chatID, itemID, accountRole, buyerUserID, sellerUserID, roleSource string) error {
	// repository、ok 是生产环境提供的角色写入仓储及能力标识。
	repository, ok := s.repository.(sessionRoleRepository)
	if !ok {
		return errors.New("聊天会话角色仓储未初始化")
	}
	return repository.UpdateSessionRole(ctx, accountID, chatID, itemID, accountRole, buyerUserID, sellerUserID, roleSource)
}
