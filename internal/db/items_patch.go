package db

import "context"

// ItemPatch 描述商品原子局部写入；nil 保留当前数据库值，空字符串和 false 均为显式更新。
type ItemPatch struct {
	// ItemTitle 是用户提交的商品标题。
	ItemTitle *string
	// ItemDescription 是用户提交的商品描述。
	ItemDescription *string
	// ItemCategory 是用户提交的类目标识。
	ItemCategory *string
	// ItemPrice 是用户提交的价格文本。
	ItemPrice *string
	// ItemDetail 是用户提交的扩展详情。
	ItemDetail *string
	// IsMultiSpec 是用户明确提交的多规格交付开关。
	IsMultiSpec *bool
	// MultiQuantityDelivery 是用户明确提交的多数量交付开关。
	MultiQuantityDelivery *bool
}

// Patch 在 ctx 内仅修改 cookieID/itemID 的显式 patch 字段；单条 UPDATE 保留并发更新的其他字段，不插入或恢复已删除记录。
// 返回 ErrNotFound 表示目标已不存在，其余数据库错误原样返回；可与商品开关写入并发调用。
func (i *Items) Patch(ctx context.Context, cookieID, itemID string, patch ItemPatch) error {
	// result、err 保存原子字段更新的影响行数和数据库错误；所有方言的布尔列均使用整数绑定。
	result, err := i.DB.ExecContext(ctx, `UPDATE item_info SET
 item_title=COALESCE(?,item_title),item_description=COALESCE(?,item_description),
 item_category=COALESCE(?,item_category),item_price=COALESCE(?,item_price),item_detail=COALESCE(?,item_detail),
 is_multi_spec=COALESCE(?,is_multi_spec),multi_quantity_delivery=COALESCE(?,multi_quantity_delivery),updated_at=CURRENT_TIMESTAMP
 WHERE cookie_id=? AND item_id=? AND deleted_at IS NULL`,
		patch.ItemTitle, patch.ItemDescription, patch.ItemCategory, patch.ItemPrice, patch.ItemDetail,
		itemPatchBool(patch.IsMultiSpec), itemPatchBool(patch.MultiQuantityDelivery), cookieID, itemID)
	if err != nil {
		return err
	}
	// affected、err 区分实际匹配与未变化的 MySQL 更新结果。
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 0 {
		return nil
	}
	// readErr 对零影响行数复核存在性，避免把相同字段重复保存误报为不存在。
	_, readErr := i.Get(ctx, cookieID, itemID)
	return readErr
}

// itemPatchBool 将 value 的显式布尔值转为跨方言整数；nil 表示 SQL 中保留原列值。
func itemPatchBool(value *bool) any {
	if value == nil {
		return nil
	}
	return boolToInt(*value)
}
