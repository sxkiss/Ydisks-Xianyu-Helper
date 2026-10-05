package adapter

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	accountmanager "xianyu-go/internal/account"
	chatapp "xianyu-go/internal/application/chat"
	"xianyu-go/internal/automation"
	domainchat "xianyu-go/internal/chat"
	"xianyu-go/internal/xianyu/cookierefresh"
	"xianyu-go/internal/xianyu/mtop"
)

// coverageChatItemClient 记录商品查询适配参数并返回预设 MTOP 页面。
type coverageChatItemClient struct {
	// mtop.Client 保留当前测试未使用的平台能力占位。
	mtop.Client
	// result 和 err 分别是商品查询预设结果与错误。
	result *mtop.ChatItemPage
	err    error
	// role 保存适配器映射后的官网商品归属参数。
	role string
	// started 和 release 用于确定化平台调用期间的并发凭证更新窗口。
	started chan struct{}
	release chan struct{}
	// replacement 是测试模拟响应 Set-Cookie 后产生的完整 Cookie Jar。
	replacement []cookierefresh.BrowserCookie
}

// SearchChatItems 记录官网角色并返回不含凭证的商品页测试结果。
func (client *coverageChatItemClient) SearchChatItems(ctx context.Context, _, _, role, _ string, _ int) (*mtop.ChatItemPage, error) {
	client.role = role
	if client.started != nil {
		close(client.started)
	}
	if client.release != nil {
		select {
		case <-client.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if client.replacement != nil {
		// session 是适配器安装在本次平台请求上的 Cookie 会话。
		session := mtop.CookieSessionFromContext(ctx)
		if session != nil {
			session.ReplaceSnapshot(client.replacement)
		}
	}
	return client.result, client.err
}

// coverageItemRuntimeSender 是商品卡片适配器使用的最小运行时发送替身。
type coverageItemRuntimeSender struct {
	// MessageSender 占位满足既有自动化文本和图片发送接口。
	automation.MessageSender
	// buyerID 和 itemID 保存商品卡片实际发送目标与商品标识。
	buyerID, itemID string
}

// SendItemCard 记录商品卡片适配器透传的对端和商品字段。
func (sender *coverageItemRuntimeSender) SendItemCard(_ context.Context, _, buyerID, itemID, _, _, _ string) error {
	sender.buyerID, sender.itemID = buyerID, itemID
	return nil
}

// TestChatSendingWrappersMapOutgoingAndMediaMessages 验证聊天外发适配器的文本、媒体和状态转换。
func TestChatSendingWrappersMapOutgoingAndMediaMessages(t *testing.T) {
	// store、cleanup 保存隔离的 SQLite 存储和关闭责任。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// ctx 是本测试聊天外发共用的上下文。
	ctx := context.Background()
	// repository 保存真实领域聊天服务包装后的应用外发仓储。
	repository := NewChatOutgoingRepository(domainchat.New(store))
	// session 保存外发消息共同使用的应用会话摘要。
	session := chatapp.Session{AccountID: "cid", ChatID: "chat-send", PeerUserID: "buyer-send", PeerName: "买家"}
	// textMessage、textErr 保存文本外发消息的模型转换结果。
	textMessage, textErr := repository.CreateOutgoing(ctx, session, "你好")
	if textErr != nil || textMessage.Content != "你好" || textMessage.AccountID != "cid" {
		t.Fatalf("文本外发转换异常 message=%+v err=%v", textMessage, textErr)
	}
	// mediaMessage、mediaErr 保存媒体外发消息的模型转换结果。
	mediaMessage, mediaErr := repository.CreateOutgoingMedia(ctx, session, "image", "https://cdn.invalid/image.jpg")
	if mediaErr != nil || mediaMessage.MessageType != "image" || mediaMessage.Content == "" {
		t.Fatalf("媒体外发转换异常 message=%+v err=%v", mediaMessage, mediaErr)
	}
	// statusMessage、statusErr 保存消息状态更新后的应用模型。
	statusMessage, statusErr := repository.SetOutgoingStatus(ctx, "cid", textMessage.MessageKey, "sent")
	if statusErr != nil || statusMessage.Status != "sent" {
		t.Fatalf("外发状态转换异常 message=%+v err=%v", statusMessage, statusErr)
	}
	// unavailableMediaErr 保存未装配领域服务时的媒体错误。
	_, unavailableMediaErr := NewChatOutgoingRepository(nil).CreateOutgoingMedia(ctx, session, "image", "url")
	if !errors.Is(unavailableMediaErr, chatapp.ErrUnavailable) {
		t.Fatalf("未装配媒体仓储错误=%v", unavailableMediaErr)
	}
	// unavailableStatusErr 保存未装配领域服务时的状态错误。
	_, unavailableStatusErr := NewChatOutgoingRepository(nil).SetOutgoingStatus(ctx, "cid", "key", "sent")
	if !errors.Is(unavailableStatusErr, chatapp.ErrUnavailable) {
		t.Fatalf("未装配状态仓储错误=%v", unavailableStatusErr)
	}
}

// TestChatSenderForwardsToRuntime 验证应用层发送器把文本与图片请求透传到账号运行时。
func TestChatSenderForwardsToRuntime(t *testing.T) {
	// ctx 是本测试发送器共用的上下文。
	ctx := context.Background()
	// unavailableSender 保存空运行时发送器的应用层包装。
	unavailableSender := chatSender{}
	// textErr 保存空运行时发送文本时返回的应用层错误。
	if err := unavailableSender.SendText(ctx, "chat", "buyer", "text", "key"); !errors.Is(err, chatapp.ErrUnavailable) {
		t.Fatalf("空文本发送错误=%v", err)
	}
	// imageErr 保存空运行时发送图片时返回的应用层错误。
	if err := unavailableSender.SendImage(ctx, "chat", "buyer", "url", 1, 10, 20, "key"); !errors.Is(err, chatapp.ErrUnavailable) {
		t.Fatalf("空图片发送错误=%v", err)
	}
	// runtimeSender 保存可记录透传调用的账号运行时发送器。
	runtimeSender := &coverageAutomationSender{}
	// sender 保存运行时发送器的应用层包装。
	sender := chatSender{sender: runtimeSender}
	// sendErr 保存文本请求透传到账号运行时的结果。
	if sendErr := sender.SendText(ctx, "chat", "buyer", "text", "key"); sendErr != nil {
		t.Fatal(sendErr)
	}
	// imageErr 保存图片请求透传到账号运行时的结果。
	if imageErr := sender.SendImage(ctx, "chat", "buyer", "url", 1, 10, 20, "key"); imageErr != nil {
		t.Fatal(imageErr)
	}
	// unsupportedItemErr 验证普通自动化发送器不会被误判为商品卡片能力。
	if unsupportedItemErr := sender.SendItemCard(ctx, "chat", "buyer", chatapp.ChatItem{ItemID: "item"}, "key"); !errors.Is(unsupportedItemErr, chatapp.ErrUnavailable) {
		t.Fatalf("不支持商品卡片的运行时错误=%v", unsupportedItemErr)
	}
	// itemRuntime 是支持商品卡片窄能力的账号运行时替身。
	itemRuntime := &coverageItemRuntimeSender{}
	// itemSender 是商品卡片应用发送器包装。
	itemSender := chatSender{sender: itemRuntime}
	// itemErr 表示商品快照和目标是否成功透传到运行时。
	if itemErr := itemSender.SendItemCard(ctx, "chat", "stored-buyer", chatapp.ChatItem{ItemID: "item-1", Title: "商品", ImageURL: "https://img.example/a.png", Price: "1"}, "key"); itemErr != nil || itemRuntime.buyerID != "stored-buyer" || itemRuntime.itemID != "item-1" {
		t.Fatalf("商品卡片透传异常 runtime=%+v err=%v", itemRuntime, itemErr)
	}
}

// TestChatSenderPreservesDefiniteNotSentError 验证运行时明确未发送错误不会被误判为不确定结果。
func TestChatSenderPreservesDefiniteNotSentError(t *testing.T) {
	// sender 是返回确定未发送错误的运行时包装器。
	sender := chatSender{sender: &coverageAutomationSender{sendErr: automation.ErrMessageNotSent}}
	// err 保存应用层发送器透传后的错误链。
	err := sender.SendText(context.Background(), "chat", "buyer", "text", "key")
	if !errors.Is(err, automation.ErrMessageNotSent) {
		t.Fatalf("确定未发送错误未保留: %v", err)
	}
	if errors.Is(err, chatapp.ErrSendUncertain) {
		t.Fatalf("确定未发送错误被误判为不确定: %v", err)
	}
}

// TestChatItemCatalogMapsRoleAndPersistsUpdatedCookie 验证商品目录适配器映射官网角色并收口 Cookie 更新。
func TestChatItemCatalogMapsRoleAndPersistsUpdatedCookie(t *testing.T) {
	// store 和 cleanup 是带测试账号的隔离 SQLite 存储及释放函数。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// client 是返回刷新 Cookie 和固定商品的 MTOP 替身。
	client := &coverageChatItemClient{result: &mtop.ChatItemPage{Items: []mtop.ChatItem{{ItemID: "item-1", Title: "测试商品", ImageURL: "https://img.example/item.png", Price: "10", Description: "摘要"}}, Page: 1, HasMore: true, UpdatedCookies: "unb=1; _m_h5_tk=fresh_2;"}}
	// catalog 是将凭证边界限制在适配层的商品目录。
	catalog := NewChatItemCatalog(store, func() mtop.Client { return client }, nil)
	// result 和 listErr 是 self 角色商品查询的应用层结果。
	result, listErr := catalog.ListChatItems(context.Background(), "cid", "chat-1", "self", "商品", 1)
	if listErr != nil || len(result.Items) != 1 || !result.HasMore || client.role != "own" || result.Items[0].Description != "摘要" {
		t.Fatalf("result=%+v role=%q err=%v", result, client.role, listErr)
	}
	// storedCookie 和 cookieErr 是适配器写回后的账号凭证，仅在测试内检查更新结果。
	storedCookie, cookieErr := store.Cookies.GetValue(context.Background(), "cid")
	if cookieErr != nil || storedCookie != "unb=1; _m_h5_tk=fresh_2;" {
		t.Fatalf("Cookie 写回异常 err=%v", cookieErr)
	}
	// peerResult 和 peerErr 验证 peer 角色映射为官网 other。
	peerResult, peerErr := catalog.ListChatItems(context.Background(), "cid", "chat-1", "peer", "", 1)
	if peerErr != nil || len(peerResult.Items) != 1 || client.role != "other" {
		t.Fatalf("peer result=%+v role=%q err=%v", peerResult, client.role, peerErr)
	}
}

// TestChatItemCatalogPreservesCompleteCookieSnapshot 验证商品查询写回权威 Cookie Jar 时不会降级为平面凭证。
func TestChatItemCatalogPreservesCompleteCookieSnapshot(t *testing.T) {
	// store 和 cleanup 是带测试账号的隔离存储及释放函数。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// initialSnapshot 保存查询前的完整 Cookie 路径和属性。
	initialSnapshot := []cookierefresh.BrowserCookie{
		{Name: "unb", Value: "1", Domain: ".goofish.com", Path: "/"},
		{Name: "_m_h5_tk", Value: "old_1", Domain: ".goofish.com", Path: "/", HTTPOnly: true},
		{Name: "scoped", Value: "keep", Domain: "h5api.m.goofish.com", Path: "/h5/"},
	}
	// metadata 是包含完整快照和其他业务字段的初始凭证元数据。
	metadata := cookierefresh.MetadataWithSnapshot(`{"origin":"chat-test"}`, initialSnapshot)
	// persistErr 是测试初始完整 Cookie 快照的保存结果。
	if persistErr := store.Cookies.UpdateRenewalCookie(context.Background(), "cid", "unb=1; _m_h5_tk=old_1", metadata, 1); persistErr != nil {
		t.Fatal(persistErr)
	}
	// replacement 模拟平台仅轮换签名 Token 后的完整 Cookie Jar，路径 Cookie 必须保留。
	replacement := []cookierefresh.BrowserCookie{
		{Name: "unb", Value: "1", Domain: ".goofish.com", Path: "/"},
		{Name: "_m_h5_tk", Value: "fresh_2", Domain: ".goofish.com", Path: "/", HTTPOnly: true},
		{Name: "scoped", Value: "keep", Domain: "h5api.m.goofish.com", Path: "/h5/"},
	}
	// client 通过请求上下文更新权威 Cookie 会话，并返回一页空商品。
	client := &coverageChatItemClient{replacement: replacement, result: &mtop.ChatItemPage{Page: 1}}
	// catalog 是待验证完整快照写回的聊天商品目录。
	catalog := NewChatItemCatalog(store, func() mtop.Client { return client }, nil)
	// listErr 是使用完整 Cookie 会话查询商品的结果。
	if _, listErr := catalog.ListChatItems(context.Background(), "cid", "chat-1", "peer", "", 1); listErr != nil {
		t.Fatal(listErr)
	}
	// latest 是查询后重新读取的凭证平台视图。
	latest, latestErr := store.Cookies.GetCookiePlatformRuntimeData(context.Background(), "cid")
	if latestErr != nil {
		t.Fatal(latestErr)
	}
	// storedSnapshot 和 complete 验证 metadata 仍是完整 Jar，且路径 Cookie 未被扁平覆盖清除。
	storedSnapshot, complete := cookierefresh.SnapshotFromMetadataOK(latest.MetadataJSON)
	if !complete || len(storedSnapshot) != 3 || !strings.Contains(latest.MetadataJSON, `"origin":"chat-test"`) {
		t.Fatalf("complete=%v snapshot=%+v metadata=%s", complete, storedSnapshot, latest.MetadataJSON)
	}
}

// TestChatItemCatalogSyncsRuntimeForMetadataOnlyCookieChange 验证路径 Cookie 单独变化时仍要求在线运行时复读完整凭证。
func TestChatItemCatalogSyncsRuntimeForMetadataOnlyCookieChange(t *testing.T) {
	// store 和 cleanup 是路径 Cookie 更新测试使用的隔离存储及释放函数。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// initialSnapshot 保存根路径签名 Cookie 和不参与 /im 扁平头的接口路径 Cookie。
	initialSnapshot := []cookierefresh.BrowserCookie{
		{Name: "unb", Value: "1", Domain: ".goofish.com", Path: "/"},
		{Name: "scoped", Value: "old", Domain: "h5api.m.goofish.com", Path: "/h5/"},
	}
	// initialSession 用权威 Jar 计算数据库应保存的规范 /im Cookie 值。
	_, initialSession := mtop.WithCookieSnapshot(context.Background(), initialSnapshot)
	// initialValue 是路径 Cookie 变化前后都保持不变的 /im 扁平 Cookie。
	initialValue, _, _ := initialSession.State()
	// metadata 是包含路径 Cookie 的初始完整 Jar。
	metadata := cookierefresh.MetadataWithSnapshot(`{"origin":"metadata-only"}`, initialSnapshot)
	// persistErr 是初始凭证状态的保存结果。
	if persistErr := store.Cookies.UpdateRenewalCookie(context.Background(), "cid", initialValue, metadata, 1); persistErr != nil {
		t.Fatal(persistErr)
	}
	// initial 是平台请求开始前用于并发指纹复核的完整凭证视图。
	initial, initialErr := store.Cookies.GetCookiePlatformRuntimeData(context.Background(), "cid")
	if initialErr != nil {
		t.Fatal(initialErr)
	}
	// requestSession 是本次商品查询独占的权威 Cookie 会话。
	_, requestSession := withCookieSnapshot(context.Background(), initial)
	requestSession.ReplaceSnapshot([]cookierefresh.BrowserCookie{
		{Name: "unb", Value: "1", Domain: ".goofish.com", Path: "/"},
		{Name: "scoped", Value: "fresh", Domain: "h5api.m.goofish.com", Path: "/h5/"},
	})
	// catalog 是直接验证凭证提交结果的聊天商品目录。
	catalog := chatItemCatalog{credentials: chatCredentialRepository{store: store}}
	// runtimeValue、syncRuntime 和 commitErr 验证扁平值不变时完整元数据写入仍会触发运行时同步。
	runtimeValue, syncRuntime, commitErr := catalog.persistCookieSession(context.Background(), initial, requestSession, nil)
	if commitErr != nil || !syncRuntime || runtimeValue != initialValue {
		t.Fatalf("runtimeValue=%q initial=%q sync=%v err=%v", runtimeValue, initialValue, syncRuntime, commitErr)
	}
	// latest 和 latestErr 是提交后的完整凭证视图。
	latest, latestErr := store.Cookies.GetCookiePlatformRuntimeData(context.Background(), "cid")
	if latestErr != nil || !strings.Contains(latest.MetadataJSON, `"value":"fresh"`) {
		t.Fatalf("latest=%+v err=%v", latest, latestErr)
	}
}

// TestChatItemCatalogRejectsStaleCookieWriteback 验证平台慢请求不会覆盖并发登录或续期写入的新凭证。
func TestChatItemCatalogRejectsStaleCookieWriteback(t *testing.T) {
	// store 和 cleanup 是并发凭证测试使用的隔离存储及释放函数。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// started 和 release 确定化商品查询读取旧凭证后的暂停窗口。
	started, release := make(chan struct{}), make(chan struct{})
	// client 返回基于旧快照生成的过期 Cookie，适配器必须在写回前复核指纹。
	client := &coverageChatItemClient{started: started, release: release, result: &mtop.ChatItemPage{Page: 1, UpdatedCookies: "unb=1; _m_h5_tk=stale_2;"}}
	// catalog 是待验证条件写回的聊天商品目录。
	catalog := NewChatItemCatalog(store, func() mtop.Client { return client }, nil)
	// outcome 接收异步查询结果，使测试能在平台 I/O 期间更新凭证。
	outcome := make(chan error, 1)
	go func() {
		// listErr 是异步商品查询的最终结果。
		_, listErr := catalog.ListChatItems(context.Background(), "cid", "chat-1", "peer", "", 1)
		outcome <- listErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("商品查询未进入平台调用")
	}
	// latestCookie 和 latestMetadata 模拟并发登录流程写入的新权威状态。
	latestCookie, latestMetadata := "unb=1; _m_h5_tk=latest_3;", `{"origin":"new-login"}`
	// updateErr 是并发登录流程提交新凭证的持久化结果。
	if updateErr := store.Cookies.UpdateRenewalCookie(context.Background(), "cid", latestCookie, latestMetadata, 3); updateErr != nil {
		t.Fatal(updateErr)
	}
	close(release)
	// listErr 是旧商品查询在检测到凭证指纹变化后的返回错误。
	if listErr := <-outcome; listErr == nil || !strings.Contains(listErr.Error(), "账号凭证已变化") {
		t.Fatalf("过期写回应被拒绝 err=%v", listErr)
	}
	// stored 是请求结束后的凭证值，必须保持并发流程写入的最新版本。
	stored, storedErr := store.Cookies.GetCookiePlatformRuntimeData(context.Background(), "cid")
	if storedErr != nil || stored.Value != latestCookie || stored.MetadataJSON != latestMetadata {
		t.Fatalf("stored=%+v err=%v", stored, storedErr)
	}
}

// TestChatReadReporterWithoutRuntime 验证没有账号管理器时已读上报保持无副作用。
func TestChatReadReporterWithoutRuntime(t *testing.T) {
	// reporter 保存未装配账号管理器的已读上报适配器。
	reporter := NewChatReadReporter(nil)
	// err 保存无运行时上报时的结果。
	err := reporter.ReportRead(context.Background(), "cid", "chat", []map[string]any{{"messageId": "key"}})
	if err != nil {
		t.Fatal(err)
	}
}

// TestChatReadReporterAndSenderProviderUseStartedRuntime 验证已启动账号会进入已读上报和发送器适配分支。
func TestChatReadReporterAndSenderProviderUseStartedRuntime(t *testing.T) {
	// store、cleanup 保存账号管理器启动所需的隔离数据库及关闭责任。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// manager 保存只用于测试运行时句柄解析的账号管理器。
	manager := accountmanager.NewManager(store, &Adapter{}, nil)
	// ctx、cancel 保存账号运行时的生命周期上下文。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// startErr 保存测试账号运行实例登记结果。
	startErr := manager.Start(ctx, "cid", "unb=1; _m_h5_tk=tk;")
	if startErr != nil {
		t.Fatalf("启动测试账号失败: %v", startErr)
	}
	// reporter 保存绑定已启动账号管理器的已读上报适配器。
	reporter := NewChatReadReporter(manager)
	// reportErr 保存账号尚未建立 WebSocket 时由运行时返回的生命周期错误。
	reportErr := reporter.ReportRead(ctx, "cid", "chat", []map[string]any{{"messageId": "key"}})
	if reportErr == nil {
		t.Fatal("未建立 WebSocket 时已读上报应返回运行时错误")
	}
	// provider 保存绑定已启动账号管理器的发送器解析适配器。
	provider := NewChatSenderProvider(manager)
	// sender、ok 保存已启动账号对应的聊天发送器及解析结果。
	sender, ok := provider.Sender("cid")
	if !ok || sender == nil {
		t.Fatalf("已启动账号发送器解析失败 sender=%v ok=%v", sender, ok)
	}
	// runtimeUpload 保存带刷新凭证的平台图片上传结果，验证运行时 Cookie 同步分支。
	runtimeUpload := fakeChatUploadClient{upload: &mtop.ChatImageUpload{URL: "https://cdn.example/runtime.jpg", UpdatedCookies: "unb=1; _m_h5_tk=runtime;"}}
	// runtimeImage、runtimeImageErr 保存绑定账号管理器的图片上传结果。
	runtimeImage, runtimeImageErr := NewChatImageUploader(store, func() mtop.Client { return runtimeUpload }, manager).UploadChatImage(ctx, "cid", "a.jpg", "image/jpeg", []byte("image"))
	if runtimeImageErr != nil || runtimeImage.URL == "" {
		t.Fatalf("运行时 Cookie 同步上传失败 image=%+v err=%v", runtimeImage, runtimeImageErr)
	}
	manager.Stop("cid")
}

// TestChatReadReporterAndSenderProviderRejectMissingRuntime 验证管理器存在但账号未启动时保持无副作用。
func TestChatReadReporterAndSenderProviderRejectMissingRuntime(t *testing.T) {
	// manager 保存没有任何运行实例的账号管理器。
	manager := accountmanager.NewManager(nil, nil, nil)
	// reporter 保存绑定空运行实例表的已读上报适配器。
	reporter := NewChatReadReporter(manager)
	// reportErr 保存不存在账号的已读上报结果。
	if reportErr := reporter.ReportRead(context.Background(), "missing", "chat", nil); reportErr != nil {
		t.Fatalf("不存在账号的已读上报错误=%v", reportErr)
	}
	// provider 保存绑定空运行实例表的发送器解析适配器。
	provider := NewChatSenderProvider(manager)
	// sender、ok 保存不存在账号时的发送器解析结果。
	sender, ok := provider.Sender("missing")
	if ok || sender != nil {
		t.Fatalf("不存在账号不应返回发送器 sender=%v ok=%v", sender, ok)
	}
}

// TestChatSendingConstructionAndCredentialGuards 验证聊天应用构造和凭证仓储的缺失依赖保护。
func TestChatSendingConstructionAndCredentialGuards(t *testing.T) {
	// ctx 是本测试依赖保护共用的上下文。
	ctx := context.Background()
	// emptyRepository 保存没有数据库入口的聊天凭证仓储。
	emptyRepository := chatCredentialRepository{}
	// readErr 保存缺少 Cookie 子仓储时的读取错误。
	if _, readErr := emptyRepository.getCookieValue(ctx, "cid"); !errors.Is(readErr, chatapp.ErrUnavailable) {
		t.Fatalf("缺失 Cookie 仓储读取错误=%v", readErr)
	}
	// updateErr 保存缺少 Cookie 子仓储时的写入错误。
	if updateErr := emptyRepository.updateCookieValue(ctx, "cid", "cookie"); !errors.Is(updateErr, chatapp.ErrUnavailable) {
		t.Fatalf("缺失 Cookie 仓储写入错误=%v", updateErr)
	}
	// emptyService 保存缺少领域服务时仍可构造的历史聊天应用服务。
	emptyService := NewChatSendingApplication(nil, nil, nil, nil)
	if emptyService == nil {
		t.Fatal("聊天应用服务构造结果为空")
	}
	// store、cleanup 保存完整聊天应用构造使用的测试存储。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// completeService 保存带领域服务和数据库适配器的聊天应用服务。
	completeService := NewChatSendingApplication(domainchat.New(store), store, nil, nil)
	if completeService == nil {
		t.Fatal("完整聊天应用服务构造结果为空")
	}
	// emptyMessage 保存 nil 数据库消息转换出的零值应用模型。
	emptyMessage := chatApplicationMessage(nil)
	if emptyMessage != (chatapp.Message{}) {
		t.Fatalf("空消息转换异常 message=%+v", emptyMessage)
	}
}
