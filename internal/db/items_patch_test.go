package db

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

// TestMultiDB_ItemsPatch 验证三方言局部更新保留省略字段，显式空值清空且不恢复已删除记录；t 管理隔离数据库。
func TestMultiDB_ItemsPatch(t *testing.T) {
	// target 为各方言的一次性数据库及清理责任。
	for _, target := range allTestTargets(t) {
		t.Run(target.name, func(t *testing.T) {
			defer target.cleanup()
			// store、ctx 管理本地方言调用，不访问平台。
			store, ctx := target.store, context.Background()
			// cookieID 是测试商品的账号归属。
			_, cookieID := seedAccount(t, store)
			// original 保存全字段初始商品，用于逐字段验证省略语义。
			original := ItemInfoRow{CookieID: cookieID, ItemID: "patch", ItemTitle: "标题", ItemDescription: "描述", ItemCategory: "类目", ItemPrice: "10", ItemDetail: "详情", IsMultiSpec: true, MultiQuantityDelivery: true}
			// err 检查夹具写入结果。
			if err := store.Items.Upsert(ctx, &original); err != nil {
				t.Fatal(err)
			}
			// before、err 是更新前的完整持久化快照。
			before, err := store.Items.Get(ctx, cookieID, original.ItemID)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Items.Patch(ctx, cookieID, original.ItemID, ItemPatch{}); err != nil {
				t.Fatal(err)
			}
			// after、err 是空补丁后的快照，重复保存应成功并逐字段不变。
			after, err := store.Items.Get(ctx, cookieID, original.ItemID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("空补丁改变字段：%v", err)
			}
			// empty、disabled 显式清空文本并关闭两个交付开关。
			empty, disabled := "", false
			if err = store.Items.Patch(ctx, cookieID, original.ItemID, ItemPatch{ItemTitle: &empty, ItemDescription: &empty, ItemCategory: &empty, ItemPrice: &empty, ItemDetail: &empty, IsMultiSpec: &disabled, MultiQuantityDelivery: &disabled}); err != nil {
				t.Fatal(err)
			}
			after, err = store.Items.Get(ctx, cookieID, original.ItemID)
			if err != nil {
				t.Fatal(err)
			}
			before.ItemTitle, before.ItemDescription, before.ItemCategory, before.ItemPrice, before.ItemDetail = "", "", "", "", ""
			before.IsMultiSpec, before.MultiQuantityDelivery = false, false
			if !reflect.DeepEqual(before, after) {
				t.Fatal("显式空值未完整写入")
			}
			// enabled 验证布尔真值通过原子补丁写入。
			enabled := true
			if err = store.Items.Patch(ctx, cookieID, original.ItemID, ItemPatch{IsMultiSpec: &enabled, MultiQuantityDelivery: &enabled}); err != nil {
				t.Fatal(err)
			}
			after, err = store.Items.Get(ctx, cookieID, original.ItemID)
			if err != nil || !after.IsMultiSpec || !after.MultiQuantityDelivery {
				t.Fatalf("布尔真值未保存：%v", err)
			}
			if err = store.Items.Patch(ctx, "other-account", original.ItemID, ItemPatch{ItemTitle: &empty}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("跨账号写入应不存在：%v", err)
			}
			if err = store.Items.Patch(ctx, cookieID, "missing", ItemPatch{}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("缺失商品：%v", err)
			}
			if err = store.Items.Delete(ctx, cookieID, original.ItemID); err != nil {
				t.Fatal(err)
			}
			if err = store.Items.Patch(ctx, cookieID, original.ItemID, ItemPatch{ItemTitle: &empty}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("已删除商品不应恢复：%v", err)
			}
			if _, err = store.Items.Get(ctx, cookieID, original.ItemID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("商品被意外恢复：%v", err)
			}
			// canceled、cancel 提供已取消调用，确保数据库错误被透传。
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if err = store.Items.Patch(canceled, cookieID, original.ItemID, ItemPatch{}); !errors.Is(err, context.Canceled) {
				t.Fatalf("取消错误未透传：%v", err)
			}
		})
	}
}

// TestItemsPatchRowsAffectedError 使用现有驱动故障夹具验证 SQL 结果元数据错误不被吞掉；t 管理连接。
func TestItemsPatchRowsAffectedError(t *testing.T) {
	passwordUpgradeRowsRegisterOnce.Do(func() { sql.Register(passwordUpgradeRowsDriverName, passwordUpgradeRowsDriver{}) })
	// database、err 是只返回受影响行数故障的本地驱动连接。
	database, err := sql.Open(passwordUpgradeRowsDriverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	// items 为使用故障驱动的被测仓储。
	items := &Items{DB: database}
	if err = items.Patch(context.Background(), "test", "item", ItemPatch{}); !errors.Is(err, errPasswordUpgradeRows) {
		t.Fatalf("驱动元数据错误未透传：%v", err)
	}
}
