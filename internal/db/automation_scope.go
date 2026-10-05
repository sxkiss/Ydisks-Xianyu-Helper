package db

import "encoding/json"

// confirmedDeliveryRules 对 order_paid 的账号通用规则实施显式授权门禁；rules 为按优先级排列的候选。
// 商品级及非付款规则保持既有范围；旧账号规则只有配置 allow_all_items=true 才可用于其他商品，解析失败拒绝兜底。
func confirmedDeliveryRules(rules []AutomationRule, triggerType string) []AutomationRule {
	if triggerType != "order_paid" {
		return rules
	}
	// confirmed 保存通过范围确认的候选，不修改调用方的规则快照。
	confirmed := make([]AutomationRule, 0, len(rules))
	// rule 是待核验的一个候选规则。
	for _, rule := range rules {
		if rule.ItemID != "" {
			confirmed = append(confirmed, rule)
			continue
		}
		// scope 只解析明确的通用发货授权，字符串或损坏配置均不能视为同意。
		var scope struct {
			// AllowAllItems 表示用户确认此内容可发给当前账号全部商品。
			AllowAllItems bool `json:"allow_all_items"`
		}
		if json.Unmarshal([]byte(rule.ConfigJSON), &scope) == nil && scope.AllowAllItems {
			confirmed = append(confirmed, rule)
		}
	}
	return confirmed
}
