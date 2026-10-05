package mtop

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// PublishSpecValue 是发布请求中的单个规格值及其可选图片索引。
type PublishSpecValue struct {
	// Value 是展示给买家的规格值文本。
	Value string
	// ImageIndex 是规格图片在 PublishItemRequest.SpecImages 中的下标，-1 表示没有图片。
	ImageIndex int
}

// PublishSpec 是闲鱼官方 itemProperties 结构中的一个规格维度。
type PublishSpec struct {
	// PropertyName 是闲鱼规格名称。
	PropertyName string
	// SupportImage 表示规格值是否允许绑定图片。
	SupportImage bool
	// Values 保存该维度下的规格值。
	Values []PublishSpecValue
}

// PublishSKUProperty 是闲鱼官方 SKU propertyList 中的规格名称和值对。
type PublishSKUProperty struct {
	// PropertyText 是 SKU 规格名称。
	PropertyText string
	// ValueText 是 SKU 规格值。
	ValueText string
}

// PublishSKU 是闲鱼官方 itemSkuList 中的一个规格组合。
type PublishSKU struct {
	// PriceCents 是该 SKU 的售价，单位为分。
	PriceCents int64
	// Quantity 是该 SKU 的库存数量。
	Quantity int
	// PropertyList 保存该 SKU 的规格组合。
	PropertyList []PublishSKUProperty
}

// validatePublishSKUs 校验多规格请求的维度、组合数量、逐行价格库存和图片索引。
func validatePublishSKUs(specs []PublishSpec, skus []PublishSKU, imageCount int) error {
	if len(specs) == 0 {
		if len(skus) > 0 || imageCount > 0 {
			return errors.New("单规格商品不能包含 SKU 规格数据")
		}
		return nil
	}
	if len(specs) > 2 {
		return errors.New("商品规格最多支持 2 个规格类型")
	}
	// combinationCount 保存规格值笛卡尔积的组合数量。
	combinationCount := 1
	// names 保存已出现的规格名称，防止官方属性列表产生歧义。
	names := make(map[string]struct{}, len(specs))
	// valueSets 保存每个规格维度允许出现的规格值，用于核对 SKU 组合引用。
	valueSets := make([]map[string]struct{}, len(specs))
	// spec 表示当前待校验的规格维度；specIndex 表示其在规格列表中的位置。
	for specIndex, spec := range specs {
		// name 保存去除首尾空白后的规格名称。
		name := strings.TrimSpace(spec.PropertyName)
		if name == "" {
			return errors.New("规格类型不能为空")
		}
		// exists 表示当前规格名称是否已经在前面的维度中出现。
		if _, exists := names[name]; exists {
			return errors.New("规格类型不能重复")
		}
		names[name] = struct{}{}
		// values 保存当前维度已经出现的规格值文本。
		values := make(map[string]struct{}, len(spec.Values))
		// validValues 保存当前维度非空且不重复的规格值数量。
		validValues := 0
		// value 表示当前待校验的规格值。
		for _, value := range spec.Values {
			// text 保存去除首尾空白后的规格值文本。
			text := strings.TrimSpace(value.Value)
			if text == "" {
				continue
			}
			// exists 表示当前规格值是否在该维度中重复。
			if _, exists := values[text]; exists {
				return fmt.Errorf("规格 %q 的规格值不能重复", name)
			}
			values[text] = struct{}{}
			validValues++
			if value.ImageIndex < -1 || value.ImageIndex >= imageCount {
				return errors.New("规格图片索引无效")
			}
		}
		valueSets[specIndex] = values
		if validValues < 2 {
			return fmt.Errorf("规格 %q 至少需要 2 个规格值", name)
		}
		combinationCount *= validValues
	}
	if combinationCount > 1500 {
		return errors.New("规格组合数量不能超过 1500")
	}
	if len(skus) != combinationCount {
		return errors.New("SKU 组合数量与规格值不匹配")
	}
	// skuKeys 保存已经出现的组合键，防止重复 SKU 掩盖缺失组合。
	skuKeys := make(map[string]struct{}, len(skus))
	// sku 表示当前待校验的 SKU 组合。
	for _, sku := range skus {
		if sku.PriceCents <= 0 {
			return errors.New("SKU 价格必须大于 0")
		}
		if sku.Quantity <= 0 {
			return errors.New("SKU 库存必须大于 0")
		}
		if len(sku.PropertyList) != len(specs) {
			return errors.New("SKU 规格组合不完整")
		}
		// propertyKeyParts 保存当前 SKU 各维度的规范化组合键片段。
		propertyKeyParts := make([]string, 0, len(sku.PropertyList))
		// index、property 分别表示规格在维度列表中的位置和对应的名称值对。
		for index, property := range sku.PropertyList {
			// valueText 保存当前 SKU 引用的规范化规格值。
			valueText := strings.TrimSpace(property.ValueText)
			if strings.TrimSpace(property.PropertyText) != strings.TrimSpace(specs[index].PropertyName) || valueText == "" {
				return errors.New("SKU 规格组合与规格定义不匹配")
			}
			// exists 表示当前 SKU 规格值是否属于对应维度定义。
			if _, exists := valueSets[index][valueText]; !exists {
				return errors.New("SKU 规格组合与规格定义不匹配")
			}
			propertyKeyParts = append(propertyKeyParts, valueText)
		}
		// skuKey 标识当前 SKU 的规格值组合。
		skuKey := strings.Join(propertyKeyParts, "\x00")
		// exists 表示当前规格组合是否已经被前面的 SKU 使用。
		if _, exists := skuKeys[skuKey]; exists {
			return errors.New("SKU 规格组合不能重复")
		}
		skuKeys[skuKey] = struct{}{}
	}
	return nil
}

// publishSKUQuantity 汇总多规格商品的 SKU 库存，供平台顶层 quantity 字段兼容使用。
func publishSKUQuantity(skus []PublishSKU) int {
	// total 保存所有 SKU 库存之和，供顶层兼容字段使用。
	total := 0
	// sku 表示当前待汇总库存的 SKU 组合。
	for _, sku := range skus {
		total += sku.Quantity
	}
	return total
}

// publishPriceDTO 组装闲鱼官方 itemPriceDTO；多规格成交价由 itemSkuList 逐行提供。
func publishPriceDTO(req PublishItemRequest) map[string]any {
	// out 保存官方 itemPriceDTO 的字段；多规格时省略单一成交价。
	out := map[string]any{}
	if len(req.SKUs) == 0 {
		out["priceInCent"] = strconv.FormatInt(req.PriceCents, 10)
	}
	if req.OriginalPriceCents > 0 {
		out["origPriceInCent"] = strconv.FormatInt(req.OriginalPriceCents, 10)
	}
	return out
}

// publishPropertiesPayload 将应用规格转换为闲鱼官方 itemProperties 结构。
func publishPropertiesPayload(specs []PublishSpec, specImages []uploadedImage) []any {
	// properties 保存所有规格维度及其值的官方 JSON 结构。
	properties := make([]any, 0, len(specs))
	// spec 表示当前待转换的规格维度。
	for _, spec := range specs {
		// values 保存当前规格维度的官方规格值结构。
		values := make([]any, 0, len(spec.Values))
		// value 表示当前规格维度中的一个规格值。
		for _, value := range spec.Values {
			// propertyValue 保存官方字段名和规格图片元数据。
			propertyValue := map[string]any{"propertyValue": strings.TrimSpace(value.Value)}
			if value.ImageIndex >= 0 && value.ImageIndex < len(specImages) {
				propertyValue["propertyValueImg"] = publishPropertyImagePayload(specImages[value.ImageIndex])
			}
			values = append(values, propertyValue)
		}
		properties = append(properties, map[string]any{"propertyName": strings.TrimSpace(spec.PropertyName), "supportImage": spec.SupportImage, "propertyValues": values})
	}
	return properties
}

// publishSKUListPayload 将逐行 SKU 转换为闲鱼官方 itemSkuList 结构。
func publishSKUListPayload(skus []PublishSKU) []any {
	// items 保存所有 SKU 行的官方 JSON 结构。
	items := make([]any, 0, len(skus))
	// sku 表示当前待转换的 SKU 组合。
	for _, sku := range skus {
		// propertyList 保存当前 SKU 的规格名称和值对。
		propertyList := make([]any, 0, len(sku.PropertyList))
		// property 表示当前 SKU 中的一组规格名称和值。
		for _, property := range sku.PropertyList {
			propertyList = append(propertyList, map[string]any{"propertyText": property.PropertyText, "valueText": property.ValueText})
		}
		items = append(items, map[string]any{"priceInCent": strconv.FormatInt(sku.PriceCents, 10), "quantity": sku.Quantity, "propertyList": propertyList})
	}
	return items
}

// publishPropertyImageList 将规格值图片转换为闲鱼官方 propertyImageList 结构。
func publishPropertyImageList(specs []PublishSpec, specImages []uploadedImage) []any {
	// images 保存规格值到已上传图片 URL 的官方映射列表。
	images := make([]any, 0)
	// spec 表示当前待转换的规格维度。
	for _, spec := range specs {
		// value 表示当前规格维度中的一个规格值。
		for _, value := range spec.Values {
			if value.ImageIndex < 0 || value.ImageIndex >= len(specImages) || strings.TrimSpace(value.Value) == "" {
				continue
			}
			images = append(images, map[string]any{
				"property": map[string]any{"propertyText": spec.PropertyName, "valueText": value.Value},
				"url":      specImages[value.ImageIndex].URL,
			})
		}
	}
	return images
}

// publishPropertyImagePayload 将规格图片上传结果转换为官方表单中的图片对象。
func publishPropertyImagePayload(image uploadedImage) map[string]any {
	return map[string]any{
		"url": image.URL, "thumbnail": image.URL, "status": "done", "major": false,
		"widthSize": image.Width, "heightSize": image.Height, "type": 0,
		"extraInfo": map[string]any{"isH": "false", "isT": "false", "raw": "false"},
		"isQrCode":  false, "labels": []any{}, "templateIndex": "0",
	}
}
