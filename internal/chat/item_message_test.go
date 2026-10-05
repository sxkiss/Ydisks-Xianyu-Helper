package chat

import "testing"

// TestExtractMessageContentNormalizesItemCard 验证历史商品卡片转换为稳定 item 消息正文。
func TestExtractMessageContentNormalizesItemCard(t *testing.T) {
	// raw 是官网 contentType=7 的个人会话商品卡片。
	raw := map[string]any{"contentType": float64(7), "itemCard": map[string]any{"itemTip": "我想要", "item": map[string]any{"itemId": "item-1", "title": "测试商品", "mainPic": "https://img.example/item.png", "price": "¥19.90"}}}
	// kind 和 content 是历史解析器输出的消息类型与规范正文。
	kind, content := extractMessageContent(raw, "我想要")
	// wantContent 是前端商品卡片只接受的固定字段 JSON。
	wantContent := `{"item_id":"item-1","title":"测试商品","image_url":"https://img.example/item.png","price":"19.90"}`
	if kind != "item" || content != wantContent {
		t.Fatalf("kind=%q content=%q", kind, content)
	}
}

// TestExtractMessageContentFallsBackForMalformedItemCard 验证缺字段卡片保留安全平台摘要而不制造破损商品消息。
func TestExtractMessageContentFallsBackForMalformedItemCard(t *testing.T) {
	// raw 是缺少主图的畸形商品卡片。
	raw := map[string]any{"contentType": float64(7), "itemCard": map[string]any{"item": map[string]any{"itemId": "item-1", "title": "测试商品", "price": "¥19.90"}}}
	// kind 和 content 是畸形卡片的安全降级结果。
	kind, content := extractMessageContent(raw, "我想要")
	if kind != "text" || content != "我想要" {
		t.Fatalf("kind=%q content=%q", kind, content)
	}
}
