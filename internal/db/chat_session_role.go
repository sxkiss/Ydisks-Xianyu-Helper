package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SessionRole 返回会话与指定商品绑定的本地角色结论；商品不一致时返回 unknown。
func (s *ChatStore) SessionRole(ctx context.Context, cookieID, chatID, itemID string) (ChatSession, error) {
	// session 保存角色查询返回的非敏感参与方标识和证据来源。
	var session ChatSession
	// err 保存账号、会话和商品共同限定的角色查询结果。
	err := s.DB.QueryRowContext(ctx, `SELECT cookie_id,chat_id,buyer_id,buyer_name,buyer_avatar_url,item_id,
		account_role,buyer_user_id,seller_user_id,role_item_id,role_source
		FROM chat_sessions WHERE cookie_id=? AND chat_id=? AND role_item_id=?`, cookieID, chatID, itemID).Scan(
		&session.CookieID, &session.ChatID, &session.BuyerID, &session.BuyerName, &session.BuyerAvatar, &session.ItemID,
		&session.AccountRole, &session.BuyerUserID, &session.SellerUserID, &session.RoleItemID, &session.RoleSource)
	if errors.Is(err, sql.ErrNoRows) {
		return ChatSession{CookieID: cookieID, ChatID: chatID, ItemID: itemID, AccountRole: "unknown"}, nil
	}
	return session, err
}

// UpdateSessionRole 持久化会话与商品绑定的买卖双方结论，不创建不存在的会话。
func (s *ChatStore) UpdateSessionRole(ctx context.Context, cookieID, chatID, itemID, accountRole, buyerUserID, sellerUserID, roleSource string) error {
	// result、err 保存带商品条件的角色更新结果，避免旧消息覆盖已经切换商品的会话。
	result, err := s.DB.ExecContext(ctx, `UPDATE chat_sessions SET account_role=?,buyer_user_id=?,seller_user_id=?,
		role_item_id=?,role_source=?,updated_at=? WHERE cookie_id=? AND chat_id=? AND item_id=?`,
		accountRole, buyerUserID, sellerUserID, itemID, roleSource, time.Now().UTC().Unix(), cookieID, chatID, itemID)
	if err != nil {
		return err
	}
	// affected 是满足当前商品快照的会话数量；零行表示会话已切换，不覆盖新角色。
	affected, affectedErr := result.RowsAffected()
	if affectedErr != nil {
		return affectedErr
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}
