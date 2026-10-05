package engine

import "testing"

// TestExtractItemCardObservationContentNormalizesCrossDeviceEcho 验证官方网页自身回显保存为规范商品消息。
func TestExtractItemCardObservationContentNormalizesCrossDeviceEcho(t *testing.T) {
	// frame 是模拟官方网页发送后收到的嵌套商品回显。
	frame := map[string]any{"payload": `{"contentType":7,"itemCard":{"itemTip":"我想要","item":{"itemId":"item-1","mainPic":"https://img.example/item.png","price":"¥9.90","title":"回显商品"}}}`}
	// content 是旁路观察器提取出的规范商品 JSON。
	content := extractItemCardObservationContent(frame)
	// want 是出站观察和历史解析共享的固定字段语义。
	want := `{"item_id":"item-1","title":"回显商品","image_url":"https://img.example/item.png","price":"9.90"}`
	if content != want {
		t.Fatalf("content=%q", content)
	}
}

// TestExtractItemCardObservationContentRejectsMalformedCard 验证自身回显缺少必要字段时不会落库为破损 item。
func TestExtractItemCardObservationContentRejectsMalformedCard(t *testing.T) {
	// frame 是缺少商品价格的畸形回显。
	frame := map[string]any{"itemCard": map[string]any{"item": map[string]any{"itemId": "item-1", "title": "不完整商品", "mainPic": "https://img.example/item.png"}}}
	// content 是畸形回显的归一化结果，预期保持为空。
	if content := extractItemCardObservationContent(frame); content != "" {
		t.Fatalf("畸形卡片不应被归一化: %q", content)
	}
}
