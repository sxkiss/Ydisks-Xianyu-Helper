package chat

import (
	"context"
	"errors"
	"testing"
	"time"
)

// blockingItemSender 在商品卡片平台调用期间暂停，用于确定化发送与删除竞态。
type blockingItemSender struct {
	// sendSender 提供文本、图片和凭证兼容方法。
	*sendSender
	// started 在商品发送取得会话操作权后通知测试。
	started chan struct{}
	// release 控制平台商品发送何时完成。
	release chan struct{}
}

// SendItemCard 暂停到测试释放后返回成功。
func (sender *blockingItemSender) SendItemCard(ctx context.Context, _, _ string, _ ChatItem, _ string) error {
	close(sender.started)
	select {
	case <-sender.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// blockingDeletionRepository 记录删除是否已经穿过应用层会话操作门。
type blockingDeletionRepository struct {
	// itemRepository 提供归属和精确会话查询能力。
	*itemRepository
	// ownershipChecked 在删除完成账号归属校验后通知测试，证明请求已经到达会话操作门。
	ownershipChecked chan struct{}
	// deleteCalled 在底层清空真正开始时通知测试。
	deleteCalled chan struct{}
}

// ExistsOwned 复用预设归属结果，并通知测试删除请求已经完成门前校验。
func (repository *blockingDeletionRepository) ExistsOwned(ctx context.Context, userID int64, accountID string) (bool, error) {
	// owned 和 ownershipErr 是底层测试仓储返回的归属结果。
	owned, ownershipErr := repository.itemRepository.ExistsOwned(ctx, userID, accountID)
	select {
	case <-repository.ownershipChecked:
	default:
		close(repository.ownershipChecked)
	}
	return owned, ownershipErr
}

// HideAndClearSession 标记删除调用已经进入仓储，并返回幂等成功。
func (repository *blockingDeletionRepository) HideAndClearSession(context.Context, int64, string, string, int64) (bool, error) {
	close(repository.deleteCalled)
	return true, nil
}

// itemRepository 在通用聊天测试仓储上提供精确会话查询能力。
type itemRepository struct {
	// fakeRepository 提供账号归属和基础会话仓储能力。
	*fakeRepository
	// session 是当前用户和账号下应返回的精确会话。
	session Session
	// findErr 是精确会话查询需要返回的错误。
	findErr error
}

// FindSession 返回预设会话，并记录不在仓储层暴露凭证的精确归属行为。
func (r *itemRepository) FindSession(_ context.Context, userID int64, accountID, chatID string) (Session, error) {
	if r.findErr != nil {
		return Session{}, r.findErr
	}
	if userID <= 0 || accountID != r.session.AccountID || chatID != r.session.ChatID {
		return Session{}, ErrChatSessionNotFound
	}
	return r.session, nil
}

// itemCatalog 记录商品查询参数并返回预设页面或错误。
type itemCatalog struct {
	// result 是平台商品查询成功时返回的非敏感分页。
	result ChatItemPage
	// err 是商品查询需要返回的平台错误。
	err error
	// accountID、chatID、role 和 query 保存最近一次查询参数。
	accountID, chatID, role, query string
	// page 保存最近一次查询页码。
	page int
}

// ListChatItems 记录应用层传入的平台查询参数并返回预设结果。
func (catalog *itemCatalog) ListChatItems(_ context.Context, accountID, chatID, role, query string, page int) (ChatItemPage, error) {
	catalog.accountID, catalog.chatID, catalog.role, catalog.query, catalog.page = accountID, chatID, role, query, page
	return catalog.result, catalog.err
}

// TestListChatItemsChecksOwnershipAndExactSession 验证查询在平台调用前完成账号归属和精确会话校验。
func TestListChatItemsChecksOwnershipAndExactSession(t *testing.T) {
	// session 是当前用户拥有账号下的目标个人会话。
	session := Session{AccountID: "account-1", ChatID: "chat-1", PeerUserID: "buyer-from-store"}
	// repository 是允许当前用户访问目标会话的测试仓储。
	repository := &itemRepository{fakeRepository: &fakeRepository{owned: true}, session: session}
	// catalog 是返回固定对方商品页的平台目录替身。
	catalog := &itemCatalog{result: ChatItemPage{Items: []ChatItem{{ItemID: "item-1", Title: "测试商品"}}, Page: 2, HasMore: true}}
	// service 是装配精确会话仓储和商品目录的聊天应用服务。
	service := WithChatItemCatalog(New(repository), catalog)
	// result 和 listErr 是合法查询的应用结果。
	result, listErr := service.ListChatItems(context.Background(), ChatItemQuery{UserID: 7, AccountID: " account-1 ", ChatID: " chat-1 ", Role: "peer", Query: " 麦克风 ", Page: 2})
	if listErr != nil || len(result.Items) != 1 || catalog.accountID != "account-1" || catalog.chatID != "chat-1" || catalog.role != "peer" || catalog.query != "麦克风" || catalog.page != 2 {
		t.Fatalf("result=%+v catalog=%+v err=%v", result, catalog, listErr)
	}
	repository.owned = false
	// forbiddenErr 是账号不归属于当前用户时的拒绝结果。
	_, forbiddenErr := service.ListChatItems(context.Background(), ChatItemQuery{UserID: 7, AccountID: "account-1", ChatID: "chat-1", Role: "peer", Page: 1})
	if !errors.Is(forbiddenErr, ErrChatItemForbidden) {
		t.Fatalf("账号越权错误=%v", forbiddenErr)
	}
	repository.owned = true
	repository.findErr = ErrChatSessionNotFound
	// missingErr 是目标会话不存在时的查询结果。
	_, missingErr := service.ListChatItems(context.Background(), ChatItemQuery{UserID: 7, AccountID: "account-1", ChatID: "chat-1", Role: "self", Page: 1})
	if !errors.Is(missingErr, ErrChatSessionNotFound) {
		t.Fatalf("会话不存在错误=%v", missingErr)
	}
}

// TestSendItemCardUsesStoredPeerAndCanonicalContent 验证发送目标来自仓储且本地保存规范商品 JSON。
func TestSendItemCardUsesStoredPeerAndCanonicalContent(t *testing.T) {
	// repository 是记录商品 sending/sent 状态迁移的出站仓储。
	repository := &sendRepository{}
	// sender 是记录商品目标和字段的平台发送器。
	sender := &sendSender{}
	// sessionRepository 是当前用户拥有账号下的精确会话仓储。
	sessionRepository := &itemRepository{fakeRepository: &fakeRepository{owned: true}, session: Session{AccountID: "account-1", ChatID: "chat-1", PeerUserID: "stored-buyer"}}
	// service 是支持商品卡片发送的聊天应用服务。
	service := NewWithSending(sessionRepository, repository, sendProvider{sender: sender}, nil)
	// message 和 sendErr 是商品卡片发送及本地状态收口结果。
	message, sendErr := service.SendItemCard(context.Background(), ItemCardInput{UserID: 7, AccountID: "account-1", ChatID: "chat-1", Item: ChatItem{ItemID: " item-1 ", Title: " 测试商品 ", ImageURL: "//img.example/item.png", Price: " ¥ 19.90 "}})
	if sendErr != nil || message == nil || message.Status != "sent" || message.MessageType != "item" {
		t.Fatalf("message=%+v err=%v", message, sendErr)
	}
	if sender.itemPeerUserID != "stored-buyer" || sender.itemChatID != "chat-1" || sender.itemKey != "local-image" || sender.item.ImageURL != "https://img.example/item.png" || sender.item.Price != "19.90" {
		t.Fatalf("sender=%+v", sender)
	}
	// wantContent 是本地消息必须保存的固定字段顺序商品 JSON。
	wantContent := `{"item_id":"item-1","title":"测试商品","image_url":"https://img.example/item.png","price":"19.90"}`
	if message.Content != wantContent || len(repository.statuses) != 1 || repository.statuses[0] != "sent" {
		t.Fatalf("content=%s statuses=%v", message.Content, repository.statuses)
	}
}

// TestDeleteConversationWaitsForInFlightItemSend 验证同一会话的删除必须在商品发送和状态收口完成后执行。
func TestDeleteConversationWaitsForInFlightItemSend(t *testing.T) {
	// started、release、ownershipChecked 和 deleteCalled 控制发送与删除的确定性执行顺序。
	started, release, ownershipChecked, deleteCalled := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	// repository 同时提供商品发送会话查询和删除观察能力。
	repository := &blockingDeletionRepository{itemRepository: &itemRepository{fakeRepository: &fakeRepository{owned: true}, session: Session{AccountID: "account-1", ChatID: "chat-1", PeerUserID: "buyer-1"}}, ownershipChecked: ownershipChecked, deleteCalled: deleteCalled}
	// sender 是会在平台投递阶段暂停的商品发送器。
	sender := &blockingItemSender{sendSender: &sendSender{}, started: started, release: release}
	// service 是发送和删除共享同一会话操作门的应用服务。
	service := NewWithSending(repository, &sendRepository{}, sendProvider{sender: sender}, nil)
	// sendResult 接收异步商品发送结果。
	sendResult := make(chan error, 1)
	go func() {
		// sendErr 是异步商品发送的最终错误。
		_, sendErr := service.SendItemCard(context.Background(), ItemCardInput{UserID: 7, AccountID: "account-1", ChatID: "chat-1", Item: ChatItem{ItemID: "item-1", Title: "商品", ImageURL: "https://img.example/item.png", Price: "10"}})
		sendResult <- sendErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("商品发送未进入平台调用")
	}
	// deleteResult 接收与发送并发启动的删除结果。
	deleteResult := make(chan error, 1)
	go func() {
		deleteResult <- service.DeleteConversation(context.Background(), 7, "account-1", "chat-1")
	}()
	select {
	case <-ownershipChecked:
	case <-time.After(time.Second):
		t.Fatal("删除请求未完成归属校验")
	}
	select {
	case <-deleteCalled:
		t.Fatal("发送完成前删除不应进入仓储")
	default:
	}
	close(release)
	// sendErr 是释放平台调用后商品发送和本地状态收口的结果。
	if sendErr := <-sendResult; sendErr != nil {
		t.Fatal(sendErr)
	}
	select {
	case <-deleteCalled:
	case <-time.After(time.Second):
		t.Fatal("发送完成后删除未继续执行")
	}
	// deleteErr 是发送完成后继续执行的会话删除结果。
	if deleteErr := <-deleteResult; deleteErr != nil {
		t.Fatal(deleteErr)
	}
}

// TestSendItemCardCoversOfflinePlatformAndStatusFailures 验证离线、平台失败和状态收口失败语义。
func TestSendItemCardCoversOfflinePlatformAndStatusFailures(t *testing.T) {
	// sessionRepository 是所有发送分支共享的已归属会话仓储。
	sessionRepository := &itemRepository{fakeRepository: &fakeRepository{owned: true}, session: Session{AccountID: "account-1", ChatID: "chat-1", PeerUserID: "buyer-1"}}
	// input 是所有发送分支共享的合法商品快照。
	input := ItemCardInput{UserID: 7, AccountID: "account-1", ChatID: "chat-1", Item: ChatItem{ItemID: "item-1", Title: "测试商品", ImageURL: "https://img.example/item.png", Price: "10"}}
	// offlineService 是无法解析在线账号发送器的应用服务。
	offlineService := NewWithSending(sessionRepository, &sendRepository{}, sendProvider{}, nil)
	// offlineErr 是账号没有在线发送实例时的应用错误。
	if _, offlineErr := offlineService.SendItemCard(context.Background(), input); !errors.Is(offlineErr, ErrOffline) {
		t.Fatalf("离线错误=%v", offlineErr)
	}
	// failedRepository 和 failedSender 模拟平台拒绝并记录 failed 状态。
	failedRepository, failedSender := &sendRepository{}, &sendSender{sendErr: errors.New("平台拒绝")}
	// failedService 是平台发送失败场景的应用服务。
	failedService := NewWithSending(sessionRepository, failedRepository, sendProvider{sender: failedSender}, nil)
	// failedMessage 和 platformErr 是平台失败后的本地消息与错误。
	failedMessage, platformErr := failedService.SendItemCard(context.Background(), input)
	if !errors.Is(platformErr, ErrSend) || failedMessage == nil || failedMessage.Status != "failed" {
		t.Fatalf("message=%+v err=%v", failedMessage, platformErr)
	}
	// createService 模拟平台调用前的 sending 消息创建失败。
	createService := NewWithSending(sessionRepository, &sendRepository{createErr: errors.New("数据库不可用")}, sendProvider{sender: &sendSender{}}, nil)
	// createErr 是待发送商品消息创建失败的稳定应用错误。
	if _, createErr := createService.SendItemCard(context.Background(), input); !errors.Is(createErr, ErrChatItemCreate) {
		t.Fatalf("待发送消息创建错误=%v", createErr)
	}
	// statusRepository 模拟平台成功后的本地 sent 状态写入失败。
	statusRepository := &sendRepository{statusErr: errors.New("状态写入失败")}
	// statusService 是状态收口失败场景的应用服务。
	statusService := NewWithSending(sessionRepository, statusRepository, sendProvider{sender: &sendSender{}}, nil)
	// uncertainMessage 和 statusErr 是不可自动重发的确认消息与收口错误。
	uncertainMessage, statusErr := statusService.SendItemCard(context.Background(), input)
	if !errors.Is(statusErr, ErrStatusSave) || uncertainMessage == nil || uncertainMessage.MessageKey != "local-image" {
		t.Fatalf("message=%+v err=%v", uncertainMessage, statusErr)
	}
}

// TestNormalizeChatItemRejectsUnsafeSnapshots 验证商品快照长度、图片协议和控制字符边界。
func TestNormalizeChatItemRejectsUnsafeSnapshots(t *testing.T) {
	// cases 是必须在任何持久化或平台调用前拒绝的商品快照。
	cases := []ChatItem{
		{Title: "缺少标识", ImageURL: "https://img.example/a.png", Price: "1"},
		{ItemID: "1", Title: "非法图片", ImageURL: "javascript:alert(1)", Price: "1"},
		{ItemID: "1", Title: "控制字符", ImageURL: "https://img.example/a.png", Price: "1\n2"},
	}
	for _ /* item 是当前待拒绝的危险或不完整商品快照。 */, item := range cases {
		if _, itemErr := normalizeChatItem(item); !errors.Is(itemErr, ErrChatItemInvalid) {
			t.Fatalf("item=%+v err=%v", item, itemErr)
		}
	}
}
