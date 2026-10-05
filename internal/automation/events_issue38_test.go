package automation

import "testing"

// TestIssue38ModernPaidCard 验证新版消息 ID 不充当会话 ID，付款正文优先于残留未付款提醒；t 管理断言。
func TestIssue38ModernPaidCard(t *testing.T) {
	// cases 覆盖卖家付款、买家副本、待付款和畸形卡片，避免漏发与误发。
	cases := []struct {
		// name 是场景名称。
		name string
		// raw 是脱敏后的平台原始帧。
		raw string
		// trigger 是预期事件，空值表示不能进入自动化。
		trigger string
		// simplified 表示是否应走旧版精确红色提醒分支。
		simplified bool
	}{
		{"卖家新版付款", `{"1":"message@goofish","2":"chat@goofish","3":1,"4":{"reminderContent":"[已付款，待发货]","redReminder":"等待买家付款","bizTag":"{\"taskName\":\"已拍下_未付款_卖家\"}","extJson":"{\"contentType\":\"26\"}"}}`, TriggerOrderPaid, false},
		{"买家新版付款", `{"1":"message","2":"chat@goofish","3":1,"4":{"reminderContent":"[已付款，待发货]","bizTag":"{\"taskName\":\"已付款_买家\"}"}}`, "", false},
		{"新版待付款", `{"1":"message","2":"chat@goofish","3":1,"4":{"redReminder":"等待买家付款"}}`, TriggerOrderCreated, false},
		{"旧简化付款", `{"1":"chat@goofish","2":1,"3":{"redReminder":"等待卖家发货"}}`, TriggerOrderPaid, true},
		{"缺少卡片状态", `{"1":"message","2":"chat@goofish","3":1,"4":{}}`, "", false},
		{"畸形卡片", `{"1":"message","2":"chat@goofish","3":1,"4":"invalid"}`, "", true},
	}
	// scenario 是当前平台结构与交易角色组合。
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			// raw 是当前案例的解码帧，不含真实账号或凭证。
			raw := mustMap(t, scenario.raw)
			// fields 保存事件分类前的结构识别结果。
			fields := fieldsFromRaw(raw)
			if fields.simplified != scenario.simplified || fields.chatID != "chat" {
				t.Fatalf("结构识别错误 simplified=%v chat=%s", fields.simplified, fields.chatID)
			}
			// task 是待交给自动化中心的结果，空值不应创建任务。
			task := ExtractTaskFromWS("account", "", raw)
			if scenario.trigger == "" {
				if task != nil {
					t.Fatal("不应产生可执行任务")
				}
			} else if task == nil || task.TriggerType != scenario.trigger {
				t.Fatalf("应识别为 %s", scenario.trigger)
			}
		})
	}
}
