package mtop

// publishRepresentativePriceText 返回发布结果用于商品列表展示的代表价格文本。
// 多规格请求没有顶层成交价，因此使用最小有效 SKU 价格作为列表起始价。
func publishRepresentativePriceText(req PublishItemRequest) string {
	if req.PriceCents > 0 {
		return centsText(req.PriceCents)
	}
	// minimumPrice 保存多规格商品的最小有效 SKU 价格，单位为分。
	var minimumPrice int64
	// sku 表示当前待比较的多规格价格行。
	for _, sku := range req.SKUs {
		if sku.PriceCents <= 0 || (minimumPrice > 0 && sku.PriceCents >= minimumPrice) {
			continue
		}
		minimumPrice = sku.PriceCents
	}
	if minimumPrice <= 0 {
		return ""
	}
	return centsText(minimumPrice)
}
