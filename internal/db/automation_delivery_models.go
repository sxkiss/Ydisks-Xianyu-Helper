package db

// AutomationDeliveryProof 保存确认发货和失败重发需要的订单发货快照。
// 每个内容字段只在数据库仓储和自动化执行器之间以加密形式流转，禁止序列化到 HTTP 或日志。
type AutomationDeliveryProof struct {
	// TradeText 是已发送给买家的文本凭证，多个动作按顺序合并。
	TradeText string `json:"trade_text"`
	// PicList 是已发送给买家的图片地址，顺序与消息发送顺序一致。
	PicList []string `json:"pic_list"`
	// Messages 按原始发送顺序保存文本和图片，人工补发时使用它避免重新获取、消费或扣除卡密。
	Messages []AutomationDeliveryMessage `json:"messages"`
	// ExpectedUnits 累计已进入执行的发货动作单位数；补发必须与原始完整计划核对，旧记录为零时不能逐件恢复。
	ExpectedUnits int `json:"expected_units,omitempty"`
	// PreparedUnits 是已经取得并保存内容的确定单位数，用于补齐剩余数量。
	PreparedUnits int `json:"prepared_units,omitempty"`
	// UnknownUnits 是结果不确定、需要人工确认后原样补发的单位数。
	UnknownUnits int `json:"unknown_units,omitempty"`
	// RefillPending 表示补取卡密前已持久化占用、但结果尚未可靠收口；跨取消或重启都禁止再次取卡。
	RefillPending bool `json:"refill_pending,omitempty"`
	// SkippedTemplateMessages 保存渲染为空且已成功恢复库存的模板消息位置；索引对应冻结动作计划，不能由当前规则推算。
	SkippedTemplateMessages []AutomationDeliverySkip `json:"skipped_template_messages,omitempty"`
}

// AutomationDeliverySkip 标识原始动作计划中一个合法跳过的模板消息。
type AutomationDeliverySkip struct {
	// ActionIndex 是原始动作计划中的模板动作下标。
	ActionIndex int `json:"action_index"`
	// MessageIndex 是该模板消息列表中的零基下标。
	MessageIndex int `json:"message_index"`
}
