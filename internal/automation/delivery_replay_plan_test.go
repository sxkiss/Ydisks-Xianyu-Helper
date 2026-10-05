package automation

import (
	"encoding/json"
	"testing"

	"xianyu-go/internal/db"
)

// TestDeliveryReplayPlanBoundaries 验证历史计划归属、完整性和直接/模板计数；t 的用例均不访问数据库或平台。
func TestDeliveryReplayPlanBoundaries(t *testing.T) {
	// card 是原单件订单发送两份卡密的动作。
	card := db.AutomationAction{ActionType: ActionSendCard, CardID: 3, Enabled: true, DeliveryCount: 2, ConfigJSON: `{}`}
	// template 是包含两条消息的模板动作，每条消息对应一个快照单位。
	template := db.AutomationAction{ActionType: ActionSendTemplate, Enabled: true, ConfigJSON: `{}`, TemplateMessages: []string{"first", "second"}}
	// scenarios 描述原始计划和快照数量，bad 表示不具备安全补发条件。
	scenarios := []struct {
		// name 是不含发货内容的测试名称。
		name string
		// actions 是运行创建时保存的动作，不受当前规则编辑影响。
		actions []db.AutomationAction
		// expected、prepared、unknown 分别是进入执行、已确定和不确定单位数。
		expected, prepared, unknown int
		// skipped 保存已经确认渲染为空的模板消息位置。
		skipped []db.AutomationDeliverySkip
		// bad 表示该快照必须被拒绝。
		bad bool
	}{
		{"direct-partial", []db.AutomationAction{card}, 2, 1, 0, nil, false},
		{"direct-complete", []db.AutomationAction{card}, 2, 2, 0, nil, false},
		{"template-complete", []db.AutomationAction{template}, 2, 2, 0, nil, false},
		{"template-unknown-content", []db.AutomationAction{template}, 2, 1, 1, nil, false},
		{"template-skipped-content", []db.AutomationAction{template}, 2, 1, 0, []db.AutomationDeliverySkip{{ActionIndex: 0, MessageIndex: 1}}, false},
		{"template-missing-content", []db.AutomationAction{template}, 2, 1, 0, nil, true},
		{"multi-action-complete", []db.AutomationAction{card, template}, 4, 4, 0, nil, false},
		{"multi-action-ambiguous", []db.AutomationAction{card, template}, 4, 2, 0, nil, true},
		{"unexecuted-action", []db.AutomationAction{card, template}, 2, 2, 0, nil, true},
		{"missing-plan", nil, 2, 1, 0, nil, true},
		{"empty-delivery-plan", []db.AutomationAction{{ActionType: ActionConfirmShipment, Enabled: true}}, 2, 1, 0, nil, true},
		{"disabled-action", []db.AutomationAction{{ActionType: ActionSendCard, Enabled: false}}, 2, 1, 0, nil, true},
		{"legacy-counts", []db.AutomationAction{card}, 0, 0, 0, nil, true},
		{"negative-prepared", []db.AutomationAction{card}, 2, -1, 0, nil, true},
		{"negative-unknown", []db.AutomationAction{card}, 2, 1, -1, nil, true},
		{"excess-counts", []db.AutomationAction{card}, 2, 2, 1, nil, true},
	}
	for _, scenario := range scenarios { // scenario 是每次独立构造的历史计划和快照。
		t.Run(scenario.name, func(t *testing.T) { // t 只管理当前用例的返回错误断言。
			// original 固定运行的账号、订单和数量。
			original := Task{AccountID: "account", OrderID: "order", Quantity: "1", ActionPlan: scenario.actions}
			// raw、err 保存合法历史计划的序列化结果。
			raw, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			// run 是交给纯函数校验的运行快照。
			run := &db.AutomationRun{CookieID: "account", OrderID: "order", RawEventJSON: string(raw), DeliveryProof: db.AutomationDeliveryProof{ExpectedUnits: scenario.expected, PreparedUnits: scenario.prepared, UnknownUnits: scenario.unknown, SkippedTemplateMessages: scenario.skipped}}
			// action、planErr 保存唯一补齐来源和拒绝原因。
			action, planErr := deliveryReplayPlan(run)
			if (planErr != nil) != scenario.bad {
				t.Fatalf("计划判断异常: %v", planErr)
			}
			if scenario.name == "direct-partial" && action.CardID != card.CardID {
				t.Fatal("未保留原始卡组")
			}
		})
	}
	if _, err := deliveryReplayPlan(nil); err == nil { // err 应报告不存在的运行。
		t.Fatal("空运行应拒绝")
	}
	// invalid 表示缺少可解析原始计划的历史快照。
	invalid := &db.AutomationRun{RawEventJSON: "broken", DeliveryProof: db.AutomationDeliveryProof{ExpectedUnits: 1}}
	if _, err := deliveryReplayPlan(invalid); err == nil { // err 应拒绝损坏的原始 JSON。
		t.Fatal("损坏计划应拒绝")
	}
	invalid.RawEventJSON = `{"AccountID":"other","OrderID":"order","ActionPlan":[{"ActionType":"send_card"}]}`
	if _, err := deliveryReplayPlan(invalid); err == nil { // err 应拒绝其他订单或账号的计划。
		t.Fatal("归属不符应拒绝")
	}
	invalid.DeliveryProof.RefillPending = true
	if _, err := deliveryReplayPlan(invalid); err == nil { // err 应优先保护未收口的补取操作。
		t.Fatal("补取占用应拒绝")
	}
	// duplicateRaw、duplicateErr 保存重复模板跳过位置的拒绝夹具。
	duplicateRaw, duplicateErr := json.Marshal(Task{AccountID: "account", OrderID: "order", Quantity: "1", ActionPlan: []db.AutomationAction{template}})
	if duplicateErr != nil {
		t.Fatal(duplicateErr)
	}
	// duplicateRun 表示包含重复跳过位置的损坏凭证。
	duplicateRun := &db.AutomationRun{CookieID: "account", OrderID: "order", RawEventJSON: string(duplicateRaw), DeliveryProof: db.AutomationDeliveryProof{ExpectedUnits: 2, PreparedUnits: 1, SkippedTemplateMessages: []db.AutomationDeliverySkip{{ActionIndex: 0, MessageIndex: 1}, {ActionIndex: 0, MessageIndex: 1}}}}
	if _, err := deliveryReplayPlan(duplicateRun); err == nil { // err 应拒绝重复跳过位置，避免伪造完整快照。
		t.Fatal("重复模板跳过位置应拒绝")
	}
	// outOfRangeRun 表示指向模板消息范围外的损坏凭证。
	outOfRangeRun := &db.AutomationRun{CookieID: "account", OrderID: "order", RawEventJSON: string(duplicateRaw), DeliveryProof: db.AutomationDeliveryProof{ExpectedUnits: 2, PreparedUnits: 1, SkippedTemplateMessages: []db.AutomationDeliverySkip{{ActionIndex: 0, MessageIndex: 2}}}}
	if _, err := deliveryReplayPlan(outOfRangeRun); err == nil { // err 应拒绝越界跳过位置。
		t.Fatal("越界模板跳过位置应拒绝")
	}
	// excessSkipRun 表示合法位置重复之外，凭证总单位数超过原始计划的损坏快照。
	excessSkipRun := &db.AutomationRun{CookieID: "account", OrderID: "order", RawEventJSON: string(duplicateRaw), DeliveryProof: db.AutomationDeliveryProof{ExpectedUnits: 2, PreparedUnits: 1, SkippedTemplateMessages: []db.AutomationDeliverySkip{{ActionIndex: 0, MessageIndex: 0}, {ActionIndex: 0, MessageIndex: 1}}}}
	if _, err := deliveryReplayPlan(excessSkipRun); err == nil { // err 应在领取补发前拒绝超额单位，避免先占用运行。
		t.Fatal("超额模板跳过凭证应拒绝")
	}
}
