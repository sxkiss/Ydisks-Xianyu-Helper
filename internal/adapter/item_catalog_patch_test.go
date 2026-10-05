package adapter

import (
	"context"
	"errors"
	"testing"
	itemapp "xianyu-go/internal/application/items"
	"xianyu-go/internal/db"
)

// interleavedCatalogRepository 只控制读取完成到写回之间的时序，其余操作使用真实 SQLite 仓储。
type interleavedCatalogRepository struct {
	*ItemCatalogRepository
	// afterRead 在真实读取完成后执行第二个独立请求。
	afterRead func()
}

// Get 先完成真实查询，再让并发请求完成；ctx、accountID、itemID 沿用应用输入，返回原查询快照与错误。
func (r *interleavedCatalogRepository) Get(ctx context.Context, accountID, itemID string) (itemapp.CatalogItem, error) {
	// item、err 是数据库已经读取的商品快照与错误。
	item, err := r.ItemCatalogRepository.Get(ctx, accountID, itemID)
	if err == nil {
		r.afterRead()
	}
	return item, err
}

// TestCatalogPatchPreservesConcurrentQuantity 用真实数据库验证只改标题不会覆盖另一请求已保存的交付开关；t 管理独立临时库。
func TestCatalogPatchPreservesConcurrentQuantity(t *testing.T) {
	// store、cleanup 提供独立 SQLite 库，不接触用户数据。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// ctx 只管理本地数据库调用。
	ctx := context.Background()
	// base 为生产商品仓储适配器。
	base := NewItemCatalogRepository(store)
	// err 检查初始商品持久化结果。
	if err := base.Upsert(ctx, "cid", itemapp.CatalogWriteInput{ItemID: "recheck", ItemTitle: "原标题"}); err != nil {
		t.Fatal(err)
	}
	// repository 在真实读写之间完成真实开关写入。
	repository := &interleavedCatalogRepository{ItemCatalogRepository: base, afterRead: func() {
		// err 检查并发交付开关写入结果。
		if err := base.SetMultiQuantity(ctx, "cid", "recheck", true); err != nil {
			t.Fatal(err)
		}
		// saved、err 验证第二个请求确实已提交 true。
		saved, err := base.Get(ctx, "cid", "recheck")
		if err != nil || !saved.MultiQuantityDelivery {
			t.Fatal("前置开关提交失败")
		}
	}}
	// service、err 为真实商品局部更新服务及构造错误。
	service, err := itemapp.NewCatalogMutationService(repository)
	if err != nil {
		t.Fatal(err)
	}
	// title 是第一个请求唯一提交的补丁字段。
	title := "新标题"
	// err 检查局部更新是否完成。
	if err := service.Update(ctx, "cid", "recheck", itemapp.CatalogPatchInput{ItemTitle: &title}); err != nil {
		t.Fatal(err)
	}
	// saved、readErr 是最终真实数据库记录。
	saved, readErr := base.Get(ctx, "cid", "recheck")
	if readErr != nil {
		t.Fatal(readErr)
	}
	t.Logf("商品更新结果：标题=%s，多数量交付=%v", saved.ItemTitle, saved.MultiQuantityDelivery)
	if !saved.MultiQuantityDelivery {
		t.Fatal("标题更新覆盖了另一请求已提交的交付开关")
	}
}

// TestCatalogPatchRejectsUnavailableAndDeleted 验证适配器初始化防御和读后删除不会被更新复活；t 管理独立数据库。
func TestCatalogPatchRejectsUnavailableAndDeleted(t *testing.T) {
	// candidate 覆盖空接收器、空存储和缺少商品仓储三种构造错误。
	for _, candidate := range []*ItemCatalogRepository{nil, {}, {store: &db.Store{}}} {
		// err 检查未初始化依赖不会触发空指针或伪成功。
		if err := candidate.Patch(context.Background(), "cid", "item", itemapp.CatalogPatchInput{}); err == nil {
			t.Fatal("缺少仓储应返回错误")
		}
	}
	// store、cleanup 提供真实数据库和释放责任。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// base、ctx 提供真实商品仓储和调用生命周期。
	base, ctx := NewItemCatalogRepository(store), context.Background()
	// err 检查初始商品创建。
	if err := base.Upsert(ctx, "cid", itemapp.CatalogWriteInput{ItemID: "deleted"}); err != nil {
		t.Fatal(err)
	}
	// repository 在服务已读取商品后将其软删除。
	repository := &interleavedCatalogRepository{ItemCatalogRepository: base, afterRead: func() {
		// err 是独立删除请求的提交结果。
		if err := base.Delete(ctx, "cid", "deleted"); err != nil {
			t.Fatal(err)
		}
	}}
	// service、err 为真实更新服务与初始化结果。
	service, err := itemapp.NewCatalogMutationService(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Update(ctx, "cid", "deleted", itemapp.CatalogPatchInput{}); !errors.Is(err, itemapp.ErrCatalogNotFound) {
		t.Fatalf("读后删除应归一为不存在：%v", err)
	}
	if _, err = base.Get(ctx, "cid", "deleted"); !errors.Is(err, itemapp.ErrCatalogNotFound) {
		t.Fatalf("已删除商品被恢复：%v", err)
	}
	// canceled、cancel 模拟数据库执行前取消，错误应透传到应用层。
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = base.Patch(canceled, "cid", "deleted", itemapp.CatalogPatchInput{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误未透传：%v", err)
	}
}

// TestCatalogPatchAllExplicitFields 验证应用补丁经适配器映射后各字段仍对应正确列；t 管理独立 SQLite。
func TestCatalogPatchAllExplicitFields(t *testing.T) {
	// store、cleanup 提供独立数据库和清理责任。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// base、ctx 为生产适配器及本地调用生命周期。
	base, ctx := NewItemCatalogRepository(store), context.Background()
	// err 检查空商品夹具的创建。
	if err := base.Upsert(ctx, "cid", itemapp.CatalogWriteInput{ItemID: "all-fields"}); err != nil {
		t.Fatal(err)
	}
	// expected、err 提取自动生成的标识，其他字段稍后填入明确期望值。
	expected, err := base.Get(ctx, "cid", "all-fields")
	if err != nil {
		t.Fatal(err)
	}
	expected.ItemTitle, expected.ItemDescription, expected.ItemCategory, expected.ItemPrice, expected.ItemDetail = "新标题", "新描述", "新类目", "20.50", "新详情"
	expected.IsMultiSpec, expected.MultiQuantityDelivery = true, true
	// service、err 构造真实应用层，确保没有绕过补丁透传。
	service, err := itemapp.NewCatalogMutationService(base)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Update(ctx, "cid", "all-fields", itemapp.CatalogPatchInput{ItemTitle: &expected.ItemTitle, ItemDescription: &expected.ItemDescription, ItemCategory: &expected.ItemCategory, ItemPrice: &expected.ItemPrice, ItemDetail: &expected.ItemDetail, IsMultiSpec: &expected.IsMultiSpec, MultiQuantityDelivery: &expected.MultiQuantityDelivery}); err != nil {
		t.Fatal(err)
	}
	// actual、err 是数据库实际保存并重新读取的应用模型。
	actual, err := base.Get(ctx, "cid", "all-fields")
	if err != nil || actual != expected {
		t.Fatalf("补丁字段映射错误：实际=%+v 期望=%+v 错误=%v", actual, expected, err)
	}
}
