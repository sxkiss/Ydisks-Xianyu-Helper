package server

import (
	"encoding/json"
	"errors"
	"strings"

	itemapp "xianyu-go/internal/application/items"
)

// itemPublishSpecRequest 是 multipart JSON 中的闲鱼规格维度 DTO。
type itemPublishSpecRequest struct {
	// PropertyName 是闲鱼规格名称。
	PropertyName string `json:"propertyName"`
	// SupportImage 表示规格值是否允许绑定图片。
	SupportImage bool `json:"supportImage"`
	// PropertyValues 保存规格值 DTO。
	PropertyValues []itemPublishSpecValueRequest `json:"propertyValues"`
}

// itemPublishSpecValueRequest 是 multipart JSON 中的规格值 DTO。
type itemPublishSpecValueRequest struct {
	// PropertyValue 是展示给买家的规格值文本。
	PropertyValue string `json:"propertyValue"`
	// ImageIndex 是规格图片在 spec_images 字段中的下标；缺省值表示没有图片。
	ImageIndex *int `json:"image_index"`
}

// itemPublishSKURequest 是 multipart JSON 中的 SKU 组合 DTO。
type itemPublishSKURequest struct {
	// Price 是 SKU 价格文本，单位为元。
	Price string `json:"price"`
	// Quantity 是 SKU 库存数量。
	Quantity int `json:"quantity"`
	// PropertyList 保存 SKU 的规格名称和值对。
	PropertyList []itemPublishSKUPropertyRequest `json:"propertyList"`
}

// itemPublishSKUPropertyRequest 是 multipart JSON 中的 SKU 规格对 DTO。
type itemPublishSKUPropertyRequest struct {
	// PropertyText 是 SKU 规格名称。
	PropertyText string `json:"propertyText"`
	// ValueText 是 SKU 规格值。
	ValueText string `json:"valueText"`
}

// parseItemPublishSpecs 将前端规格 JSON 转换为应用模型，并校验图片引用与价格格式。
func parseItemPublishSpecs(rawProperties, rawSKUs string, specImageCount int) ([]itemapp.PublishSpec, []itemapp.PublishSKU, error) {
	if strings.TrimSpace(rawProperties) == "" && strings.TrimSpace(rawSKUs) == "" {
		return nil, nil, nil
	}
	if strings.TrimSpace(rawProperties) == "" || strings.TrimSpace(rawSKUs) == "" {
		return nil, nil, errors.New("商品规格配置不完整")
	}
	// propertyRequests、skuRequests 保存传输层解码后的规格和 SKU DTO。
	var propertyRequests []itemPublishSpecRequest
	// skuRequests 保存传输层解码后的 SKU DTO，价格仍保持前端的小数元字符串。
	var skuRequests []itemPublishSKURequest
	// err 表示规格维度 JSON 的解码错误。
	if err := json.Unmarshal([]byte(rawProperties), &propertyRequests); err != nil {
		return nil, nil, errors.New("商品规格格式错误")
	}
	// err 表示 SKU 列表 JSON 的解码错误。
	if err := json.Unmarshal([]byte(rawSKUs), &skuRequests); err != nil {
		return nil, nil, errors.New("商品 SKU 格式错误")
	}
	// specs 保存转换后的应用规格维度。
	specs := make([]itemapp.PublishSpec, 0, len(propertyRequests))
	// property 表示当前解码后的规格维度 DTO。
	for _, property := range propertyRequests {
		// values 保存当前维度的有效规格值。
		values := make([]itemapp.PublishSpecValue, 0, len(property.PropertyValues))
		// value 表示当前规格维度中的一个规格值 DTO。
		for _, value := range property.PropertyValues {
			// valueText 保存去除首尾空白后的规格值文本。
			valueText := strings.TrimSpace(value.PropertyValue)
			if valueText == "" {
				continue
			}
			// imageIndex 保存缺省为 -1 的规格图片索引。
			imageIndex := -1
			if value.ImageIndex != nil {
				imageIndex = *value.ImageIndex
			}
			if imageIndex < -1 || imageIndex >= specImageCount {
				return nil, nil, errors.New("商品规格图片引用无效")
			}
			values = append(values, itemapp.PublishSpecValue{Value: valueText, ImageIndex: imageIndex})
		}
		specs = append(specs, itemapp.PublishSpec{PropertyName: strings.TrimSpace(property.PropertyName), SupportImage: property.SupportImage, Values: values})
	}
	// skus 保存转换后的应用 SKU 组合。
	skus := make([]itemapp.PublishSKU, 0, len(skuRequests))
	// sku 表示当前解码后的 SKU DTO。
	for _, sku := range skuRequests {
		// priceCents 保存 SKU 售价的分值。
		priceCents, err := parseMoneyCents(sku.Price)
		if err != nil {
			return nil, nil, errors.New("SKU 价格格式错误")
		}
		// properties 保存当前 SKU 的规格名称和值对。
		properties := make([]itemapp.PublishSKUProperty, 0, len(sku.PropertyList))
		// property 表示当前 SKU 中的一组规格名称和值 DTO。
		for _, property := range sku.PropertyList {
			properties = append(properties, itemapp.PublishSKUProperty{PropertyText: strings.TrimSpace(property.PropertyText), ValueText: strings.TrimSpace(property.ValueText)})
		}
		skus = append(skus, itemapp.PublishSKU{PriceCents: priceCents, Quantity: sku.Quantity, PropertyList: properties})
	}
	return specs, skus, nil
}
