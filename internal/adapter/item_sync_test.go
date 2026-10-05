package adapter

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	itemapp "xianyu-go/internal/application/items"
	"xianyu-go/internal/xianyu/mtop"
)

// TestItemSyncRepositoryUsesListMultiSpecMarkers 验证商品同步只使用列表模型的多规格标记。
func TestItemSyncRepositoryUsesListMultiSpecMarkers(t *testing.T) {
	// store、cleanup 保存隔离数据库及关闭责任。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// logs 收集商品同步日志，用于验证日志明确说明多规格判断来源。
	var logs bytes.Buffer
	// client 提供带有明确多规格标记的列表结果。
	client := &itemSyncListClient{
		allResult: &mtop.ItemListResult{Items: []mtop.ItemListItem{
			{ID: "list-multi", IsMultiSpec: true},
			{ID: "list-single", IsMultiSpec: false},
		}, PageNumber: 1, PageSize: 20, TotalPages: 1},
	}
	// repository 使用列表接口替身执行同步。
	repository := NewItemSyncRepository(store, func() mtop.Client { return client }, slog.New(slog.NewTextHandler(&logs, nil)), nil, nil)
	// owner、ownerErr 保存测试账号归属，供同步接口执行所有权校验。
	owner, ownerErr := store.Users.GetByUsername(context.Background(), "admin")
	if ownerErr != nil {
		t.Fatal(ownerErr)
	}
	// result、syncErr 保存全量同步结果。
	result, syncErr := repository.SyncAll(context.Background(), itemapp.SyncQuery{UserID: owner.ID, CookieID: "cid", PageSize: 20, MaxPages: 1})
	if syncErr != nil || result.TotalCount != 2 || result.SavedCount != 2 {
		t.Fatalf("列表同步异常 result=%+v err=%v", result, syncErr)
	}
	// multi、multiErr 和 single、singleErr 保存同步后的两条商品记录。
	multi, multiErr := store.Items.Get(context.Background(), "cid", "list-multi")
	// single、singleErr 保存列表中普通单规格商品的同步结果及读取错误。
	single, singleErr := store.Items.Get(context.Background(), "cid", "list-single")
	if multiErr != nil || singleErr != nil || !multi.IsMultiSpec || single.IsMultiSpec {
		t.Fatalf("列表多规格标记落库异常 multi=%+v single=%+v multiErr=%v singleErr=%v", multi, single, multiErr, singleErr)
	}
	if !strings.Contains(logs.String(), "列表 isSKU 字段") || !strings.Contains(logs.String(), "全量商品同步完成") {
		t.Fatalf("商品同步日志未说明多规格判断来源：%s", logs.String())
	}
}
