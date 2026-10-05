package automation

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"xianyu-go/internal/xianyu/cookierefresh"
)

// TestTaskCookieRejectsConcurrentCredentialUpdate 验证完整与扁平会话都不能覆盖请求期间完成的续期。
func TestTaskCookieRejectsConcurrentCredentialUpdate(t *testing.T) {
	// complete 区分带完整域属性的账号和旧扁平凭证账号。
	for _, complete := range []bool{true, false} {
		// store、cleanup 提供独立账号数据和释放函数。
		store, cleanup := newAutomationTestStore(t)
		// ctx 控制本轮确定性数据库调用。
		ctx := context.Background()
		// metadata 仅包含虚构凭证，用于覆盖两种账号存储形态。
		metadata := ""
		if complete {
			metadata = cookierefresh.MetadataWithSnapshot("", []cookierefresh.BrowserCookie{{Name: "_m_h5_tk", Value: "old_test", Domain: ".goofish.com", Path: "/"}})
		}
		// err 保存本轮虚构凭证持久化失败原因。
		if err := store.Cookies.UpdateRenewalCookie(ctx, "cid", "_m_h5_tk=old_test", metadata, 1); err != nil {
			t.Fatal(err)
		}
		// sender 记录冲突路径是否错误通知运行时。
		sender := &testSender{}
		// coordinator 使用生产存储及账号凭证锁。
		coordinator := &accountTaskCoordinator{repository: newStoreAccountTaskRepository(store), senders: testSenderProvider{sender: sender}, logger: slog.Default()}
		// credential、err 捕获外部请求开始时的版本。
		credential, err := coordinator.openAccountTaskCredentialSession(ctx, "cid")
		if err != nil {
			t.Fatal(err)
		}
		if complete {
			credential.cookieSession.ReplaceSnapshot([]cookierefresh.BrowserCookie{{Name: "_m_h5_tk", Value: "old_test", Domain: ".goofish.com", Path: "/"}, {Name: "ordinary", Value: "response", Domain: ".goofish.com", Path: "/"}})
		}
		// err 保存本轮虚构凭证持久化失败原因。
		if err := store.Cookies.UpdateRenewalCookie(ctx, "cid", "_m_h5_tk=new_test", "", 2); err != nil {
			t.Fatal(err)
		}
		// err 必须报告旧请求与数据库凭证版本冲突。
		if _, err := coordinator.persistTaskCookieSession(ctx, "cid", credential.cookieValue, "_m_h5_tk=old_test; ordinary=response", &credential); err == nil {
			t.Fatal("并发凭证更新必须拒绝旧响应写回")
		}
		// stored、err 验证新凭证保持不变，且没有错误通知运行时。
		stored, err := store.Cookies.GetCookieRuntimeData(ctx, "cid")
		if err != nil || stored.Value != "_m_h5_tk=new_test" || len(sender.cookieUpdates) != 0 {
			t.Fatal("旧任务覆盖了新凭证或通知了旧 Cookie")
		}
		cleanup()
	}
}

// TestTaskCookieUnlocksBeforeRuntimeNotification 验证运行时重新申请账号锁不会死锁，且本轮多次写回可继续使用更新后的版本。
func TestTaskCookieUnlocksBeforeRuntimeNotification(t *testing.T) {
	// store、cleanup 提供真实账号锁与隔离存储。
	store, cleanup := newAutomationTestStore(t)
	defer cleanup()
	// ctx 控制测试数据库操作。
	ctx := context.Background()
	// sender 复现 engine.UpdateCookie 内部的同账号锁重取。
	sender := &testSender{onCookieUpdate: func(string) {
		// unlock 在运行时回调返回前释放重取的账号锁。
		unlock := store.LockAccountCredentials("cid")
		defer unlock()
	}}
	// coordinator 通过生产仓储适配器执行凭证写回。
	coordinator := &accountTaskCoordinator{repository: newStoreAccountTaskRepository(store), senders: testSenderProvider{sender: sender}, logger: slog.Default()}
	// credential、err 保存读取的原始持久化版本。
	credential, err := coordinator.openAccountTaskCredentialSession(ctx, "cid")
	if err != nil {
		t.Fatal(err)
	}
	// done 由唯一写回协程发送执行结果，测试等待其结束后才关闭数据库。
	done := make(chan error, 1)
	go func() {
		// index、value 是同一任务内的两次值更新、一次仅属性变化及一次相同快照重放；重放不能重复通知运行时。
		for index, value := range []string{"first_test", "second_test", "second_test", "second_test"} {
			credential.cookieSession.ReplaceSnapshot([]cookierefresh.BrowserCookie{{Name: "unb", Value: value, Domain: ".goofish.com", Path: "/", HTTPOnly: index >= 2}})
			// updated、saveErr 保存写回值及失败原因；下次写回必须以本次已提交版本为基准。
			updated, saveErr := coordinator.persistTaskCookieSession(ctx, "cid", credential.cookieValue, "", &credential)
			if saveErr != nil {
				done <- saveErr
				return
			}
			credential.cookieValue = updated
		}
		done <- nil
	}()
	select {
	case err := <-done: // err 保存两个写回阶段的最终错误。
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("运行时通知重入账号凭证锁导致写回卡住")
	}
	if len(sender.cookieUpdates) != 3 || !strings.Contains(sender.cookieUpdates[1], "second_test") {
		t.Fatal("连续写回未通知最新凭证")
	}
}

// TestRunAccountTaskUninitialized 验证空协调器返回明确错误而不是解引用空对象。
func TestRunAccountTaskUninitialized(t *testing.T) {
	// center 覆盖空 facade 和未装配任务 runner 的实例。
	for _, center := range []*Center{nil, {}} {
		// err 必须明确报告任务协调器未装配。
		if _, err := center.RunAccountTask(context.Background(), "cid", TaskAutoRate); err == nil {
			t.Fatal("未初始化协调器必须拒绝运行")
		}
	}
}

// taskCredentialFailingWriter 模拟完整快照仓储在提交前失败，其他读取委托给测试仓储。
type taskCredentialFailingWriter struct {
	// AccountTaskRepository 提供请求开始与写回前的凭证读取。
	AccountTaskRepository
	// writeErr 是可由调用方验证的持久化失败哨兵。
	writeErr error
}

// UpdateRenewalCookie 返回预置存储错误，拒绝更新调用方的已持久化版本。
func (writer taskCredentialFailingWriter) UpdateRenewalCookie(context.Context, string, string, string, int64) error {
	return writer.writeErr
}

// TestTaskCookiePersistenceFailures 验证写回前读取及两种仓储写入失败都保留原版本，不通知运行时。
func TestTaskCookiePersistenceFailures(t *testing.T) {
	// failure 是三种失败边界共用的可识别错误。
	failure := errors.New("合成凭证存储失败")
	// mode 区分重读失败、旧仓储写入失败和完整快照写入失败。
	for _, mode := range []string{"read", "flat-write", "snapshot-write"} {
		// repository 提供稳定原始版本和可控失败。
		repository := &accountTaskFlowRepository{value: "unb=old"}
		// sender 用于断言失败路径没有发出运行时同步。
		sender := &testSender{}
		// coordinator 持有当前边界场景仓储。
		coordinator := &accountTaskCoordinator{repository: repository, senders: testSenderProvider{sender: sender}, logger: slog.Default()}
		// credential、err 捕获原始版本，随后只修改响应状态。
		credential, err := coordinator.openAccountTaskCredentialSession(context.Background(), "cid")
		if err != nil {
			t.Fatal(err)
		}
		switch mode {
		case "read":
			repository.runtimeDataErr = failure
		case "flat-write":
			repository.updateErr = failure
		case "snapshot-write":
			coordinator.repository = taskCredentialFailingWriter{AccountTaskRepository: repository, writeErr: failure}
			credential.cookieSession.ReplaceSnapshot([]cookierefresh.BrowserCookie{{Name: "unb", Value: "new", Domain: ".goofish.com", Path: "/"}})
		}
		// value、saveErr 保存失败结果，调用方必须继续看到原版本而不是未提交响应。
		value, saveErr := coordinator.persistTaskCookieSession(context.Background(), "cid", "unb=old", "unb=new", &credential)
		if !errors.Is(saveErr, failure) || value != "unb=old" || credential.persistedValue != "" || len(sender.cookieUpdates) != 0 {
			t.Fatalf("%s 未保留失败边界", mode)
		}
	}
}
