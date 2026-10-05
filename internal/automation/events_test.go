package automation

import (
	"encoding/json"
	"testing"
)

// TestExtractTaskFromWS_BuyerReviewed 封装TestExtract任务FromWS买家Reviewed业务协调。
func TestExtractTaskFromWS_BuyerReviewed(t *testing.T) {
	// raw 用于本次流程后续判断的原始
	raw := mustMap(t, `{
	  "1": {
	    "2": "62904549781@goofish",
	    "10": {
	      "redReminder": "有新交易评价",
	      "reminderContent": "[我完成了评价]",
	      "reminderTitle": "我完成了评价",
	      "senderUserId": "2222315258815",
	      "reminderUrl": "fleamarket://message_chat?itemId=1063217820795&peerUserId=2222315258815&sid=62904549781&messageId=abc&adv=no",
	      "extJson": "{\"updateKey\":\"62904549781:3310145690545023994:10:BUYER_RATE_SELLER:26\",\"contentType\":\"26\"}"
	    }
	  }
	}`)
	// task 用于本次流程后续判断的任务
	task := ExtractTaskFromWS("acc1", "cookie", raw)
	if task == nil {
		t.Fatal("评价卡片应解析为自动化事件")
	}
	if task.TriggerType != TriggerBuyerReviewed || task.OrderID != "3310145690545023994" ||
		task.ChatID != "62904549781" || task.ItemID != "1063217820795" || task.BuyerID != "2222315258815" {
		t.Fatalf("task=%+v", task)
	}
}

// TestExtractTaskFromWS_BuyerReviewedUsesBusinessKeyAcrossCopyVariants 封装TestExtract任务FromWS买家ReviewedUsesBusinessKeyAcrossCopyVariants业务协调。
func TestExtractTaskFromWS_BuyerReviewedUsesBusinessKeyAcrossCopyVariants(t *testing.T) {
	// raw 用于本次流程后续判断的原始
	raw := mustMap(t, `{
	  "1":{"2":"62904549781@goofish","10":{
	    "reminderContent":"感谢您的再次购买，评价已经完成",
	    "senderUserId":"buyer-2",
	    "reminderUrl":"fleamarket://message_chat?itemId=item-2&peerUserId=buyer-2&sid=62904549781",
	    "extJson":"{\"updateKey\":\"62904549781:order-second:10:buyer_rate_seller:26\",\"contentType\":\"26\"}"
	  }}
	}`)
	// task 用于本次流程后续判断的任务
	task := ExtractTaskFromWS("acc1", "cookie", raw)
	if task == nil || task.TriggerType != TriggerBuyerReviewed || task.OrderID != "order-second" {
		t.Fatalf("second-purchase review task=%+v", task)
	}
}

// TestExtractTaskFromWS_OrderCompleted 验证确认收货后的官方评价提醒会推进本地订单完成事实，而不是被当作买家评价赠品事件。
func TestExtractTaskFromWS_OrderCompleted(t *testing.T) {
	// raw 保存带订单跳转链接和 contentType=25 的卖家侧确认收货系统卡片。
	raw := mustMap(t, `{
	  "1":{"2":"62904549781@goofish","7":1,"10":{
	    "reminderContent":"快给ta一个评价吧～",
	    "reminderTitle":"买家已确认收货",
	    "senderUserId":"buyer-3",
	    "reminderUrl":"fleamarket://order_detail?id=3310145690545023995&role=seller&peerUserId=buyer-3&sid=62904549781",
	    "extJson":"{\"contentType\":\"25\"}"
	  }}
	}`)
	// task 保存从确认收货系统卡片解析出的本地订单事实任务。
	task := ExtractTaskFromWS("acc1", "cookie", raw)
	if task == nil || task.TriggerType != TriggerOrderCompleted || task.OrderStatus != "completed" || task.OrderID != "3310145690545023995" {
		t.Fatalf("确认收货任务解析错误: %+v", task)
	}
}

// TestExtractTaskFromWS_ServiceReviewInvitationIgnored 封装TestExtract任务FromWSServiceReviewInvitationIgnored业务协调。
func TestExtractTaskFromWS_ServiceReviewInvitationIgnored(t *testing.T) {
	// raw 用于本次流程后续判断的原始
	raw := mustMap(t, `{
	  "1": {
	    "2": "62854995941@goofish",
	    "10": {
	      "reminderContent": "为了给您提供更好的服务，诚邀您参与服务评价>>",
	      "reminderTitle": "闲小蜜发来一条新消息",
	      "senderUserId": "1400",
	      "extJson": "{\"messageId\":\"e5e96\"}"
	    }
	  }
	}`)
	if // task 用于本次流程后续判断的任务
	task := ExtractTaskFromWS("acc1", "cookie", raw); task != nil {
		t.Fatalf("服务评价邀请不能触发买家评价赠品: %+v", task)
	}
}

// TestExtractTaskFromWS_OrderPaid 封装TestExtract任务FromWS订单Paid业务协调。
func TestExtractTaskFromWS_OrderPaid(t *testing.T) {
	// raw 用于本次流程后续判断的原始
	raw := mustMap(t, `{
	  "1": {
	    "2": "63107041124@goofish",
	    "7": 1,
	    "10": {
	      "redReminder": "等待卖家发货",
	      "reminderContent": "[我已付款，等待你发货]",
	      "senderUserId": "2222315258815",
	      "reminderUrl": "fleamarket://message_chat?itemId=1063177864132&peerUserId=2222315258815&sid=63107041124"
	    },
	    "6": {"3": {"5": "{\"dxCard\":{\"item\":{\"main\":{\"targetUrl\":\"fleamarket://order_detail?id=3310145690545023994&role=seller\"}}}}"}}
	  }
	}`)
	// task 用于本次流程后续判断的任务
	task := ExtractTaskFromWS("acc1", "cookie", raw)
	if task == nil || task.TriggerType != TriggerOrderPaid || task.OrderID != "3310145690545023994" {
		t.Fatalf("task=%+v", task)
	}
}

// TestExtractTaskFromWS_OrderPaidSignalVariants 验证参考项目明确允许的付款文案和成功小刀卡片触发条件。
func TestExtractTaskFromWS_OrderPaidSignalVariants(t *testing.T) {
	// cases 覆盖参考项目的四个直接发货文案、成功小刀系统卡片，以及不应直接发货的相似字段。
	cases := []struct {
		// name 表示当前付款信号样本的可读场景名称。
		name string
		// raw 表示当前样本的解密 WS 原始报文。
		raw string
		// want 表示当前样本是否应生成付款自动化任务。
		want bool
	}{
		{
			name: "我已付款等待发货",
			raw:  `{"1":{"7":1,"10":{"reminderContent":"[我已付款，等待你发货]"}}}`,
			want: true,
		},
		{
			name: "已付款待发货",
			raw:  `{"1":{"7":1,"10":{"reminderContent":"[已付款，待发货]"}}}`,
			want: true,
		},
		{
			name: "无方括号付款文案",
			raw:  `{"1":{"7":1,"10":{"reminderContent":"我已付款，等待你发货"}}}`,
			want: true,
		},
		{
			name: "记得及时发货",
			raw:  `{"1":{"7":1,"10":{"reminderContent":"[记得及时发货]"}}}`,
			want: true,
		},
		{
			name: "成功小刀系统卡片标题",
			raw:  `{"1":{"7":1,"6":{"3":{"5":"{\"dxCard\":{\"item\":{\"main\":{\"exContent\":{\"title\":\"我已成功小刀，待发货\",\"button\":{\"text\":\"去发货\"}},\"targetUrl\":\"fleamarket://order_detail?id=3310145690545023994&role=seller\"}}}}"}}}}`,
			want: true,
		},
		{
			name: "买家已付款相似文案不触发",
			raw:  `{"1":{"7":1,"10":{"reminderContent":"[买家已付款]"}}}`,
			want: false,
		},
		{
			name: "付款完成待发货相似文案不触发",
			raw:  `{"1":{"7":1,"10":{"redReminder":"付款完成待发货"}}}`,
			want: false,
		},
		{
			name: "付款业务键不单独触发",
			raw:  `{"1":{"7":1,"10":{"extJson":"{\"updateKey\":\"chat:3310145690545023994:10:TRADE_PAID_DONE_SELLER:26\"}"}}}`,
			want: false,
		},
		{
			name: "成功小刀普通文本不触发",
			raw:  `{"1":{"7":2,"10":{"reminderContent":"我已成功小刀，待发货"}}}`,
			want: false,
		},
	}
	// testCase 表示当前平台信号样本及其预期是否生成付款任务。
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// raw 保存当前样本的解密 WS 结构。
			raw := mustMap(t, testCase.raw)
			// task 保存统一事件入口解析出的付款任务。
			task := ExtractTaskFromWS("acc1", "cookie", raw)
			if (task != nil) != testCase.want {
				t.Fatalf("task=%+v want task=%v", task, testCase.want)
			}
			if task != nil && task.TriggerType != TriggerOrderPaid {
				t.Fatalf("trigger=%q want %q", task.TriggerType, TriggerOrderPaid)
			}
		})
	}
}

// TestExtractTaskFromWS_BargainPendingCard 验证“我已小刀，待刀成”被识别为独立免拼阶段，而不是付款发货任务。
func TestExtractTaskFromWS_BargainPendingCard(t *testing.T) {
	// raw 保存砍价第一阶段的卖家系统卡片样本。
	raw := mustMap(t, `{"1":{"7":1,"6":{"3":{"5":"{\"dxCard\":{\"item\":{\"main\":{\"exContent\":{\"title\":\"我已小刀，待刀成\"}}}}}"}}}}`)
	// task 保存从 WS 提取的独立砍价阶段任务。
	task := ExtractTaskFromWS("acc1", "cookie", raw)
	if task == nil || task.TriggerType != TriggerBargainPending || !task.IsBargain {
		t.Fatalf("待刀成卡片未生成独立免拼阶段任务: %+v", task)
	}
}

// TestExtractTaskFromWS_BargainReadyBuyerIgnored 验证成功小刀卡片的买家副本不会进入卖家自动发货。
func TestExtractTaskFromWS_BargainReadyBuyerIgnored(t *testing.T) {
	// raw 保存带有买家角色订单链接的成功小刀卡片。
	raw := mustMap(t, `{"1":{"7":1,"6":{"3":{"5":"{\"dxCard\":{\"item\":{\"main\":{\"exContent\":{\"title\":\"我已成功小刀，待发货\"},\"targetUrl\":\"fleamarket://order_detail?id=3310145690545023994&role=buyer\"}}}}"}}}}`)
	if // task 表示买家侧卡片经卖家自动化入口解析后的结果。
	task := ExtractTaskFromWS("acc1", "cookie", raw); task != nil {
		t.Fatalf("买家成功小刀卡片不应触发自动发货: %+v", task)
	}
}

// TestExtractCardSignals_DynamicOperation 验证平台把交易卡片包在 dynamicOperation 时仍能提取付款信号。
func TestExtractCardSignals_DynamicOperation(t *testing.T) {
	// content 保存动态操作协议中的交易卡片 JSON，不包含外层聊天字段。
	content := `{"dynamicOperation":{"changeContent":{"dxCard":{"item":{"main":{"exContent":{"title":"我已成功小刀，待发货","button":{"text":"去发货"}}}}}}}}`
	// cardTitle、buttonText 保存动态卡片解析出的标题和按钮文案。
	cardTitle, buttonText := extractCardSignals(content)
	if cardTitle != "我已成功小刀，待发货" || buttonText != "去发货" {
		t.Fatalf("cardTitle=%q buttonText=%q", cardTitle, buttonText)
	}
}

// TestExtractTaskFromWS_ReferenceDeliveryFields 验证路由候选字段不会越权成为直接自动发货触发条件。
func TestExtractTaskFromWS_ReferenceDeliveryFields(t *testing.T) {
	// cases 覆盖参考项目的候选字段来源，只有精确发货文案且具备系统方向时才生成付款任务。
	cases := []struct {
		// name 表示当前字段来源场景。
		name string
		// raw 表示当前解密 WS 报文。
		raw string
		// want 表示当前字段是否满足直接自动发货触发条件。
		want bool
	}{
		{name: "reminderNotice相似文案", raw: `{"1":{"7":1,"10":{"reminderNotice":"买家已付款"}}}`, want: false},
		{name: "taskName相似文案", raw: `{"1":{"7":1,"10":{"bizTag":"{\"taskName\":\"付款完成待发货_卖家\"}"}}}`, want: false},
		{name: "旁路卡片文案不作为直接触发", raw: `{"1":{"7":1,"6":{"3":{"2":"[已付款，待发货]"}}}}`, want: false},
	}
	// testCase 表示当前字段来源样本。
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// task 保存字段兼容解析出的统一自动化任务。
			task := ExtractTaskFromWS("acc1", "cookie", mustMap(t, testCase.raw))
			if (task != nil) != testCase.want {
				t.Fatalf("task=%+v want task=%v", task, testCase.want)
			}
			if task != nil && task.TriggerType != TriggerOrderPaid {
				t.Fatalf("trigger=%q want %q", task.TriggerType, TriggerOrderPaid)
			}
		})
	}
}

// TestExtractTaskFromWS_SimplifiedPaidMessage 验证 message["1"] 为会话字符串时仍提取会话、商品和买家事实。
func TestExtractTaskFromWS_SimplifiedPaidMessage(t *testing.T) {
	// raw 保存参考项目专门处理的简化“等待卖家发货”消息结构。
	raw := mustMap(t, `{"1":"63107041124@goofish","3":{"redReminder":"等待卖家发货"},"4":{"senderUserId":"buyer-1","reminderUrl":"fleamarket://message_chat?itemId=item-1&peerUserId=buyer-1&sid=63107041124"}}`)
	// task 保存简化消息经统一入口解析后的交易任务。
	task := ExtractTaskFromWS("acc1", "cookie", raw)
	if task == nil || task.TriggerType != TriggerOrderPaid || task.ChatID != "63107041124" || task.ItemID != "item-1" || task.BuyerID != "buyer-1" {
		t.Fatalf("simplified task=%+v", task)
	}
}

// TestExtractTaskFromWS_UserPaidTextIgnored 验证明确的普通用户消息不会伪造系统付款触发。
func TestExtractTaskFromWS_UserPaidTextIgnored(t *testing.T) {
	// raw 明确标记为普通接收消息，正文却包含系统付款文案。
	raw := mustMap(t, `{"1":{"7":2,"10":{"reminderContent":"[我已付款，等待你发货]","extJson":"{\"contentType\":\"1\"}"}}}`)
	// task 保存普通消息经过自动化入口后的解析结果。
	if task := ExtractTaskFromWS("acc1", "cookie", raw); task != nil {
		t.Fatalf("普通用户消息不应触发自动发货: %+v", task)
	}
}

// TestExtractTaskFromWS_BuyerOrderPaidIgnored 封装TestExtract任务FromWS买家订单PaidIgnored业务协调。
func TestExtractTaskFromWS_BuyerOrderPaidIgnored(t *testing.T) {
	// raw 用于本次流程后续判断的原始
	raw := mustMap(t, `{
	  "1": {
	    "2": "63107041124@goofish",
	    "10": {
	      "redReminder": "等待卖家发货",
	      "reminderContent": "[我已付款，等待你发货]",
	      "senderUserId": "2222315258815",
	      "reminderUrl": "fleamarket://message_chat?itemId=1063177864132&peerUserId=2222315258815&sid=63107041124"
	    },
	    "6": {"3": {"5": "{\"dxCard\":{\"item\":{\"main\":{\"targetUrl\":\"fleamarket://order_detail?id=3310145690545023994&role=buyer\"}}}}"}}
	  }
	}`)
	if // task 用于本次流程后续判断的任务
	task := ExtractTaskFromWS("acc1", "cookie", raw); task != nil {
		t.Fatalf("买家订单不应进入卖家自动化和订单管理: %+v", task)
	}
}

// TestExtractTaskFromWS_BuyerOrderPaidTaskNameIgnored 封装TestExtract任务FromWS买家订单Paid任务名称Ignored业务协调。
func TestExtractTaskFromWS_BuyerOrderPaidTaskNameIgnored(t *testing.T) {
	// raw 用于本次流程后续判断的原始
	raw := mustMap(t, `{
	  "1": {"2": "63107041124@goofish", "10": {
	    "bizTag": "{\"taskName\":\"付款完成待发货_买家\"}",
	    "redReminder": "等待卖家发货",
	    "reminderContent": "[我已付款，等待你发货]"
	  }}
	}`)
	if // task 用于本次流程后续判断的任务
	task := ExtractTaskFromWS("acc1", "cookie", raw); task != nil {
		t.Fatalf("买家侧 taskName 不应进入卖家自动化和订单管理: %+v", task)
	}
}

// TestExtractTaskFromWS_OrderCreated 校验买家拍下未付款卡片解析为 order_created 事件。
func TestExtractTaskFromWS_OrderCreated(t *testing.T) {
	// raw 是卖家收到的买家拍下待付款交易卡片样本。
	raw := mustMap(t, `{
	  "1": {
	    "2": "63107041124@goofish",
	    "10": {
	      "redReminder": "等待买家付款",
	      "reminderContent": "[我已拍下，待付款]",
	      "senderUserId": "2222315258815",
	      "reminderUrl": "fleamarket://message_chat?itemId=1063177864132&peerUserId=2222315258815&sid=63107041124",
	      "extJson": "{\"updateKey\":\"63107041124:3310145690545023994:10:BUYER_CREATE_ORDER:26\",\"contentType\":\"26\"}"
	    },
	    "6": {"3": {"5": "{\"dxCard\":{\"item\":{\"main\":{\"targetUrl\":\"fleamarket://order_detail?id=3310145690545023994&role=seller\"}}}}"}}
	  }
	}`)
	// task 是解析出的自动化任务。
	task := ExtractTaskFromWS("acc1", "cookie", raw)
	if task == nil || task.TriggerType != TriggerOrderCreated || task.OrderID != "3310145690545023994" ||
		task.ChatID != "63107041124" || task.ItemID != "1063177864132" || task.BuyerID != "2222315258815" {
		t.Fatalf("task=%+v", task)
	}
}

// TestExtractTaskFromWS_OrderCreatedUsesNestedFallbackFacts 验证新版卡片将交易字段放入非固定嵌套 JSON 时，仍能生成完整的真实改价任务。
func TestExtractTaskFromWS_OrderCreatedUsesNestedFallbackFacts(t *testing.T) {
	// raw 是保留既有展示文案、但把订单主键移动到扩展 JSON 的新版卖家交易卡片。
	raw := mustMap(t, `{
	  "1": {
	    "2": "63107041124@goofish",
	    "10": {
	      "redReminder": "等待买家付款",
	      "reminderContent": "[我已拍下，待付款]",
	      "senderUserId": "2222315258815",
	      "reminderUrl": "fleamarket://message_chat?itemId=1063177864132&peerUserId=2222315258815&sid=63107041124"
	    },
	    "8": {"extra": "{\"trade\":{\"bizOrderId\":\"3310145690545023994\"}}"}
	  }
	}`)
	// task 是解析后的订单创建任务，必须具备领取 AI 报价和调用真实改价所需的四项事实。
	task := ExtractTaskFromWS("acc1", "cookie", raw)
	if task == nil || task.TriggerType != TriggerOrderCreated || task.OrderID != "3310145690545023994" ||
		task.ChatID != "63107041124" || task.ItemID != "1063177864132" || task.BuyerID != "2222315258815" {
		t.Fatalf("新版卡片任务=%+v", task)
	}
}

// TestExtractTaskFromWS_OrderEventWithoutOrderIDRetained 验证订单 ID 尚未出现在卡片中时仍保留事件，供后续使用 updateKey 作为防重键完成卡密投递。
func TestExtractTaskFromWS_OrderEventWithoutOrderIDRetained(t *testing.T) {
	// raw 是仅含待付款展示文案、没有订单链接或业务键的非完整交易提示。
	raw := mustMap(t, `{
	  "1": {"2": "63107041124@goofish", "10": {
	    "redReminder": "等待买家付款",
	    "reminderContent": "[我已拍下，待付款]"
	  }}
	}`)
	// task 是解析结果；订单 ID 缺失不应吞掉系统卡片，协调器仍可根据 updateKey 或最终的空键门禁决定是否执行。
	task := ExtractTaskFromWS("acc1", "cookie", raw)
	if task == nil || task.TriggerType != TriggerOrderCreated || task.OrderID != "" {
		t.Fatalf("缺少订单 ID 的交易提示应保留为待协调事件: %+v", task)
	}
}

// TestExtractTaskFromWS_NestedBuyerRoleIgnored 验证角色仅存在于备用嵌套链接时，买家侧付款卡片仍不会进入卖家自动化。
func TestExtractTaskFromWS_NestedBuyerRoleIgnored(t *testing.T) {
	// raw 保留完整订单事实，但买卖角色位于新版嵌套 targetUrl，而非旧固定路径。
	raw := mustMap(t, `{
	  "1": {"2": "63107041124@goofish", "10": {
	    "redReminder": "等待卖家发货",
	    "reminderContent": "[我已付款，等待你发货]",
	    "extJson": "{\"updateKey\":\"63107041124:3310145690545023994:10:TRADE_PAID:26\"}"
	  }},
	  "payload": {"targetUrl": "fleamarket://order_detail?id=3310145690545023994&role=buyer"}
	}`)
	// task 是解析结果；当前账号作为买家时不能触发其卖家发货规则。
	task := ExtractTaskFromWS("acc1", "cookie", raw)
	if task != nil {
		t.Fatalf("嵌套买家角色不应进入卖家自动化: %+v", task)
	}
}

// TestExtractTaskFromWS_PriceModifiedEventIgnored 验证改价确认卡片不会因“等待买家付款”提示被重复识别为订单创建。
func TestExtractTaskFromWS_PriceModifiedEventIgnored(t *testing.T) {
	// raw 是闲鱼在卖家改价成功后推送的确认卡片，沿用待付款提醒但业务键明确为改价。
	raw := mustMap(t, `{
	  "1": {"2": "65854441361@goofish", "10": {
	    "redReminder": "等待买家付款",
	    "reminderContent": "[我已修改价格，等待你付款]",
	    "extJson": "{\"updateKey\":\"65854441361:5127636708304071608:2:TRADE_MODIFY_FEE_SELLER:26\"}"
	  }}
	}`)
	// task 是改价确认卡片的自动化解析结果，正确行为是不产生任何订单创建任务。
	task := ExtractTaskFromWS("acc1", "cookie", raw)
	if task != nil {
		t.Fatalf("改价确认卡片不应触发自动改价: %+v", task)
	}
}

// TestExtractTaskFromWS_BuyerOrderCreatedIgnored 校验买家角色的拍下卡片不进入卖家自动化。
func TestExtractTaskFromWS_BuyerOrderCreatedIgnored(t *testing.T) {
	// raw 是当前账号自己下单产生的买家角色拍下卡片样本。
	raw := mustMap(t, `{
	  "1": {
	    "2": "63107041124@goofish",
	    "10": {
	      "redReminder": "等待买家付款",
	      "reminderContent": "[我已拍下，待付款]",
	      "senderUserId": "2222315258815"
	    },
	    "6": {"3": {"5": "{\"dxCard\":{\"item\":{\"main\":{\"targetUrl\":\"fleamarket://order_detail?id=3310145690545023994&role=buyer\"}}}}"}}
	  }
	}`)
	if // task 是不应产生的自动化任务。
	task := ExtractTaskFromWS("acc1", "cookie", raw); task != nil {
		t.Fatalf("买家拍下订单不应进入卖家自动化: %+v", task)
	}
}

// mustMap 封装mustMap业务协调。
func mustMap(t *testing.T, s string) map[string]any {
	t.Helper()
	// m 用于本次流程后续判断的m
	var m map[string]any
	if // err 用于本次流程后续判断的err
	err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}
