package adapter

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"xianyu-go/internal/xianyu/mtop"
)

// coordinatedOrderDetailFake 是订单详情协调器测试使用的平台替身；calls 统计实际进入平台边界的次数。
type coordinatedOrderDetailFake struct {
	// calls 统计 FetchOrderDetail 被协调器实际调用的次数。
	calls atomic.Int32
	// started 在每次实际平台请求开始时通知测试。
	started chan struct{}
	// release 控制平台请求何时返回；nil 时请求立即成功。
	release <-chan struct{}
	// startedAt 保存每次平台请求开始的墙钟时间，仅由测试在请求结束后读取。
	startedAt chan time.Time
}

// FetchOrderDetail 记录一次平台访问，并按测试控制的阻塞信号返回固定订单详情。
func (f *coordinatedOrderDetailFake) FetchOrderDetail(ctx context.Context, _, _ string) (*mtop.OrderDetailResult, error) {
	f.calls.Add(1)
	if f.startedAt != nil {
		f.startedAt <- time.Now()
	}
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &mtop.OrderDetailResult{Quantity: "1", Amount: "9.90"}, nil
}

// TestOrderDetailCoordinatorCoalescesSameOrder 验证同账号同订单的并发调用只访问一次平台，并向等待者返回独立结果副本。
func TestOrderDetailCoordinatorCoalescesSameOrder(t *testing.T) {
	// coordinator 使用零等待的确定性策略，隔离本测试对同订单并发去重的验证。
	coordinator := newOrderDetailCoordinator(0, 0, nil)
	// release 控制首个请求保持在途，确保第二个请求必然进入复用分支。
	release := make(chan struct{})
	// fetcher 记录实际平台调用次数并阻塞首个请求。
	fetcher := &coordinatedOrderDetailFake{started: make(chan struct{}, 1), release: release}
	// firstResult 保存首个调用收到的订单详情。
	var firstResult *mtop.OrderDetailResult
	// firstErr 保存首个调用收到的平台或协调错误。
	var firstErr error
	// firstDone 仅在首个调用完成后关闭。
	firstDone := make(chan struct{})
	go func() {
		// result、err 保存首个调用从协调器返回的平台事实及错误。
		result, err := coordinator.Fetch(context.Background(), "account-a", "order-1", "cookie", fetcher)
		firstResult, firstErr = result, err
		close(firstDone)
	}()
	select {
	case <-fetcher.started:
	case <-time.After(time.Second):
		t.Fatal("首个订单详情请求未开始")
	}
	// secondResult 保存并发等待者复用首个请求后的订单详情。
	var secondResult *mtop.OrderDetailResult
	// secondErr 保存等待或复用时产生的错误。
	var secondErr error
	// secondDone 仅在等待者收到首个请求终态后关闭。
	secondDone := make(chan struct{})
	go func() {
		// result、err 保存第二个调用复用的订单事实及错误。
		result, err := coordinator.Fetch(context.Background(), "account-a", "order-1", "cookie", fetcher)
		secondResult, secondErr = result, err
		close(secondDone)
	}()
	select {
	case <-secondDone:
		t.Fatal("在途订单详情尚未完成，等待者不应提前返回")
	case <-time.After(30 * time.Millisecond):
	}
	if fetcher.calls.Load() != 1 {
		t.Fatalf("同订单并发请求次数=%d，期望 1", fetcher.calls.Load())
	}
	close(release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("首个订单详情请求未完成")
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("同订单等待者未收到复用结果")
	}
	if firstErr != nil || secondErr != nil || firstResult == nil || secondResult == nil || firstResult == secondResult {
		t.Fatalf("同订单结果复用异常 first=%+v firstErr=%v second=%+v secondErr=%v", firstResult, firstErr, secondResult, secondErr)
	}
}

// TestOrderDetailCoordinatorReusesRecentSuccess 验证成功详情在短暂窗口内按订单复用，重复 WS 投递不会再次访问平台。
func TestOrderDetailCoordinatorReusesRecentSuccess(t *testing.T) {
	// coordinator 使用零限流和默认成功缓存窗口，隔离本测试对顺序重复事件的验证。
	coordinator := newOrderDetailCoordinator(0, 0, nil)
	// fetcher 统计真正进入平台边界的次数。
	fetcher := &coordinatedOrderDetailFake{}
	// firstResult、firstErr 保存首次订单详情读取结果和错误。
	firstResult, firstErr := coordinator.Fetch(context.Background(), "account-a", "order-1", "cookie", fetcher)
	if firstErr != nil || firstResult == nil {
		t.Fatalf("首次订单详情失败 result=%+v err=%v", firstResult, firstErr)
	}
	// secondResult、secondErr 保存同一订单短暂重复事件复用的结果和错误。
	secondResult, secondErr := coordinator.Fetch(context.Background(), "account-a", "order-1", "cookie", fetcher)
	if secondErr != nil || secondResult == nil || firstResult == secondResult {
		t.Fatalf("成功详情缓存复用异常 first=%+v second=%+v err=%v", firstResult, secondResult, secondErr)
	}
	if fetcher.calls.Load() != 1 {
		t.Fatalf("顺序重复订单请求次数=%d，期望 1", fetcher.calls.Load())
	}
}

// TestOrderDetailCoordinatorSpacesSameAccountRequests 验证同账号不同订单至少按配置间隔访问平台。
func TestOrderDetailCoordinatorSpacesSameAccountRequests(t *testing.T) {
	// minimumGap 是本测试要求的同账号最小请求间隔。
	minimumGap := 35 * time.Millisecond
	// coordinator 使用固定零抖动，确保时间断言不依赖随机数。
	coordinator := newOrderDetailCoordinator(minimumGap, 0, nil)
	// fetcher 记录两次平台访问真正开始的时间。
	fetcher := &coordinatedOrderDetailFake{startedAt: make(chan time.Time, 2)}
	// firstResult、firstErr 保存首笔订单详情结果和错误。
	firstResult, firstErr := coordinator.Fetch(context.Background(), "account-a", "order-1", "cookie", fetcher)
	if firstErr != nil || firstResult == nil {
		t.Fatalf("首笔订单详情失败 result=%+v err=%v", firstResult, firstErr)
	}
	// secondResult、secondErr 保存第二笔订单详情结果和错误。
	secondResult, secondErr := coordinator.Fetch(context.Background(), "account-a", "order-2", "cookie", fetcher)
	if secondErr != nil || secondResult == nil {
		t.Fatalf("第二笔订单详情失败 result=%+v err=%v", secondResult, secondErr)
	}
	// firstStarted、secondStarted 分别是两次实际平台访问的开始时刻。
	firstStarted, secondStarted := <-fetcher.startedAt, <-fetcher.startedAt
	// elapsed 是两次实际平台请求起点的间隔，用于容忍调度误差后验证限流下界。
	if elapsed := secondStarted.Sub(firstStarted); elapsed < minimumGap-5*time.Millisecond {
		t.Fatalf("同账号请求间隔=%s，小于最低间隔=%s", elapsed, minimumGap)
	}
}

// TestOrderDetailCoordinatorDoesNotBlockOtherAccounts 验证一个账号的限流或慢请求不会阻塞其他账号。
func TestOrderDetailCoordinatorDoesNotBlockOtherAccounts(t *testing.T) {
	// coordinator 对每个账号独立排队，使用很长间隔突出跨账号隔离行为。
	coordinator := newOrderDetailCoordinator(time.Minute, 0, nil)
	// release 控制账号 A 的慢平台请求。
	release := make(chan struct{})
	// accountAFetcher 为账号 A 提供阻塞的平台请求。
	accountAFetcher := &coordinatedOrderDetailFake{started: make(chan struct{}, 1), release: release}
	// accountBFetcher 为账号 B 提供立即返回的平台请求。
	accountBFetcher := &coordinatedOrderDetailFake{started: make(chan struct{}, 1)}
	// accountADone 仅在账号 A 的平台请求释放后关闭。
	accountADone := make(chan struct{})
	go func() {
		// result、err 丢弃账号 A 的固定成功结果；测试只验证其慢请求不会阻塞账号 B。
		_, _ = coordinator.Fetch(context.Background(), "account-a", "order-1", "cookie", accountAFetcher)
		close(accountADone)
	}()
	select {
	case <-accountAFetcher.started:
	case <-time.After(time.Second):
		t.Fatal("账号 A 请求未开始")
	}
	// accountBResult、accountBErr 保存账号 B 不应被账号 A 阻塞的结果和错误。
	accountBResult, accountBErr := coordinator.Fetch(context.Background(), "account-b", "order-1", "cookie", accountBFetcher)
	if accountBErr != nil || accountBResult == nil {
		t.Fatalf("账号 B 被错误阻塞或请求失败 result=%+v err=%v", accountBResult, accountBErr)
	}
	select {
	case <-accountBFetcher.started:
	default:
		t.Fatal("账号 B 未独立进入平台请求")
	}
	close(release)
	select {
	case <-accountADone:
	case <-time.After(time.Second):
		t.Fatal("账号 A 请求未在释放后完成")
	}
}
