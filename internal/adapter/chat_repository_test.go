package adapter

import (
	"context"
	"errors"
	"strings"
	"testing"

	chatapp "xianyu-go/internal/application/chat"
	"xianyu-go/internal/automation"
	domainchat "xianyu-go/internal/chat"
	"xianyu-go/internal/db"
	"xianyu-go/internal/xianyu/cookierefresh"
	"xianyu-go/internal/xianyu/mtop"
)

// subscriptionDomainRepository 是领域聊天订阅测试使用的最小内存仓储。
type subscriptionDomainRepository struct{}

// ListOwnedIDs 返回实时订阅测试账号归属。
func (subscriptionDomainRepository) ListOwnedIDs(context.Context, int64) ([]string, error) {
	return []string{"cid"}, nil
}

// SetSessionVisible 满足领域聊天仓储接口的软隐藏能力。
func (subscriptionDomainRepository) SetSessionVisible(context.Context, string, string, bool) error {
	return nil
}

// UpsertSession 满足领域聊天仓储接口的会话写入能力。
func (subscriptionDomainRepository) UpsertSession(context.Context, db.ChatSession) error { return nil }

// SyncSessionSummary 满足领域聊天仓储接口的会话摘要同步能力。
func (subscriptionDomainRepository) SyncSessionSummary(context.Context, string, string, string, int64, int64, int) error {
	return nil
}

// SaveMessage 返回领域事件需要的消息副本，模拟幂等首次写入。
func (subscriptionDomainRepository) SaveMessage(_ context.Context, _ db.ChatSession, message db.ChatMessage, _ bool) (*db.ChatMessage, bool, error) {
	return &message, true, nil
}

// UpdateMessageContent 满足领域聊天仓储接口的富媒体分类与地址更新能力。
func (subscriptionDomainRepository) UpdateMessageContent(context.Context, string, string, string, string) error {
	return nil
}

// UpdateMessageMediaDuration 满足领域聊天仓储接口的富媒体时长更新能力。
func (subscriptionDomainRepository) UpdateMessageMediaDuration(context.Context, string, string, int64) error {
	return nil
}

// UpdateMessageStatus 满足领域聊天仓储接口的消息状态更新能力。
func (subscriptionDomainRepository) UpdateMessageStatus(context.Context, string, string, string) (*db.ChatMessage, error) {
	return &db.ChatMessage{}, nil
}

// CountUnreadUserMessages 为订阅测试提供没有本地未读消息的稳定结果。
func (subscriptionDomainRepository) CountUnreadUserMessages(context.Context, string, string) (int, error) {
	return 0, nil
}

// MarkMessageRead 为订阅测试模拟指定出站消息的已读回执写入。
func (subscriptionDomainRepository) MarkMessageRead(_ context.Context, _ string, key string, readAt int64) (*db.ChatMessage, error) {
	return &db.ChatMessage{MessageKey: key, ReadStatus: 2, ReadAt: readAt}, nil
}

// MarkLatestOutgoingRead 为订阅测试模拟缺失消息键时的会话级已读回退。
func (subscriptionDomainRepository) MarkLatestOutgoingRead(_ context.Context, _ string, chatID string, readAt int64) (*db.ChatMessage, error) {
	return &db.ChatMessage{ChatID: chatID, ReadStatus: 2, ReadAt: readAt}, nil
}

// TestChatSubscriptionProviderConvertsAndCleansEvents 验证领域事件转换和订阅取消可重复执行。
func TestChatSubscriptionProviderConvertsAndCleansEvents(t *testing.T) {
	// service 是使用内存仓储构造的领域聊天事件中心。
	service := domainchat.NewWithRepository(subscriptionDomainRepository{})
	// provider 是隐藏领域模型并输出应用事件的适配器。
	provider := NewChatSubscriptionProvider(service)
	// events、cancel、err 保存适配器订阅结果。
	events, cancel, err := provider.Subscribe(context.Background(), 42)
	if err != nil {
		t.Fatalf("Subscribe() error=%v", err)
	}
	// _, _, recordErr 保存领域入站消息写入结果。
	_, _, recordErr := service.RecordIncoming(context.Background(), domainchat.Incoming{
		AccountID: "cid", ChatID: "chat-1", BuyerID: "buyer-1", BuyerName: "买家", Text: "你好",
		Raw: map[string]any{"messageId": "msg-1"},
	})
	if recordErr != nil {
		t.Fatalf("RecordIncoming() error=%v", recordErr)
	}
	// event 保存转换后的应用层事件。
	event := <-events
	if event.Type != "message.created" || event.Message == nil || event.Message.MessageKey != "msg-1" || event.Message.Content != "你好" {
		t.Fatalf("event=%+v", event)
	}
	cancel()
	cancel()
	// closed 表示取消后转发通道已完成清理并关闭。
	_, closed := <-events
	if closed {
		t.Fatal("取消订阅后事件通道仍保持开启")
	}
}

// fakeChatUploadClient 只实现聊天图片上传能力，用于隔离平台网络请求。
type fakeChatUploadClient struct {
	// mtop.Client 保留其余平台能力占位，测试只关注图片上传方法。
	mtop.Client
	// upload 保存模拟平台返回的图片结果。
	upload *mtop.ChatImageUpload
	// err 保存模拟平台上传错误。
	err error
	// beforeReturn 在返回上传结果前执行一次测试专用的存储状态变更。
	beforeReturn func()
}

// UploadChatImage 返回预设图片结果或错误，不记录传入 Cookie。
func (c fakeChatUploadClient) UploadChatImage(context.Context, string, string, string, []byte) (*mtop.ChatImageUpload, error) {
	if c.beforeReturn != nil {
		c.beforeReturn()
	}
	return c.upload, c.err
}

// fakeChatIdentityClient 只实现聊天身份查询，其余 MTOP 能力由嵌入接口占位。
type fakeChatIdentityClient struct {
	// mtop.Client 保留未涉及本切片的 MTOP 能力占位。
	mtop.Client
	// info 保存模拟平台返回的买家展示身份。
	info *mtop.ChatUserInfo
}

// sessionRefreshingIdentityClient 模拟身份查询通过请求 CookieSession 下发换签 Cookie 的平台响应。
type sessionRefreshingIdentityClient struct {
	// mtop.Client 保留未涉及身份查询的接口占位。
	mtop.Client
	// info 保存平台返回的非敏感对端身份。
	info *mtop.ChatUserInfo
	// snapshot 保存响应后应成为权威状态的完整 Cookie Jar。
	snapshot []cookierefresh.BrowserCookie
	// beforeResponse 在向 CookieSession 写入模拟响应前执行，用于模拟并发请求已更新凭证的时序。
	beforeResponse func()
}

// fakeChatRefreshFetcher 提供联系人和历史分页的最小运行时能力，用于隔离账号管理器与平台连接。
type fakeChatRefreshFetcher struct {
	// MessageSender 占位实现聊天发送端口未涉及的其余运行时方法。
	automation.MessageSender
}

// FetchChatConversations 返回空联系人页，验证刷新适配器调用边界。
func (fakeChatRefreshFetcher) FetchChatConversations(context.Context, int64, int) (map[string]any, string, error) {
	return map[string]any{"hasMore": true, "nextCursor": int64(8)}, "self", nil
}

// FetchChatHistory 返回空历史页，验证刷新适配器调用边界。
func (fakeChatRefreshFetcher) FetchChatHistory(context.Context, string, int64, int) (map[string]any, string, error) {
	return map[string]any{"hasMore": true, "nextCursor": int64(9)}, "self", nil
}

// TestChatRefreshProviderKeepsPlatformAndDomainInsideAdapter 验证平台分页与领域落库均停留在适配器内。
func TestChatRefreshProviderKeepsPlatformAndDomainInsideAdapter(t *testing.T) {
	// provider 保存使用内存领域仓储和测试运行时查找函数的刷新适配器。
	provider := chatRefreshProvider{
		service: domainchat.NewWithRepository(subscriptionDomainRepository{}),
		lookup:  func(string) (automation.MessageSender, bool) { return fakeChatRefreshFetcher{}, true },
	}
	// conversations 和 conversationErr 保存联系人刷新结果及错误。
	conversations, conversationErr := provider.RefreshConversations(context.Background(), "cid", 0, 10)
	if conversationErr != nil || !conversations.HasMore || conversations.NextCursor != 8 {
		t.Fatalf("conversations=%+v err=%v", conversations, conversationErr)
	}
	// history 和 historyErr 保存历史刷新结果及错误。
	history, historyErr := provider.RefreshHistory(context.Background(), "cid", "chat", 0, 10, chatapp.Session{AccountID: "cid", ChatID: "chat"})
	if historyErr != nil || !history.HasMore || history.NextCursor != 9 {
		t.Fatalf("history=%+v err=%v", history, historyErr)
	}
	// unavailable 保存缺失运行实例查找函数时的应用端口错误。
	unavailable := chatRefreshProvider{service: provider.service}
	// unavailableErr 保存缺失运行时查找函数时的应用端口错误。
	if _, unavailableErr := unavailable.RefreshConversations(context.Background(), "cid", 0, 10); !errors.Is(unavailableErr, chatapp.ErrRefreshUnavailable) {
		t.Fatalf("unavailable error=%v", unavailableErr)
	}
}

// FetchChatUserInfo 返回预设聊天身份，并记录适配器已能调用动态能力。
func (c fakeChatIdentityClient) FetchChatUserInfo(context.Context, string, string) (*mtop.ChatUserInfo, error) {
	return c.info, nil
}

// FetchChatUserInfo 将模拟响应 Cookie 吸收进请求会话，验证身份查询也会持久化 Token 换签。
func (c sessionRefreshingIdentityClient) FetchChatUserInfo(ctx context.Context, _ string, _ string) (*mtop.ChatUserInfo, error) {
	// session 保存本次平台调用附带的 CookieSession；缺失表示适配器未建立凭证收口边界。
	session := mtop.CookieSessionFromContext(ctx)
	if session == nil {
		return nil, errors.New("身份查询缺少 CookieSession")
	}
	if c.beforeResponse != nil {
		c.beforeResponse()
	}
	session.ReplaceSnapshot(c.snapshot)
	return c.info, nil
}

// TestChatRepositoryMapsSessionMaintenance 验证聊天数据库适配器覆盖列表、清理、身份和归属端口。
func TestChatRepositoryMapsSessionMaintenance(t *testing.T) {
	// store 是使用临时 SQLite 数据库的测试存储。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// ctx 是本测试数据库操作使用的非取消上下文。
	ctx := context.Background()
	// repository 是聊天应用层会话端口的数据库实现。
	repository := NewChatRepository(store)
	// owner 是模板账号的所有者。
	owner, ownerErr := store.Users.GetByUsername(ctx, "admin")
	if ownerErr != nil {
		t.Fatal(ownerErr)
	}
	// session 是待写入数据库并转换回应用模型的非敏感会话。
	session := db.ChatSession{CookieID: "cid", ChatID: "chat-1", BuyerID: "buyer-1", BuyerName: "买家", LastMessage: "你好", LastMessageAt: 10}
	// saveErr 表示写入测试会话时的数据库错误。
	if saveErr := store.Chats.UpsertSession(ctx, session); saveErr != nil {
		t.Fatal(saveErr)
	}
	// port 是经类型断言确认的完整聊天维护端口。
	port, ok := repository.(chatapp.SessionRepository)
	if !ok {
		t.Fatal("聊天适配器未覆盖 SessionRepository")
	}
	// listed 和 listErr 保存应用层会话列表及转换错误。
	listed, listErr := port.ListSessions(ctx, owner.ID, "cid", 20)
	if listErr != nil || len(listed) != 1 || listed[0].PeerName != "买家" {
		t.Fatalf("会话列表映射异常 listed=%+v err=%v", listed, listErr)
	}
	// owned 和 ownershipErr 保存账号归属查询结果。
	owned, ownershipErr := port.ExistsOwned(ctx, owner.ID, "cid")
	if ownershipErr != nil || !owned {
		t.Fatalf("账号归属映射异常 owned=%v err=%v", owned, ownershipErr)
	}
	// updateErr 保存会话身份缓存写入错误。
	if updateErr := port.UpdateSessionIdentity(ctx, "cid", "chat-1", "buyer-1", "新名称", "avatar"); updateErr != nil {
		t.Fatal(updateErr)
	}
	// refreshed 和 refreshedErr 保存身份缓存写入后的会话列表。
	refreshed, refreshedErr := port.ListSessions(ctx, owner.ID, "cid", 20)
	if refreshedErr != nil || refreshed[0].PeerName != "新名称" || refreshed[0].PeerAvatar != "avatar" {
		t.Fatalf("身份缓存未更新 refreshed=%+v err=%v", refreshed, refreshedErr)
	}
	// emptyErr 保存删除无消息会话壳的结果。
	if emptyErr := port.DeleteEmptySessions(ctx, "cid"); emptyErr != nil {
		t.Fatal(emptyErr)
	}
}

// TestChatIdentityResolverKeepsCredentialsInsideAdapter 验证身份适配器只向应用层返回展示字段。
func TestChatIdentityResolverKeepsCredentialsInsideAdapter(t *testing.T) {
	// store 是包含测试账号 Cookie 的临时数据库。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// client 是返回非敏感身份的测试 MTOP 客户端。
	client := fakeChatIdentityClient{info: &mtop.ChatUserInfo{Nickname: "买家新名", AvatarURL: "https://example.invalid/avatar"}}
	// resolver 是读取 Cookie 并调用测试平台客户端的聊天身份适配器。
	resolver := NewChatIdentityResolver(store, func() mtop.Client { return client }, nil)
	// identity 和 resolveErr 保存适配器转换后的身份及查询错误。
	identity, resolveErr := resolver.Resolve(context.Background(), "cid", "chat-1")
	if resolveErr != nil || identity.PeerName != "买家新名" || identity.PeerAvatar == "" {
		t.Fatalf("身份映射异常 identity=%+v err=%v", identity, resolveErr)
	}
}

// TestChatIdentityResolverPersistsResponseCookie 验证联系人身份查询收到 Token 换签后会写回完整 Cookie Jar。
func TestChatIdentityResolverPersistsResponseCookie(t *testing.T) {
	// store、cleanup 保存测试专用数据库及其资源释放责任。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// snapshot 保存模拟平台下发的完整新 Cookie Jar，包含身份字段和新的签名 Token。
	snapshot := []cookierefresh.BrowserCookie{
		{Name: "unb", Value: "1", Domain: ".goofish.com", Path: "/"},
		{Name: "_m_h5_tk", Value: "fresh_token_1", Domain: ".goofish.com", Path: "/"},
	}
	// client 通过调用上下文中的 CookieSession 模拟 Token 过期响应后重新签发 Cookie。
	client := sessionRefreshingIdentityClient{info: &mtop.ChatUserInfo{Nickname: "买家新名"}, snapshot: snapshot}
	// resolver 是待验证的身份查询适配器；本用例不需要在线账号运行时同步。
	resolver := NewChatIdentityResolver(store, func() mtop.Client { return client }, nil)
	// identity、resolveErr 保存身份查询返回值和平台凭证写回结果。
	identity, resolveErr := resolver.Resolve(context.Background(), "cid", "chat-1")
	if resolveErr != nil || identity.PeerName != "买家新名" {
		t.Fatalf("identity=%+v err=%v", identity, resolveErr)
	}
	// runtimeData、dataErr 保存持久化后的最小运行时凭证视图；测试不读取或输出旧 Cookie。
	runtimeData, dataErr := store.Cookies.GetCookiePlatformRuntimeData(context.Background(), "cid")
	if dataErr != nil || !strings.Contains(runtimeData.Value, "_m_h5_tk=fresh_token_1") {
		t.Fatalf("身份查询未持久化换签 Cookie: err=%v", dataErr)
	}
	// persistedSnapshot、complete 保存 metadata 中的权威 Cookie Jar，避免回归为仅扁平 Cookie。
	persistedSnapshot, complete := cookierefresh.SnapshotFromMetadataOK(runtimeData.MetadataJSON)
	if !complete || len(persistedSnapshot) != len(snapshot) {
		t.Fatalf("身份查询未持久化完整 Cookie Jar: complete=%v count=%d", complete, len(persistedSnapshot))
	}
}

// TestChatIdentityResolverDoesNotFailWhenSiblingRefreshWins 验证并发联系人查询先后换签时，旧响应不会覆盖新凭证或让展示请求失败。
func TestChatIdentityResolverDoesNotFailWhenSiblingRefreshWins(t *testing.T) {
	// store、cleanup 保存测试数据库及其资源释放责任。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// winningCookie 是已由并发请求写入的较新扁平 Cookie，旧请求不得回写覆盖它。
	winningCookie := "unb=1; _m_h5_tk=winner_token_1"
	// losingSnapshot 是本次较旧请求随后收到的完整 Cookie Jar。
	losingSnapshot := []cookierefresh.BrowserCookie{{Name: "unb", Value: "1", Domain: ".goofish.com", Path: "/"}, {Name: "_m_h5_tk", Value: "loser_token_1", Domain: ".goofish.com", Path: "/"}}
	// client 在返回旧响应前写入胜出的凭证，模拟八个联系人并发补全中的竞争。
	client := sessionRefreshingIdentityClient{
		info:     &mtop.ChatUserInfo{Nickname: "买家新名"},
		snapshot: losingSnapshot,
		beforeResponse: func() {
			// updateErr 保存模拟并发写入胜出 Cookie 的持久化结果。
			if updateErr := store.Cookies.UpdateRenewalCookie(context.Background(), "cid", winningCookie, "", 1); updateErr != nil {
				t.Fatalf("准备并发凭证失败: %v", updateErr)
			}
		},
	}
	// resolver 是身份查询适配器；旧请求仍应返回非敏感身份。
	resolver := NewChatIdentityResolver(store, func() mtop.Client { return client }, nil)
	// identity、resolveErr 保存竞争发生后的展示结果及错误。
	identity, resolveErr := resolver.Resolve(context.Background(), "cid", "chat-1")
	if resolveErr != nil || identity.PeerName != "买家新名" {
		t.Fatalf("identity=%+v err=%v", identity, resolveErr)
	}
	// runtimeData、dataErr 保存最终凭证，必须保持已胜出的新值而非旧响应内容。
	runtimeData, dataErr := store.Cookies.GetCookiePlatformRuntimeData(context.Background(), "cid")
	if dataErr != nil || runtimeData.Value != winningCookie {
		t.Fatalf("旧身份查询覆盖了新凭证: err=%v", dataErr)
	}
}

// TestChatSendingAdaptersRejectUnavailableDependencies 验证实时聊天适配器的未装配错误分支。
func TestChatSendingAdaptersRejectUnavailableDependencies(t *testing.T) {
	// outgoingErr 保存未装配聊天领域服务时的稳定应用错误。
	_, outgoingErr := NewChatOutgoingRepository(nil).CreateOutgoing(context.Background(), chatapp.Session{}, "文本")
	if !errors.Is(outgoingErr, chatapp.ErrUnavailable) {
		t.Fatalf("nil outgoing service error=%v", outgoingErr)
	}
	// senderProvider 是未装配账号管理器的在线发送适配器。
	senderProvider := NewChatSenderProvider(nil)
	// sender、ok 保存未装配管理器时返回的发送器和存在性标记。
	if sender, ok := senderProvider.Sender("account-1"); ok || sender != nil {
		t.Fatalf("nil sender provider returned sender=%v ok=%v", sender, ok)
	}
	// uploadErr 保存未提供平台客户端时的稳定应用错误。
	_, uploadErr := NewChatImageUploader(nil, nil, nil).UploadChatImage(context.Background(), "account-1", "a.jpg", "image/jpeg", []byte("image"))
	if !errors.Is(uploadErr, chatapp.ErrUnavailable) {
		t.Fatalf("nil image client error=%v", uploadErr)
	}
}

// TestChatImageUploaderRejectsUnsupportedAndEmptyPlatformResults 验证图片上传适配器不会吞掉平台能力缺失或空结果。
func TestChatImageUploaderRejectsUnsupportedAndEmptyPlatformResults(t *testing.T) {
	// store 是包含测试 Cookie 的临时数据库，凭证只在适配器调用期间读取。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// unsupportedErr 保存客户端缺少图片上传能力时的稳定错误。
	_, unsupportedErr := NewChatImageUploader(store, func() mtop.Client { return fakeChatIdentityClient{} }, nil).UploadChatImage(context.Background(), "cid", "a.jpg", "image/jpeg", []byte("image"))
	if !errors.Is(unsupportedErr, chatapp.ErrUnavailable) {
		t.Fatalf("unsupported image client error=%v", unsupportedErr)
	}
	// emptyErr 保存平台返回空图片结果时的发送错误。
	_, emptyErr := NewChatImageUploader(store, func() mtop.Client { return fakeChatUploadClient{} }, nil).UploadChatImage(context.Background(), "cid", "a.jpg", "image/jpeg", []byte("image"))
	if !errors.Is(emptyErr, chatapp.ErrSend) {
		t.Fatalf("empty image result error=%v", emptyErr)
	}
}

// TestChatImageUploaderPreservesPlatformDimensions 验证图片尺寸从 MTOP 结果映射到应用端口且不被丢弃。
func TestChatImageUploaderPreservesPlatformDimensions(t *testing.T) {
	// store、cleanup 保存包含测试账号凭证的临时数据库及清理函数。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// client 返回带真实像素尺寸的平台上传结果。
	client := fakeChatUploadClient{upload: &mtop.ChatImageUpload{URL: "https://cdn.example/image.jpg", Width: 1920, Height: 1080}}
	// upload 保存应用适配器转换后的非敏感图片结果。
	upload, err := NewChatImageUploader(store, func() mtop.Client { return client }, nil).UploadChatImage(context.Background(), "cid", "a.jpg", "image/jpeg", []byte("image"))
	if err != nil || upload.URL == "" || upload.Width != 1920 || upload.Height != 1080 {
		t.Fatalf("upload=%+v err=%v", upload, err)
	}
}

// TestChatImageUploaderPersistsRefreshedCookieAndMapsPlatformErrors 验证刷新凭证写回、平台错误和持久化失败分支。
func TestChatImageUploaderPersistsRefreshedCookieAndMapsPlatformErrors(t *testing.T) {
	// store、cleanup 保存本测试使用的 SQLite 存储及清理函数。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// ctx 是本测试上传调用共用的非取消上下文。
	ctx := context.Background()
	// refreshedCookie 保存平台上传后返回的新 Cookie，不能向应用层泄露。
	refreshedCookie := "unb=1; _m_h5_tk=refreshed;"
	// client 保存返回新 Cookie 的平台图片上传替身。
	client := fakeChatUploadClient{upload: &mtop.ChatImageUpload{URL: "https://cdn.example/refreshed.jpg", UpdatedCookies: refreshedCookie}}
	// upload、uploadErr 保存刷新凭证后的图片结果和适配错误。
	upload, uploadErr := NewChatImageUploader(store, func() mtop.Client { return client }, nil).UploadChatImage(ctx, "cid", "a.jpg", "image/jpeg", []byte("image"))
	if uploadErr != nil || upload.URL == "" {
		t.Fatalf("刷新凭证上传结果=%+v err=%v", upload, uploadErr)
	}
	// persistedCookie、cookieErr 保存适配器写回数据库后的凭证读取结果。
	persistedCookie, cookieErr := store.Cookies.GetValue(ctx, "cid")
	if cookieErr != nil || persistedCookie != refreshedCookie {
		t.Fatalf("刷新凭证未写回 persisted=%q err=%v", persistedCookie, cookieErr)
	}
	// platformErr 是平台上传失败时应原样传出的业务错误。
	platformErr := errors.New("image upload failed")
	// _, uploadFailureErr 保存平台错误映射结果。
	_, uploadFailureErr := NewChatImageUploader(store, func() mtop.Client {
		return fakeChatUploadClient{err: platformErr}
	}, nil).UploadChatImage(ctx, "cid", "a.jpg", "image/jpeg", []byte("image"))
	if !errors.Is(uploadFailureErr, platformErr) {
		t.Fatalf("平台上传错误未透传: %v", uploadFailureErr)
	}
	// _, missingCookieErr 保存账号凭证不存在时的读取错误。
	_, missingCookieErr := NewChatImageUploader(store, func() mtop.Client { return client }, nil).UploadChatImage(ctx, "missing", "a.jpg", "image/jpeg", []byte("image"))
	if missingCookieErr == nil {
		t.Fatal("不存在账号的图片上传应返回凭证读取错误")
	}
	// persistenceClient 保存平台返回新 Cookie 但数据库已关闭时的上传替身。
	persistenceClient := fakeChatUploadClient{
		upload:       &mtop.ChatImageUpload{URL: "https://cdn.example/persist-error.jpg", UpdatedCookies: "unb=1; _m_h5_tk=again;"},
		beforeReturn: func() { _ = store.DB.Close() },
	}
	// _, persistenceErr 保存刷新凭证持久化失败时的适配错误。
	_, persistenceErr := NewChatImageUploader(store, func() mtop.Client { return persistenceClient }, nil).UploadChatImage(ctx, "cid", "a.jpg", "image/jpeg", []byte("image"))
	if persistenceErr == nil || strings.Contains(persistenceErr.Error(), "账号凭证已变化") {
		t.Fatal("数据库关闭后刷新凭证写回应返回错误")
	}
}

// TestChatImageUploaderRejectsStaleCookieWriteback 验证图片上传期间的新登录凭证不会被旧响应覆盖。
func TestChatImageUploaderRejectsStaleCookieWriteback(t *testing.T) {
	// store、cleanup 保存并发凭证测试使用的隔离数据库及释放函数。
	store, cleanup := newAdapterTestStore(t)
	defer cleanup()
	// latestCookie 表示平台请求期间新登录流程写入的权威凭证。
	latestCookie := "unb=1; _m_h5_tk=latest;"
	// client 返回旧响应并在返回前模拟新登录写入。
	client := fakeChatUploadClient{
		upload: &mtop.ChatImageUpload{URL: "https://cdn.example/stale.jpg", UpdatedCookies: "unb=1; _m_h5_tk=stale;"},
		beforeReturn: func() {
			// updateErr 保存模拟并发登录凭证写入错误。
			if updateErr := store.Cookies.UpdateValueOwned(context.Background(), "cid", latestCookie, 1); updateErr != nil {
				t.Fatalf("并发登录凭证写入失败: %v", updateErr)
			}
		},
	}
	// _, uploadErr 保存检测到凭证指纹冲突后的上传结果。
	_, uploadErr := NewChatImageUploader(store, func() mtop.Client { return client }, nil).UploadChatImage(context.Background(), "cid", "a.jpg", "image/jpeg", []byte("image"))
	if uploadErr == nil || !strings.Contains(uploadErr.Error(), "账号凭证已变化") {
		t.Fatalf("旧图片上传响应未被拒绝: %v", uploadErr)
	}
	// stored、storedErr 验证新登录凭证仍是数据库最终状态。
	stored, storedErr := store.Cookies.GetValue(context.Background(), "cid")
	if storedErr != nil || stored != latestCookie {
		t.Fatalf("旧图片上传响应覆盖了新登录凭证 stored=%q err=%v", stored, storedErr)
	}
}

var _ chatapp.SessionRepository = chatRepository{}
var _ chatapp.IdentityResolver = chatIdentityResolver{}
