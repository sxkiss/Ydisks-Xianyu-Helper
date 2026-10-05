package chat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// normalizedItemCardContent 将官方 contentType=7 载荷转换为前端可稳定解析的非敏感 JSON。
func normalizedItemCardContent(value map[string]any) string {
	// itemCard 是官方商品卡片正文的外层对象。
	itemCard, _ := value["itemCard"].(map[string]any)
	// item 是包含商品身份和展示字段的内层对象。
	item, _ := itemCard["item"].(map[string]any)
	if item == nil {
		return ""
	}
	// itemID 和 title 是完整商品气泡的必需身份字段。
	itemID, title := strings.TrimSpace(fmt.Sprint(item["itemId"])), strings.TrimSpace(fmt.Sprint(item["title"]))
	// imageURL 和 price 是完整商品气泡的必需展示字段。
	imageURL, price := strings.TrimSpace(fmt.Sprint(item["mainPic"])), strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(fmt.Sprint(item["price"])), "¥"))
	if strings.HasPrefix(imageURL, "//") {
		imageURL = "https:" + imageURL
	}
	if itemID == "" || itemID == "<nil>" || title == "" || title == "<nil>" || imageURL == "" || imageURL == "<nil>" || price == "" || price == "<nil>" {
		return ""
	}
	// encoded 是固定字段顺序的商品卡片 JSON。
	encoded, marshalErr := json.Marshal(struct {
		ItemID   string `json:"item_id"`
		Title    string `json:"title"`
		ImageURL string `json:"image_url"`
		Price    string `json:"price"`
	}{ItemID: itemID, Title: title, ImageURL: imageURL, Price: price})
	if marshalErr != nil {
		return ""
	}
	return string(encoded)
}
