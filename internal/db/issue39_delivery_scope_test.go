package db

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
)

// TestConfirmedDeliveryRules 验证旧账号规则不能静默发货，只有显式布尔确认才允许全商品兜底；t 管理断言。
func TestConfirmedDeliveryRules(t *testing.T) {
	// config 是当前被测试的旧版、无效或明确拒绝的规则配置。
	for _, config := range []string{"", "{}", "null", "invalid", `{"allow_all_items":false}`, `{"allow_all_items":"true"}`, `{"allow_all_items":1}`} {
		if len(confirmedDeliveryRules([]AutomationRule{{ConfigJSON: config}}, "order_paid")) != 0 {
			t.Fatal("未经明确授权的账号规则不应发送任何商品内容")
		}
	}
	// rules 同时含未确认账号规则、商品规则与明确确认的账号通用规则。
	rules := []AutomationRule{{ID: 1}, {ID: 2, ItemID: "item"}, {ID: 3, ConfigJSON: `{"allow_all_items":true}`}}
	// matched 是过滤后仍保持原优先级顺序的候选。
	matched := confirmedDeliveryRules(rules, "order_paid")
	if len(matched) != 2 || matched[0].ID != 2 || matched[1].ID != 3 || len(confirmedDeliveryRules(rules, "buyer_reviewed")) != 3 {
		t.Fatal("商品规则、明确授权通用规则或其他触发器的范围发生变化")
	}
}

// TestMultiDB_Issue39RelistingAndDeliveryScope 在可用方言中验证下架、重上架、旧规则隔离及账号通用授权；t 管理目标生命周期。
func TestMultiDB_Issue39RelistingAndDeliveryScope(t *testing.T) {
	// target 是当前可用的 SQLite、MySQL 或 PostgreSQL 测试库。
	for _, target := range allTestTargets(t) {
		t.Run(target.name, func(t *testing.T) {
			defer target.cleanup()
			// ctx 限定所有本地仓储操作的请求生命周期。
			ctx := context.Background()
			// store 是该方言的仓储实现。
			store := target.store
			// accountID 同时用于唯一测试账号和用户，避免外部数据库测试相互污染。
			accountID := fmt.Sprintf("issue39_%s_%d", target.name, atomic.AddUint64(&multidbCounter, 1))
			// created、createErr 保存合成测试用户创建结果。
			created, createErr := store.Users.Create(ctx, accountID, accountID+"@example.com", "test-only-password")
			if createErr != nil || !created {
				t.Fatal("创建测试用户失败")
			}
			// user、userErr 保存规则所有者身份查询结果。
			user, userErr := store.Users.GetByUsername(ctx, accountID)
			if userErr != nil {
				t.Fatal(userErr)
			}
			if saveErr := store.Cookies.Save(ctx, accountID, "unb=test", user.ID); saveErr != nil { // saveErr 是合成账号保存错误。
				t.Fatal(saveErr)
			}
			// itemA 是下架后以同 ID 重上架的商品，带需保留的本地描述与发货设置。
			itemA := ItemInfoRow{CookieID: accountID, ItemID: "item-a", ItemTitle: "商品 A", ItemDescription: "本地说明", IsMultiSpec: true, MultiQuantityDelivery: true}
			if saveErr := store.Items.Upsert(ctx, &itemA); saveErr != nil { // saveErr 是商品夹具写入错误。
				t.Fatal(saveErr)
			}
			// createRule 用 itemID 和 config 明确声明规则范围，返回规则 ID；priority 控制候选顺序。
			createRule := func(itemID, config string, priority int) int64 {
				// id、err 保存该规则及其商品专用文本动作的创建结果。
				id, err := store.Automation.Create(ctx, AutomationRuleInput{UserID: user.ID, CookieID: accountID, ItemID: itemID, Name: "范围测试", TriggerType: "order_paid", Enabled: true, Priority: priority, ConfigJSON: config, Actions: []AutomationActionInput{{ActionType: "send_text", MessageTemplate: "商品内容", Enabled: true}}})
				if err != nil {
					t.Fatal(err)
				}
				return id
			}
			// originalRule 是下架前商品 A 的旧规则，不得在商品恢复后自动重新启用。
			originalRule := createRule("item-a", "{}", 20)
			createRule("", "{}", 1)
			// expectRule 通过真实仓储验证 itemID 的最终匹配，expected=0 表示必须阻止发送。
			expectRule := func(itemID string, expected int64) {
				// rules、err 保存按账号、商品和付款触发器查询的结果。
				rules, err := store.Automation.Match(ctx, accountID, itemID, "order_paid")
				if err != nil || (expected == 0 && len(rules) != 0) || (expected != 0 && (len(rules) != 1 || rules[0].ID != expected)) {
					t.Fatalf("商品 %s 匹配错误，期望规则 %d，候选数量 %d，错误 %v", itemID, expected, len(rules), err)
				}
			}
			expectRule("item-a", originalRule)
			expectRule("item-b", 0)
			expectRule("", 0)
			if _, syncErr := store.Items.SyncFromRemote(ctx, accountID, nil); syncErr != nil { // syncErr 是已确认完整空列表的下架事务错误。
				t.Fatal(syncErr)
			}
			expectRule("item-a", 0)
			// remoteA 仅包含重新上架后的远端数据，本地描述和多数量设置必须保留。
			remoteA := ItemInfoRow{ItemID: "item-a", ItemTitle: "重新上架 A", IsMultiSpec: true}
			if _, syncErr := store.Items.SyncFromRemote(ctx, accountID, []ItemInfoRow{remoteA, {ItemID: "item-new", ItemTitle: "新 ID 商品"}}); syncErr != nil { // syncErr 是商品恢复事务错误。
				t.Fatal(syncErr)
			}
			// restored、restoreErr 保存恢复后正常管理查询可见的商品。
			restored, restoreErr := store.Items.Get(ctx, accountID, "item-a")
			if restoreErr != nil || restored.ItemDescription != itemA.ItemDescription || !restored.IsMultiSpec || !restored.MultiQuantityDelivery {
				t.Fatal("重上架商品没有恢复，或本地设置丢失")
			}
			expectRule("item-a", 0)
			// genericRule 是用户明确确认的新通用规则，旧高优先级账号规则不能抢占它。
			genericRule := createRule("", `{"allow_all_items":true}`, 50)
			expectRule("item-a", genericRule)
			expectRule("item-new", genericRule)
			// replacement 是用户为重新上架商品重新配置的规则，应优先于账号通用规则。
			replacement := createRule("item-a", "{}", 100)
			expectRule("item-a", replacement)
			expectRule("item-new", genericRule)
		})
	}
}
