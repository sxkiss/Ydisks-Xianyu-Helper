package db

import (
	"context"
	"testing"
)

// TestMultiDBChatSessionRolePersistence 验证三方言升级旧会话、固定角色、切换商品失效及订单匹配边界。
func TestMultiDBChatSessionRolePersistence(t *testing.T) {
	// target 是当前可用的隔离数据库目标；未配置的外部方言由公共夹具明确跳过。
	for _, target := range allTestTargets(t) {
		t.Run(target.name, func(t *testing.T) {
			defer target.cleanup()
			// ctx 控制本测试全部本地数据库操作，不包含任何平台访问。
			ctx := context.Background()
			// roleColumns 是迁移 00047 必须在旧 chat_sessions 表上追加的非敏感角色字段。
			roleColumns := []string{"account_role", "buyer_user_id", "seller_user_id", "role_item_id", "role_source"}
			// column 是当前检查的角色字段名。
			for _, column := range roleColumns {
				if !columnExistsForDialect(t, target.store.DB, target.dialect, "chat_sessions", column) {
					t.Fatalf("migration 47 column %s is missing", column)
				}
			}
			// userID 和 accountID 建立满足外键的测试管理用户及空凭证账号。
			_, accountID := seedAccount(t, target.store)
			// legacySession 模拟升级前已经存在、尚无角色结论的会话。
			legacySession := ChatSession{CookieID: accountID, ChatID: "chat-role", BuyerID: "peer", ItemID: "item-old"}
			// writeErr 保存旧会话写入失败。
			if writeErr := target.store.Chats.UpsertSession(ctx, legacySession); writeErr != nil {
				t.Fatal(writeErr)
			}
			// unknown 和 unknownErr 验证旧会话安全升级为 unknown，不能猜测买卖角色。
			unknown, unknownErr := target.store.Chats.SessionRole(ctx, accountID, "chat-role", "item-old")
			if unknownErr != nil || unknown.AccountRole != "unknown" {
				t.Fatalf("legacy role=%+v err=%v", unknown, unknownErr)
			}
			// sellerErr 保存本地商品证据对应的卖家角色写入结果。
			if sellerErr := target.store.Chats.UpdateSessionRole(ctx, accountID, "chat-role", "item-old", "seller", "peer", "self", "local_item"); sellerErr != nil {
				t.Fatal(sellerErr)
			}
			// seller 和 sellerReadErr 验证完整买卖双方与证据绑定商品均可读取。
			seller, sellerReadErr := target.store.Chats.SessionRole(ctx, accountID, "chat-role", "item-old")
			if sellerReadErr != nil || seller.AccountRole != "seller" || seller.BuyerUserID != "peer" || seller.SellerUserID != "self" || seller.RoleSource != "local_item" {
				t.Fatalf("seller role=%+v err=%v", seller, sellerReadErr)
			}
			// sellerMatches 和 sellerMatchErr 验证已确认卖家会话使用 buyer_user_id 关联售出订单。
			sellerMatches, sellerMatchErr := target.store.Chats.FindChatIDsByBuyerAndItem(ctx, accountID, "peer", "item-old")
			if sellerMatchErr != nil || len(sellerMatches) != 1 || sellerMatches[0] != "chat-role" {
				t.Fatalf("seller order matches=%v err=%v", sellerMatches, sellerMatchErr)
			}
			// switchErr 把会话切换到另一商品，旧商品角色必须立即失效。
			if switchErr := target.store.Chats.UpsertSession(ctx, ChatSession{CookieID: accountID, ChatID: "chat-role", BuyerID: "peer", ItemID: "item-new"}); switchErr != nil {
				t.Fatal(switchErr)
			}
			// switched 和 switchedErr 验证新商品不会继承旧商品的卖家身份。
			switched, switchedErr := target.store.Chats.SessionRole(ctx, accountID, "chat-role", "item-new")
			if switchedErr != nil || switched.AccountRole != "unknown" {
				t.Fatalf("switched role=%+v err=%v", switched, switchedErr)
			}
			// buyerErr 保存当前账号作为买家的明确角色，用于验证售出订单不会误匹配对端卖家。
			if buyerErr := target.store.Chats.UpdateSessionRole(ctx, accountID, "chat-role", "item-new", "buyer", "self", "peer", "platform_verified"); buyerErr != nil {
				t.Fatal(buyerErr)
			}
			// buyerMatches 和 buyerMatchErr 验证买家侧会话不参与卖家订单的自动关联。
			buyerMatches, buyerMatchErr := target.store.Chats.FindChatIDsByBuyerAndItem(ctx, accountID, "peer", "item-new")
			if buyerMatchErr != nil || len(buyerMatches) != 0 {
				t.Fatalf("buyer-side conversation matched sold order: ids=%v err=%v", buyerMatches, buyerMatchErr)
			}
		})
	}
}
