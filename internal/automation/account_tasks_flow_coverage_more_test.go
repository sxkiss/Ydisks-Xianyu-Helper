package automation

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"xianyu-go/internal/db"
	"xianyu-go/internal/xianyu/mtop"
)

// accountTaskFlowRepository 是账号任务流程测试使用的可控内存仓储。
type accountTaskFlowRepository struct {
	// AccountTaskRepository 嵌入流程未涉及方法的默认接口实现。
	AccountTaskRepository
	// value、valueErr 保存 Cookie 读取结果和错误。
	value    string
	valueErr error
	// dueOrderIDs、dueOrderIDsErr 保存本地买家确认收货事实筛选出的订单及读取错误。
	dueOrderIDs    []string
	dueOrderIDsErr error
	// updateErr 保存外部动作返回 Cookie 的持久化错误。
	updateErr error
	// claimed、claimErr 保存周期任务抢占结果和错误。
	claimed  bool
	claimErr error
	// immediateClaimed、immediateClaimErr 保存人工立即执行抢占结果和错误。
	immediateClaimed  bool
	immediateClaimErr error
	// finishErrors 按调用顺序保存任务终态写入错误。
	finishErrors []error
	// markRateErr、markPolishErr 保存扫描时间和擦亮日期写入错误。
	markRateErr   error
	markPolishErr error
	// markPolishCalls、markPolishDate 保存擦亮日期成功写入的调用次数和日期。
	markPolishCalls int
	markPolishDate  string
	// runtimeData、runtimeDataErr 保存凭证指纹读取结果和错误。
	runtimeData    db.CookieRuntimeData
	runtimeDataErr error
}

// GetValue 返回测试预置的 Cookie。
func (repository *accountTaskFlowRepository) GetValue(context.Context, string) (string, error) {
	return repository.value, repository.valueErr
}

// DueAutoRateOrderIDs 返回测试预置的本地买家已确认收货订单，确保评价流程不依赖远端列表扫描。
func (repository *accountTaskFlowRepository) DueAutoRateOrderIDs(context.Context, string, int) ([]string, error) {
	return repository.dueOrderIDs, repository.dueOrderIDsErr
}

// UpdateValueExisting 返回测试预置的 Cookie 持久化错误。
func (repository *accountTaskFlowRepository) UpdateValueExisting(context.Context, string, string) error {
	return repository.updateErr
}

// GetCookieRuntimeData 返回测试预置的凭证运行数据。
func (repository *accountTaskFlowRepository) GetCookieRuntimeData(context.Context, string) (db.CookieRuntimeData, error) {
	return repository.runtimeData, repository.runtimeDataErr
}

// ClaimRun 返回测试预置的周期任务抢占结果。
func (repository *accountTaskFlowRepository) ClaimRun(context.Context, db.AccountTaskRun, int64) (bool, error) {
	return repository.claimed, repository.claimErr
}

// ClaimRunImmediately 返回测试预置的立即任务抢占结果。
func (repository *accountTaskFlowRepository) ClaimRunImmediately(context.Context, db.AccountTaskRun, int64) (bool, error) {
	return repository.immediateClaimed, repository.immediateClaimErr
}

// FinishRun 按顺序返回终态写入错误，便于覆盖首次失败和隔离补偿。
func (repository *accountTaskFlowRepository) FinishRun(context.Context, string, string, int, int, string, int64) error {
	if len(repository.finishErrors) == 0 {
		return nil
	}
	// finishErr 保存当前终态写入阶段的预置错误。
	finishErr := repository.finishErrors[0]
	repository.finishErrors = repository.finishErrors[1:]
	return finishErr
}

// MarkRateScan 返回测试预置的自动评价扫描时间写入错误。
func (repository *accountTaskFlowRepository) MarkRateScan(context.Context, string, int64) error {
	return repository.markRateErr
}

// MarkPolished 返回测试预置的擦亮日期写入错误，并记录成功写入的调用。
func (repository *accountTaskFlowRepository) MarkPolished(_ context.Context, _ string, date string, _ int64) error {
	if repository.markPolishErr == nil {
		repository.markPolishCalls++
		repository.markPolishDate = date
	}
	return repository.markPolishErr
}

// accountTaskFlowClient 是账号任务平台调用的可控内存客户端。
type accountTaskFlowClient struct {
	// rateResult、rateErr 保存评价动作结果和错误。
	rateResult *mtop.AccountTaskResult
	rateErr    error
	// itemResult、itemErr 保存商品列表结果和错误。
	itemResult *mtop.ItemListResult
	itemErr    error
	// polishResult、polishErr 保存擦亮动作结果和错误。
	polishResult *mtop.AccountTaskResult
	polishErr    error
}

// accountTaskRecovererBoundary 是会话恢复结果可控的测试替身。
type accountTaskRecovererBoundary struct {
	// success 表示是否接受凭证恢复请求。
	success bool
}

// RecoverExpiredCredential 返回预置的会话恢复接受结果。
func (recoverer accountTaskRecovererBoundary) RecoverExpiredCredential(context.Context, string) bool {
	return recoverer.success
}

// accountTaskCookieRotatingRecoverer 在恢复回调中更新测试仓储的签名 Cookie，用于覆盖续期后凭证版本核验。
type accountTaskCookieRotatingRecoverer struct {
	// repository 保存恢复回调要更新的测试凭证仓储。
	repository *accountTaskFlowRepository
	// value 是恢复后写入的 MTOP 签名 Cookie。
	value string
}

// RecoverExpiredCredential 写入预置的恢复后 Cookie 并返回成功，模拟协议续期回调。
func (recoverer accountTaskCookieRotatingRecoverer) RecoverExpiredCredential(context.Context, string) bool {
	recoverer.repository.runtimeData.Value = recoverer.value
	return true
}

// accountTaskCredentialReadFailingRecoverer 在恢复回调成功后让凭证读取失败，用于覆盖后态证据缺失。
type accountTaskCredentialReadFailingRecoverer struct {
	// repository 保存恢复回调要切换读取状态的测试仓储。
	repository *accountTaskFlowRepository
	// readErr 是恢复完成后凭证读取返回的底层错误。
	readErr error
}

// RecoverExpiredCredential 返回成功但使恢复后凭证不可读，验证调用方不会报告假恢复。
func (recoverer accountTaskCredentialReadFailingRecoverer) RecoverExpiredCredential(context.Context, string) bool {
	recoverer.repository.runtimeDataErr = recoverer.readErr
	return true
}

// RateBuyer 返回测试预置的评价结果。
func (client *accountTaskFlowClient) RateBuyer(context.Context, string, string, string) (*mtop.AccountTaskResult, error) {
	if client.rateErr != nil {
		return client.rateResult, client.rateErr
	}
	if client.rateResult == nil {
		return &mtop.AccountTaskResult{Success: true}, nil
	}
	return client.rateResult, nil
}

// FetchAllItems 返回测试预置的商品列表。
func (client *accountTaskFlowClient) FetchAllItems(context.Context, string, int, int) (*mtop.ItemListResult, error) {
	if client.itemErr != nil {
		return nil, client.itemErr
	}
	if client.itemResult == nil {
		return &mtop.ItemListResult{}, nil
	}
	return client.itemResult, nil
}

// PolishItem 返回测试预置的擦亮结果。
func (client *accountTaskFlowClient) PolishItem(context.Context, string, string) (*mtop.AccountTaskResult, error) {
	if client.polishErr != nil {
		return client.polishResult, client.polishErr
	}
	if client.polishResult == nil {
		return &mtop.AccountTaskResult{Success: true}, nil
	}
	return client.polishResult, nil
}

// newAccountTaskFlowCoordinator 构造使用固定平台客户端和日志器的账号任务协调器。
func newAccountTaskFlowCoordinator(repository *accountTaskFlowRepository, client AccountTaskClient) *accountTaskCoordinator {
	return &accountTaskCoordinator{
		repository: repository,
		client:     func() AccountTaskClient { return client },
		logger:     slog.Default(),
	}
}

// TestAccountTaskRateCoversFailureAndCompensationBranches 验证自动评价的平台、Cookie、租约和终态写入失败分支。
func TestAccountTaskRateCoversFailureAndCompensationBranches(t *testing.T) {
	// baseSettings 保存自动评价流程共用的账号任务配置。
	baseSettings := db.AccountTaskSettings{CookieID: "account", RateContent: "感谢光临"}
	// nilClientCoordinator 保存客户端未装配的评价协调器。
	nilClientCoordinator := newAccountTaskFlowCoordinator(&accountTaskFlowRepository{value: "cookie", dueOrderIDs: []string{"trade"}}, nil)
	// nilClientResultErr 保存客户端缺失错误。
	if _, nilClientResultErr := nilClientCoordinator.runAutoRate(context.Background(), baseSettings); nilClientResultErr == nil {
		t.Fatal("缺少自动评价客户端不应成功")
	}
	// valueErr 是 Cookie 读取失败的底层错误。
	valueErr := errors.New("cookie read failed")
	// valueCoordinator 保存 Cookie 读取失败的评价协调器。
	valueCoordinator := newAccountTaskFlowCoordinator(&accountTaskFlowRepository{valueErr: valueErr, dueOrderIDs: []string{"trade"}}, &accountTaskFlowClient{})
	// valueResultErr 保存 Cookie 读取错误。
	if _, valueResultErr := valueCoordinator.runAutoRate(context.Background(), baseSettings); !errors.Is(valueResultErr, valueErr) {
		t.Fatalf("Cookie 读取错误=%v", valueResultErr)
	}
	// dueOrderIDsErr 是本地买家确认收货订单查询失败的底层错误。
	dueOrderIDsErr := errors.New("local completed orders failed")
	// dueOrderIDsCoordinator 保存本地订单查询失败的评价协调器。
	dueOrderIDsCoordinator := newAccountTaskFlowCoordinator(&accountTaskFlowRepository{value: "cookie", dueOrderIDsErr: dueOrderIDsErr}, &accountTaskFlowClient{})
	// dueOrderIDsResultErr 保存本地订单查询错误。
	if _, dueOrderIDsResultErr := dueOrderIDsCoordinator.runAutoRate(context.Background(), baseSettings); !errors.Is(dueOrderIDsResultErr, dueOrderIDsErr) {
		t.Fatalf("本地已完成订单查询错误=%v", dueOrderIDsResultErr)
	}
	// claimErr 是评价运行记录抢占失败的底层错误。
	claimErr := errors.New("claim failed")
	// claimCoordinator 保存运行记录抢占失败的评价协调器。
	claimCoordinator := newAccountTaskFlowCoordinator(&accountTaskFlowRepository{value: "cookie", dueOrderIDs: []string{"trade"}, claimed: true, claimErr: claimErr}, &accountTaskFlowClient{})
	// claimResultErr 保存运行记录抢占错误。
	if _, claimResultErr := claimCoordinator.runAutoRate(context.Background(), baseSettings); !errors.Is(claimResultErr, claimErr) {
		t.Fatalf("评价运行记录抢占错误=%v", claimResultErr)
	}
	// skippedRepository 保存并发运行记录已被其他 worker 抢占的场景。
	skippedRepository := &accountTaskFlowRepository{value: "cookie", dueOrderIDs: []string{"trade"}, claimed: false}
	// skippedCoordinator 保存并发跳过场景的评价协调器。
	skippedCoordinator := newAccountTaskFlowCoordinator(skippedRepository, &accountTaskFlowClient{})
	// skippedSummary、skippedErr 保存跳过结果。
	skippedSummary, skippedErr := skippedCoordinator.runAutoRate(context.Background(), baseSettings)
	if skippedErr != nil || skippedSummary.Skipped != 1 {
		t.Fatalf("并发跳过结果=%+v err=%v", skippedSummary, skippedErr)
	}
	// failedRepository 保存评价动作失败后可以正常写入 failed 状态的仓储。
	failedRepository := &accountTaskFlowRepository{value: "cookie", dueOrderIDs: []string{"trade"}, claimed: true}
	// failedCoordinator 保存平台返回失败结果的评价协调器。
	failedCoordinator := newAccountTaskFlowCoordinator(failedRepository, &accountTaskFlowClient{rateResult: &mtop.AccountTaskResult{Success: false, Message: "平台拒绝"}})
	// failedSummary、failedErr 保存评价失败结果。
	failedSummary, failedErr := failedCoordinator.runAutoRate(context.Background(), baseSettings)
	if failedErr != nil || failedSummary.Failed != 1 {
		t.Fatalf("评价失败结果=%+v err=%v", failedSummary, failedErr)
	}
	// markError 是评价扫描时间写入失败的底层错误。
	markError := errors.New("mark rate scan failed")
	// markCoordinator 保存扫描时间写入失败的评价协调器。
	markCoordinator := newAccountTaskFlowCoordinator(&accountTaskFlowRepository{value: "cookie", markRateErr: markError}, &accountTaskFlowClient{})
	// markResultErr 保存扫描时间写入错误。
	if _, markResultErr := markCoordinator.runAutoRate(context.Background(), baseSettings); !errors.Is(markResultErr, markError) {
		t.Fatalf("扫描时间写入错误=%v", markResultErr)
	}
	// cookieUpdateErr 是平台返回新 Cookie 后的持久化错误。
	cookieUpdateErr := errors.New("task cookie update failed")
	// cookieUpdateCoordinator 保存 Cookie 持久化失败的评价协调器。
	cookieUpdateCoordinator := newAccountTaskFlowCoordinator(&accountTaskFlowRepository{value: "old", dueOrderIDs: []string{"trade"}, claimed: true, updateErr: cookieUpdateErr}, &accountTaskFlowClient{rateResult: &mtop.AccountTaskResult{Success: true, UpdatedCookies: "new"}})
	// cookieUpdateResultErr 保存 Cookie 持久化错误。
	if _, cookieUpdateResultErr := cookieUpdateCoordinator.runAutoRate(context.Background(), baseSettings); !errors.Is(cookieUpdateResultErr, cookieUpdateErr) {
		t.Fatalf("评价 Cookie 持久化错误=%v", cookieUpdateResultErr)
	}
}

// TestAccountTaskPolishCoversClaimFetchAndOutcomeBranches 验证擦亮任务的抢占、商品查询、空列表和失败收口分支。
func TestAccountTaskPolishCoversClaimFetchAndOutcomeBranches(t *testing.T) {
	// settings 保存擦亮流程共用的账号任务配置。
	settings := db.AccountTaskSettings{CookieID: "account"}
	// noClientCoordinator 保存擦亮客户端未装配的协调器。
	noClientCoordinator := newAccountTaskFlowCoordinator(&accountTaskFlowRepository{}, nil)
	// noClientErr 保存客户端缺失错误。
	if _, noClientErr := noClientCoordinator.runAutoPolish(context.Background(), settings, time.Now(), true); noClientErr == nil {
		t.Fatal("缺少擦亮客户端不应成功")
	}
	// immediateClaimErr 是立即抢占失败的底层错误。
	immediateClaimErr := errors.New("immediate claim failed")
	// immediateCoordinator 保存立即抢占失败的协调器。
	immediateCoordinator := newAccountTaskFlowCoordinator(&accountTaskFlowRepository{immediateClaimErr: immediateClaimErr}, &accountTaskFlowClient{})
	// immediateResultErr 保存立即抢占错误。
	if _, immediateResultErr := immediateCoordinator.runAutoPolish(context.Background(), settings, time.Now(), true); !errors.Is(immediateResultErr, immediateClaimErr) {
		t.Fatalf("立即抢占错误=%v", immediateResultErr)
	}
	// scheduledRepository 保存自动调度抢占失败的仓储。
	scheduledRepository := &accountTaskFlowRepository{claimed: false}
	// scheduledCoordinator 保存自动调度跳过场景的协调器。
	scheduledCoordinator := newAccountTaskFlowCoordinator(scheduledRepository, &accountTaskFlowClient{})
	// scheduledSummary、scheduledErr 保存自动调度跳过结果。
	scheduledSummary, scheduledErr := scheduledCoordinator.runAutoPolish(context.Background(), settings, time.Now(), false)
	if scheduledErr != nil || scheduledSummary.Skipped != 1 {
		t.Fatalf("自动调度跳过结果=%+v err=%v", scheduledSummary, scheduledErr)
	}
	// valueErr 是擦亮流程 Cookie 读取失败的底层错误。
	valueErr := errors.New("polish cookie read failed")
	// valueCoordinator 保存 Cookie 读取失败的擦亮协调器。
	valueCoordinator := newAccountTaskFlowCoordinator(&accountTaskFlowRepository{immediateClaimed: true, valueErr: valueErr}, &accountTaskFlowClient{})
	// valueResultErr 保存擦亮 Cookie 读取错误。
	if _, valueResultErr := valueCoordinator.runAutoPolish(context.Background(), settings, time.Now(), true); !errors.Is(valueResultErr, valueErr) {
		t.Fatalf("擦亮 Cookie 读取错误=%v", valueResultErr)
	}
	// itemErr 是商品列表查询失败的底层错误。
	itemErr := errors.New("item list failed")
	// itemCoordinator 保存商品列表查询失败的擦亮协调器。
	itemCoordinator := newAccountTaskFlowCoordinator(&accountTaskFlowRepository{immediateClaimed: true, value: "cookie"}, &accountTaskFlowClient{itemErr: itemErr})
	// itemResultErr 保存商品列表查询错误。
	if _, itemResultErr := itemCoordinator.runAutoPolish(context.Background(), settings, time.Now(), true); !errors.Is(itemResultErr, itemErr) {
		t.Fatalf("商品列表查询错误=%v", itemResultErr)
	}
	// emptyRepository 保存空商品列表可以正常收口的仓储。
	emptyRepository := &accountTaskFlowRepository{immediateClaimed: true, value: "cookie"}
	// emptyCoordinator 保存空商品列表擦亮协调器。
	emptyCoordinator := newAccountTaskFlowCoordinator(emptyRepository, &accountTaskFlowClient{itemResult: &mtop.ItemListResult{}})
	// emptySummary、emptyErr 保存空商品列表结果。
	emptySummary, emptyErr := emptyCoordinator.runAutoPolish(context.Background(), settings, time.Now(), true)
	if emptyErr != nil || emptySummary.Found != 0 || emptySummary.Success != 0 || emptySummary.Failed != 0 || emptySummary.Message == "" || emptyRepository.markPolishCalls != 1 {
		t.Fatalf("空商品列表结果=%+v err=%v", emptySummary, emptyErr)
	}
	// markPolishErr 是空列表成功态写入当日完成标记失败的底层错误。
	markPolishErr := errors.New("mark polish date failed")
	// markErrorRepository 保存当日完成标记写入失败的仓储。
	markErrorRepository := &accountTaskFlowRepository{immediateClaimed: true, value: "cookie", markPolishErr: markPolishErr}
	// markErrorCoordinator 保存空列表完成标记写入失败的擦亮协调器。
	markErrorCoordinator := newAccountTaskFlowCoordinator(markErrorRepository, &accountTaskFlowClient{itemResult: &mtop.ItemListResult{}})
	// markResultErr 保存空列表完成标记写入错误。
	if _, markResultErr := markErrorCoordinator.runAutoPolish(context.Background(), settings, time.Now(), true); !errors.Is(markResultErr, markPolishErr) {
		t.Fatalf("空列表完成标记写入错误=%v", markResultErr)
	}
	// failedRepository 保存商品擦亮失败后正常记录 failed 状态的仓储。
	failedRepository := &accountTaskFlowRepository{immediateClaimed: true, value: "cookie"}
	// failedCoordinator 保存商品擦亮失败的协调器。
	failedCoordinator := newAccountTaskFlowCoordinator(failedRepository, &accountTaskFlowClient{itemResult: &mtop.ItemListResult{Items: []mtop.ItemListItem{{ID: "item"}}}, polishResult: &mtop.AccountTaskResult{Success: false, Message: "平台拒绝"}})
	// failedSummary、failedErr 保存商品擦亮失败结果。
	failedSummary, failedErr := failedCoordinator.runAutoPolish(context.Background(), settings, time.Now(), true)
	if failedErr == nil || failedSummary.Failed != 1 {
		t.Fatalf("擦亮失败结果=%+v err=%v", failedSummary, failedErr)
	}
}

// TestAccountTaskCoordinatorCoversPropagationAndRecoveryBranches 验证任务状态错误传播、人工核对双重失败和会话恢复成功路径。
func TestAccountTaskCoordinatorCoversPropagationAndRecoveryBranches(t *testing.T) {
	// stateErr 是账号状态门禁查询失败的底层错误。
	stateErr := errors.New("state failed")
	// stateCoordinator 保存状态门禁失败的任务协调器。
	stateCoordinator := &accountTaskCoordinator{repository: &accountTaskBoundaryRepository{pausedErr: stateErr}, client: func() AccountTaskClient { return &accountTaskFlowClient{} }, logger: slog.Default()}
	// stateResultErr 保存任务入口传播的状态错误。
	if _, stateResultErr := stateCoordinator.runAccountTask(context.Background(), "account", TaskAutoRate); !errors.Is(stateResultErr, stateErr) {
		t.Fatalf("任务状态错误=%v", stateResultErr)
	}
	// fingerprintErr 是会话阻断状态复核的凭证查询错误。
	fingerprintErr := errors.New("fingerprint failed")
	// blockedCoordinator 保存已经记录 Session 失效指纹的协调器。
	blockedCoordinator := newAccountTaskFlowCoordinator(&accountTaskFlowRepository{runtimeDataErr: fingerprintErr}, &accountTaskFlowClient{})
	blockedCoordinator.sessionExpired.Store("account", "fingerprint")
	// blockedResultErr 保存会话阻断复核错误。
	if _, blockedResultErr := blockedCoordinator.runConfiguredAccountTask(context.Background(), db.AccountTaskSettings{CookieID: "account"}, TaskAutoRate); !errors.Is(blockedResultErr, fingerprintErr) {
		t.Fatalf("会话阻断复核错误=%v", blockedResultErr)
	}
	// finishError、quarantineError 保存两次任务终态写入错误。
	finishError := errors.New("finish failed")
	// quarantineError 表示人工核对补偿状态的第二次写入错误。
	quarantineError := errors.New("quarantine failed")
	// finishCoordinator 保存任务终态和隔离状态均失败的协调器。
	finishCoordinator := newAccountTaskFlowCoordinator(&accountTaskFlowRepository{finishErrors: []error{finishError, quarantineError}}, &accountTaskFlowClient{})
	// finishResultErr 保存合并后的人工核对错误。
	finishResultErr := finishCoordinator.finishAccountTaskRun(context.Background(), "run", "success", 1, 0, "", 0)
	if !errors.Is(finishResultErr, finishError) || !errors.Is(finishResultErr, quarantineError) {
		t.Fatalf("终态双重错误=%v", finishResultErr)
	}
	// recoveryCoordinator 保存会话恢复成功的协调器。
	recoveryCoordinator := newAccountTaskFlowCoordinator(&accountTaskFlowRepository{runtimeData: db.CookieRuntimeData{Value: "cookie"}}, &accountTaskFlowClient{})
	recoveryCoordinator.recoverer = func() CredentialRecoverer { return accountTaskRecovererBoundary{success: true} }
	// sessionError 是平台报告的会话过期错误。
	sessionError := errors.New("session expired")
	// recoveryErr 保存恢复成功后的提示错误，原始会话错误仍应可识别。
	recoveryErr := recoveryCoordinator.recoverAccountTaskCredential(context.Background(), "account", sessionError)
	if !errors.Is(recoveryErr, sessionError) || !errors.Is(recoveryErr, errAccountTaskCredentialRenewed) {
		t.Fatalf("会话恢复错误=%v", recoveryErr)
	}
	// tokenCoordinator 为 Token 错误装配可观测恢复入口，验证入口不能被调用。
	tokenCoordinator := newAccountTaskFlowCoordinator(&accountTaskFlowRepository{
		runtimeData: db.CookieRuntimeData{Value: "unb=1; _m_h5_tk=old_token"},
	}, &accountTaskFlowClient{})
	// 恢复端口即使存在也不能因 Token 错误被读取或调用。
	tokenCoordinator.recoverer = func() CredentialRecoverer {
		t.Fatal("Token 错误不得进入账号恢复端口")
		return nil
	}
	// tokenError 是平台报告的 MTOP 签名 Token 过期错误。
	tokenError := &mtop.MTopResponseError{API: "评价接口", Kind: mtop.MTopErrorTokenExpired, HTTPStatus: 200}
	// tokenRecoveryErr 必须原样保留 Token 失败，不能包装为账号恢复结果。
	tokenRecoveryErr := tokenCoordinator.recoverAccountTaskCredential(context.Background(), "account", tokenError)
	if tokenRecoveryErr != tokenError {
		t.Fatalf("Token 错误必须原样返回: %v", tokenRecoveryErr)
	}
	// expiryRepository 保存初始凭证，验证 Token 错误不会调用只更新时间后缀的恢复器。
	expiryRepository := &accountTaskFlowRepository{runtimeData: db.CookieRuntimeData{Value: "unb=1; _m_h5_tk=old_token_100"}}
	// expiryCoordinator 使用会把时间后缀更新为 200 的恢复回调。
	expiryCoordinator := newAccountTaskFlowCoordinator(expiryRepository, &accountTaskFlowClient{})
	expiryCoordinator.recoverer = func() CredentialRecoverer {
		return accountTaskCookieRotatingRecoverer{repository: expiryRepository, value: "unb=1; _m_h5_tk=old_token_200"}
	}
	// expiryRecoveryErr 保存仅令牌时间后缀变化时的恢复结果。
	expiryRecoveryErr := expiryCoordinator.recoverAccountTaskCredential(context.Background(), "account", tokenError)
	if expiryRecoveryErr != tokenError || expiryRepository.runtimeData.Value != "unb=1; _m_h5_tk=old_token_100" {
		t.Fatal("Token 错误不得通过账号恢复器更新时间后缀")
	}
	// preReadErr 是仓储预置错误；Token 错误不应读取账号恢复所需的凭证。
	preReadErr := errors.New("续期前 Cookie 读取失败")
	// preReadRepository 保存无法提供续期前 Token 的测试仓储。
	preReadRepository := &accountTaskFlowRepository{runtimeDataErr: preReadErr}
	// preReadCoordinator 验证仓储不可读时也直接保留 Token 错误。
	preReadCoordinator := newAccountTaskFlowCoordinator(preReadRepository, &accountTaskFlowClient{})
	preReadCoordinator.recoverer = func() CredentialRecoverer { return accountTaskRecovererBoundary{success: true} }
	// preReadRecoveryErr 必须原样保留平台错误，不执行账号恢复读取。
	preReadRecoveryErr := preReadCoordinator.recoverAccountTaskCredential(context.Background(), "account", tokenError)
	if preReadRecoveryErr != tokenError {
		t.Fatalf("仓储不可读也不得把 Token 错误升级为账号恢复: %v", preReadRecoveryErr)
	}
	// postReadErr 由恢复器注入；Token 错误不得触发该副作用。
	postReadErr := errors.New("续期后 Cookie 读取失败")
	// postReadRepository 保存续期前可读、续期后由恢复器切换为不可读的测试仓储。
	postReadRepository := &accountTaskFlowRepository{runtimeData: db.CookieRuntimeData{Value: "unb=1; _m_h5_tk=old_token"}}
	// postReadCoordinator 验证 Token 错误根本不会运行会修改仓储的恢复器。
	postReadCoordinator := newAccountTaskFlowCoordinator(postReadRepository, &accountTaskFlowClient{})
	postReadCoordinator.recoverer = func() CredentialRecoverer {
		return accountTaskCredentialReadFailingRecoverer{repository: postReadRepository, readErr: postReadErr}
	}
	// postReadRecoveryErr 原样保留 Token 错误，仓储读取能力也应保持不变。
	postReadRecoveryErr := postReadCoordinator.recoverAccountTaskCredential(context.Background(), "account", tokenError)
	if postReadRecoveryErr != tokenError || postReadRepository.runtimeDataErr != nil {
		t.Fatalf("Token 错误不得执行账号恢复副作用: %v", postReadRecoveryErr)
	}
}
