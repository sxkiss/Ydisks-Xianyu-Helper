package engine

import "testing"

// TestExecutableAIOfferKeepsMarkerWhenSanitized 锁定报价标记与正文安全过滤的边界。
// 回归背景：曾把 sanitizeAIContent 包在 extractExecutableOffer 外层，
// 导致思考块/答案块包裹时内部报价标记被连带删除，出现"该改价没改价"。
func TestExecutableAIOfferKeepsMarkerWhenSanitized(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		wantPrice float64
		wantOK    bool
	}{
		{"标记在末尾", "亲，最低 88.5 元 [[AUTO_PRICE:88.50]]", 88.50, true},
		{"闭合思考块后标记", "<think>成本70</think>亲，88.5 元 [[AUTO_PRICE:88.50]]", 88.50, true},
		{"答案块外标记", "<answer>亲，88.5 元</answer> [[AUTO_PRICE:88.50]]", 88.50, true},
		{"标记在思考块内", "<think>定 [[AUTO_PRICE:88.50]]</think>亲，88.5 元", 88.50, true},
		{"多标记不可执行", "88.5 元 [[AUTO_PRICE:88.50]] [[AUTO_PRICE:87.00]]", 0, false},
		{"非法标记不可执行", "[[AUTO_PRICE:bad]] 88.5 元", 0, false},
		{"无标记", "您好，请问有什么可以帮您？", 0, false},
	}
	for _, c := range cases {
		visible, price, ok := executableAIOffer(c.content)
		if ok != c.wantOK || price != c.wantPrice {
			t.Fatalf("%s: price=%v ok=%v，期望 price=%v ok=%v", c.name, price, ok, c.wantPrice, c.wantOK)
		}
		// 买家可见正文绝不能残留思考块或内部标记。
		if containsAny(visible, "<think", "<thinking", "<reasoning", "AUTO_PRICE") {
			t.Fatalf("%s: 正文泄漏内部内容: %q", c.name, visible)
		}
	}
}

// TestExecutableAIOfferDropsUnsafeOutput 验证错误原文与自言自语不得发给买家。
func TestExecutableAIOfferDropsUnsafeOutput(t *testing.T) {
	for _, content := range []string{
		"API返回错误: 502 bad gateway",
		"[error] upstream timeout",
		"用户发来消息，让我用技能处理",
		"<think>成本70 亲，88.5 元 [[AUTO_PRICE:88.50]]",
	} {
		if visible, price, ok := executableAIOffer(content); visible != "" || ok || price != 0 {
			t.Fatalf("不安全输出应整体丢弃: %q -> %q %v %v", content, visible, price, ok)
		}
	}
}

// containsAny 判断 s 是否包含任一子串。
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// TestAIPlatformNoticeFilterIsPrecise 锁定平台系统通知过滤的精确边界。
// 回归背景：曾用"以 [ 开头"粗暴拦截，误伤买家表情（[微笑]）与图片（[图片] url）消息，
// 导致这些真实买家消息被拒绝自动回复；必须只拦平台合规提示本身。
func TestAIPlatformNoticeFilterIsPrecise(t *testing.T) {
	// 平台合规通知：整条即一个方括号提示且含合规关键词，必须拦截。
	blocked := []string{
		"[请勿引导买家脱离优推抵扣商品进行交易]",
	}
	// 真实买家消息：表情、图片、表情+文字与普通文本，必须放行。
	allowed := []string{
		"[微笑]", "[尴尬]", "[思考]",
		"[暗中观察]怎么搞啊",
		"[图片]", "[图片] https://img.alicdn.com/imgextra/i3/2206533066862/O1CN01.jpg",
		"在吗，这个还有货吗", "多少钱",
	}
	for _, text := range blocked {
		if !aiPlatformNoticeRe.MatchString(text) {
			t.Fatalf("平台通知应被拦截: %q", text)
		}
	}
	for _, text := range allowed {
		if aiPlatformNoticeRe.MatchString(text) {
			t.Fatalf("买家消息被误拦: %q", text)
		}
	}
}
