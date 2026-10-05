package mtop

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestPublishPayloadBuilders 封装Test发布请求载荷Builders业务协调。
func TestPublishPayloadBuilders(t *testing.T) {
	// req 用于本次流程后续判断的req
	req := PublishItemRequest{PriceCents: 1234, OriginalPriceCents: 2000, PostageMode: "fixed", PostageCents: 800}
	// price 用于本次流程后续判断的price
	price := publishPriceDTO(req)
	if price["priceInCent"] != "1234" || price["origPriceInCent"] != "2000" {
		t.Fatalf("publishPriceDTO = %#v", price)
	}
	// postage 用于本次流程后续判断的postage
	postage := postageDTO(req)
	if postage["postPriceInCent"] != "800" || postage["templateId"] != "0" {
		t.Fatalf("postageDTO = %#v", postage)
	}
	if // got 用于本次流程后续判断的got
	got := postageDTO(PublishItemRequest{PostageMode: "free"}); got["canFreeShipping"] != true {
		t.Fatalf("free postage = %#v", got)
	}
	if // got 用于本次流程后续判断的got
	got := postageDTO(PublishItemRequest{PostageMode: "distance"}); got["templateId"] != "-100" {
		t.Fatalf("distance postage = %#v", got)
	}
}

// TestPublishTokenFailureUsesTypedClassification 验证发布错误只接受统一 Token 类型，不依赖可变的错误文本。
func TestPublishTokenFailureUsesTypedClassification(t *testing.T) {
	// tokenErr 保存统一 Token 过期错误，代表可以安全刷新后重试的错误。
	tokenErr := &MTopResponseError{Kind: MTopErrorTokenExpired, API: "publish"}
	if !isPublishTokenFailure(fmt.Errorf("外层包装: %w", tokenErr)) {
		t.Fatal("统一 Token 错误应被识别")
	}
	// canceledErr 模拟“Token 过期且刷新失败”文本中包装了取消错误的情况。
	canceledErr := fmt.Errorf("publish Token 过期且刷新失败: %w", context.Canceled)
	if isPublishTokenFailure(canceledErr) {
		t.Fatal("取消错误不能因错误文本包含 Token 和刷新而被归类为认证过期")
	}
}

// TestPublishParsingAndErrors 封装Test发布ParsingAnd错误列表业务协调。
func TestPublishParsingAndErrors(t *testing.T) {
	if // w、h 用于本次流程后续判断的w、h
	w, h := parsePix("800x600"); w != 800 || h != 600 {
		t.Fatalf("parsePix = %d x %d", w, h)
	}
	if // w、h 用于本次流程后续判断的w、h
	w, h := parsePix("bad"); w != 0 || h != 0 {
		t.Fatalf("invalid parsePix = %d x %d", w, h)
	}
	if // got 用于本次流程后续判断的got
	got := centsText(1234); got != "12.34" {
		t.Fatalf("centsText = %q", got)
	}
	if // got 用于本次流程后续判断的got
	got := findStringDeep(map[string]any{"outer": map[string]any{"itemId": "42"}}, "itemId"); got != "42" {
		t.Fatalf("findStringDeep = %q", got)
	}

	// err 用于本次流程后续判断的err
	err := classifyPublishError([]string{"FAIL_SYS_TOKEN_EXPIRED::令牌过期"}, map[string]any{})
	// publishErr 用于本次流程后续判断的发布Err
	var publishErr *PublishError
	if !errors.As(err, &publishErr) || publishErr.Code != PublishErrorTokenExpired {
		t.Fatalf("token error = %#v", err)
	}
	err = classifyPublishError([]string{"账号没有库存发布权限"}, map[string]any{})
	if !errors.As(err, &publishErr) || publishErr.Code != PublishErrorStockPermissionMissing {
		t.Fatalf("stock error = %#v", err)
	}
}
