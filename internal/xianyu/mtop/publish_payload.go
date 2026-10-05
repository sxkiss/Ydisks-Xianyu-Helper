package mtop

// publishImagePayload 组装闲鱼发布接口使用的商品图片对象。
func publishImagePayload(img uploadedImage, major bool) map[string]any {
	return map[string]any{
		"extraInfo":  map[string]any{"isH": "false", "isT": "false", "raw": "false"},
		"isQrCode":   false,
		"url":        img.URL,
		"heightSize": img.Height,
		"widthSize":  img.Width,
		"major":      major,
		"type":       0,
		"status":     "done",
	}
}
