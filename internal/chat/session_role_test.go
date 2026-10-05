package chat

import (
	"context"
	"testing"

	"xianyu-go/internal/db"
)

// TestSessionCreationUsesLocalItemRole 验证实时消息和联系人创建会话时会用本地商品确定买卖双方且不访问平台。
func TestSessionCreationUsesLocalItemRole(t *testing.T) {
	// store 和 cleanup 提供迁移后的隔离 SQLite 数据库及关闭责任。
	store, cleanup := chatTestStore(t)
	defer cleanup()
	// ctx 控制当前测试的纯本地仓储操作。
	ctx := context.Background()
	// itemErr 保存账号本地商品夹具写入结果。
	if itemErr := store.Items.Upsert(ctx, &db.ItemInfoRow{CookieID: "account-1", ItemID: "owned-item", ItemTitle: "本地商品"}); itemErr != nil {
		t.Fatal(itemErr)
	}
	// service 是使用生产仓储适配器的聊天领域服务。
	service := New(store)
	// stored、inserted 和 recordErr 是实时消息落库及去重结果。
	stored, inserted, recordErr := service.RecordIncoming(ctx, Incoming{
		AccountID: "account-1", AccountUserID: "seller-self", ChatID: "live-chat", BuyerID: "buyer-peer",
		BuyerName: "对方", ItemID: "owned-item", MessageID: "live-message", Text: "你好", ObservedAt: 1000,
	})
	if recordErr != nil || !inserted || stored == nil {
		t.Fatalf("record incoming message=%+v inserted=%v err=%v", stored, inserted, recordErr)
	}
	// liveRole 和 liveRoleErr 验证实时会话在首次创建时已经固定当前账号为卖家。
	liveRole, liveRoleErr := store.Chats.SessionRole(ctx, "account-1", "live-chat", "owned-item")
	if liveRoleErr != nil || liveRole.AccountRole != "seller" || liveRole.BuyerUserID != "buyer-peer" || liveRole.SellerUserID != "seller-self" || liveRole.RoleSource != "local_item" {
		t.Fatalf("live role=%+v err=%v", liveRole, liveRoleErr)
	}
	// body 是联系人接口返回的本地合成会话页，商品同样已在当前账号商品表中。
	body := map[string]any{"userConvs": []any{map[string]any{"singleChatUserConversation": map[string]any{
		"visible": float64(1), "modifyTime": float64(2000),
		"singleChatConversation": map[string]any{"cid": "contact-chat@goofish", "pairFirst": "seller-self@goofish", "pairSecond": "contact-peer@goofish", "extension": map[string]any{"itemId": "owned-item"}},
		"lastMessage":            map[string]any{"message": map[string]any{"createAt": float64(2000), "content": map[string]any{"custom": map[string]any{"summary": "联系人消息"}}}},
	}}}}
	// page 和 pageErr 保存联系人页解析结果。
	page, pageErr := service.RecordConversationPage(ctx, "account-1", "seller-self", body)
	if pageErr != nil || page.HasMore {
		t.Fatalf("record conversations page=%+v err=%v", page, pageErr)
	}
	// contactRole 和 contactRoleErr 验证联系人会话在创建阶段已经固定双方身份。
	contactRole, contactRoleErr := store.Chats.SessionRole(ctx, "account-1", "contact-chat", "owned-item")
	if contactRoleErr != nil || contactRole.AccountRole != "seller" || contactRole.BuyerUserID != "contact-peer" || contactRole.SellerUserID != "seller-self" || contactRole.RoleSource != "local_item" {
		t.Fatalf("contact role=%+v err=%v", contactRole, contactRoleErr)
	}
}
