package db

import (
	"context"
	"testing"
)

// TestItemsListForUserUsesOwnershipJoin 验证商品批量查询按用户归属过滤并支持账号筛选。
func TestItemsListForUserUsesOwnershipJoin(t *testing.T) {
	// ctx 是本测试使用的数据库上下文。
	ctx := context.Background()
	// database、dialect、err 保存临时 SQLite 数据库及打开结果。
	database, dialect, err := Open(ctx, t.TempDir()+"/items-scope.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()
	// store 是绑定最新迁移的数据库访问聚合。
	store := NewStore(database, dialect)
	// err 表示创建商品归属测试用户失败。
	if _, err := store.Users.Create(ctx, "owner", "owner@example.com", "pw"); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	// err 表示创建跨用户测试用户失败。
	if _, err := store.Users.Create(ctx, "other", "other@example.com", "pw"); err != nil {
		t.Fatalf("create other: %v", err)
	}
	// owner、other 保存两个测试用户及其 ID。
	owner, _ := store.Users.GetByUsername(ctx, "owner")
	// other 保存跨用户测试用户及其 ID。
	other, _ := store.Users.GetByUsername(ctx, "other")
	// item 表示待写入的账号与商品归属组合。
	for _, item := range []struct {
		cookieID string
		userID   int64
		itemID   string
	}{
		{cookieID: "owned-cookie", userID: owner.ID, itemID: "owned-item"},
		{cookieID: "other-cookie", userID: other.ID, itemID: "other-item"},
	} {
		// err 表示创建测试账号失败。
		if err := store.Cookies.Save(ctx, item.cookieID, "unb=1", item.userID); err != nil {
			t.Fatalf("save cookie %s: %v", item.cookieID, err)
		}
		// err 表示创建测试商品失败。
		if err := store.Items.Upsert(ctx, &ItemInfoRow{CookieID: item.cookieID, ItemID: item.itemID, ItemTitle: item.itemID}); err != nil {
			t.Fatalf("upsert item %s: %v", item.itemID, err)
		}
	}
	// items、err 保存用户范围的一次性商品查询结果及错误。
	items, err := store.Items.ListForUser(ctx, owner.ID, "")
	if err != nil || len(items) != 1 || items[0].ItemID != "owned-item" {
		t.Fatalf("owner items=%+v err=%v", items, err)
	}
	// filtered、err 保存按账号筛选后的商品结果及错误。
	filtered, err := store.Items.ListForUser(ctx, owner.ID, "owned-cookie")
	if err != nil || len(filtered) != 1 || filtered[0].CookieID != "owned-cookie" {
		t.Fatalf("filtered items=%+v err=%v", filtered, err)
	}
	// owned、ownedErr 验证商品归属存在性查询不读取商品详情。
	owned, ownedErr := store.Items.ExistsByCookieItem(ctx, "owned-cookie", "owned-item")
	if ownedErr != nil || !owned {
		t.Fatalf("本账号商品归属=%v err=%v", owned, ownedErr)
	}
	// missing、missingErr 验证不存在商品不会被误判为当前账号商品。
	missing, missingErr := store.Items.ExistsByCookieItem(ctx, "owned-cookie", "missing-item")
	if missingErr != nil || missing {
		t.Fatalf("缺失商品归属=%v err=%v", missing, missingErr)
	}
	// deleteErr 保存商品同步下线后的逻辑删除结果；历史归属仍需作为已售订单的卖家证据。
	if deleteErr := store.Items.Delete(ctx, "owned-cookie", "owned-item"); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	// historicalOwned、historicalErr 验证软删除商品仍可证明历史订单属于当前账号。
	historicalOwned, historicalErr := store.Items.ExistsByCookieItem(ctx, "owned-cookie", "owned-item")
	if historicalErr != nil || !historicalOwned {
		t.Fatalf("软删除商品历史归属=%v err=%v", historicalOwned, historicalErr)
	}
	// forbidden、err 保存跨用户账号筛选得到的结果及错误。
	forbidden, err := store.Items.ListForUser(ctx, owner.ID, "other-cookie")
	if err != nil || len(forbidden) != 0 {
		t.Fatalf("cross-user items=%+v err=%v", forbidden, err)
	}
}
