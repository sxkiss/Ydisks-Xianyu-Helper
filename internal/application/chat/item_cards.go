package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

var (
	// ErrChatItemUnavailable 表示聊天商品查询端口未装配。
	ErrChatItemUnavailable = errors.New("聊天商品服务未启用")
	// ErrChatItemInvalid 表示查询参数或商品快照不符合对外契约。
	ErrChatItemInvalid = errors.New("聊天商品参数无效")
	// ErrChatItemForbidden 表示当前用户不拥有指定账号。
	ErrChatItemForbidden = errors.New("无权访问聊天账号")
	// ErrChatItemCreate 表示商品卡片尚未访问平台时，本地 sending 消息创建失败。
	ErrChatItemCreate = errors.New("聊天商品待发送消息保存失败")
)

// ChatItem 是应用层向 HTTP 返回的非敏感商品快照。
type ChatItem struct {
	// ItemID 是闲鱼商品标识。
	ItemID string
	// Title 是商品标题。
	Title string
	// ImageURL 是商品主图的 HTTP(S) 地址。
	ImageURL string
	// Price 是不带货币符号的平台价格文本。
	Price string
	// Description 是只用于选择列表的可选摘要。
	Description string
}

// ChatItemPage 是聊天商品选择器的应用层分页结果。
type ChatItemPage struct {
	// Items 是当前页商品。
	Items []ChatItem
	// Page 是从 1 开始的当前页码。
	Page int
	// HasMore 表示是否还可以请求下一页。
	HasMore bool
}

// ChatItemCatalog 定义个人会话商品查询所需的最小平台能力。
type ChatItemCatalog interface {
	// ListChatItems 按账号、会话、角色、搜索词和页码查询商品。
	ListChatItems(ctx context.Context, accountID, chatID, role, query string, page int) (ChatItemPage, error)
}

// ChatItemQuery 是已脱离 HTTP 参数的商品列表用例输入。
type ChatItemQuery struct {
	// UserID 是当前登录管理员标识，用于账号归属校验。
	UserID int64
	// AccountID 是闲鱼账号的本地稳定标识。
	AccountID string
	// ChatID 是当前个人会话标识。
	ChatID string
	// Role 是 peer 或 self，分别表示对方和当前账号的商品。
	Role string
	// Query 是最多 100 字的商品搜索词。
	Query string
	// Page 是从 1 开始的目标页码。
	Page int
}

// ItemCardInput 是已完成 HTTP 解码的商品卡片发送输入。
type ItemCardInput struct {
	// UserID 是当前登录管理员标识。
	UserID int64
	// AccountID 是执行发送的闲鱼账号标识。
	AccountID string
	// ChatID 是目标个人会话标识，对端身份由仓储解析。
	ChatID string
	// Item 是用户在当前列表选中的商品快照。
	Item ChatItem
}

// ItemSendingAvailable 报告商品卡片所需的会话、持久化和运行时端口是否已装配。
func (s *Service) ItemSendingAvailable() bool {
	if s == nil || s.repository == nil || s.outgoing == nil || s.senders == nil {
		return false
	}
	// ok 表示当前仓储是否提供按用户、账号和会话精确查询的窄能力。
	_, ok := s.repository.(SessionLookupRepository)
	return ok
}

// ItemCatalogAvailable 报告个人会话商品查询端口是否已完成装配。
func (s *Service) ItemCatalogAvailable() bool {
	return s != nil && s.repository != nil && s.itemCatalog != nil
}

// ListChatItems 在应用层完成归属与会话校验后查询平台商品。
func (s *Service) ListChatItems(ctx context.Context, input ChatItemQuery) (ChatItemPage, error) {
	// query 是去除首尾空白后的用例输入。
	query := input
	query.AccountID, query.ChatID, query.Role, query.Query = strings.TrimSpace(query.AccountID), strings.TrimSpace(query.ChatID), strings.TrimSpace(query.Role), strings.TrimSpace(query.Query)
	if query.UserID <= 0 || query.AccountID == "" || query.ChatID == "" || (query.Role != "peer" && query.Role != "self") || query.Page < 1 || len([]rune(query.Query)) > 100 {
		return ChatItemPage{}, ErrChatItemInvalid
	}
	if s == nil || s.itemCatalog == nil || s.repository == nil {
		return ChatItemPage{}, ErrChatItemUnavailable
	}
	// owned 和 ownershipErr 表示当前用户是否拥有目标账号及查询错误。
	owned, ownershipErr := s.OwnsAccount(ctx, query.UserID, query.AccountID)
	if ownershipErr != nil {
		return ChatItemPage{}, ownershipErr
	}
	if !owned {
		return ChatItemPage{}, ErrChatItemForbidden
	}
	// lookupErr 表示当前用户范围内的精确会话查询是否失败。
	if _, lookupErr := s.lookupOwnedSession(ctx, query.UserID, query.AccountID, query.ChatID); lookupErr != nil {
		return ChatItemPage{}, lookupErr
	}
	return s.itemCatalog.ListChatItems(ctx, query.AccountID, query.ChatID, query.Role, query.Query, query.Page)
}

// SendItemCard 使用本地会话中的对端身份创建、发送并收口一条商品卡片消息。
func (s *Service) SendItemCard(ctx context.Context, input ItemCardInput) (*Message, error) {
	// accountID 和 chatID 是经规范化的发送定位字段。
	accountID, chatID := strings.TrimSpace(input.AccountID), strings.TrimSpace(input.ChatID)
	// item 和 normalizeErr 是规范化后的商品快照及校验错误。
	item, normalizeErr := normalizeChatItem(input.Item)
	if input.UserID <= 0 || accountID == "" || chatID == "" || normalizeErr != nil {
		return nil, ErrChatItemInvalid
	}
	if !s.ItemSendingAvailable() {
		return nil, ErrUnavailable
	}
	// owned 和 ownershipErr 表示用户是否拥有发送账号。
	owned, ownershipErr := s.OwnsAccount(ctx, input.UserID, accountID)
	if ownershipErr != nil {
		return nil, ownershipErr
	}
	if !owned {
		return nil, ErrChatItemForbidden
	}
	// unlockOperation 阻止删除事务在商品卡片的平台投递和本地状态收口之间执行。
	unlockOperation := s.sessionOperations.lock(accountID, chatID)
	defer unlockOperation()
	// session 是按归属精确解析的会话，其 PeerUserID 不从 HTTP 请求接收。
	session, lookupErr := s.lookupOwnedSession(ctx, input.UserID, accountID, chatID)
	if lookupErr != nil {
		return nil, lookupErr
	}
	if strings.TrimSpace(session.PeerUserID) == "" {
		return nil, ErrChatSessionNotFound
	}
	// sender 和 ok 是当前账号的在线发送能力及存在性。
	sender, ok := s.senders.Sender(accountID)
	if !ok || sender == nil {
		return nil, ErrOffline
	}
	// itemSender 和 supported 表示当前在线发送器是否提供不扩大基础 Sender 的商品卡片窄能力。
	itemSender, supported := sender.(interface {
		SendItemCard(context.Context, string, string, ChatItem, string) error
	})
	if !supported {
		return nil, ErrUnavailable
	}
	// contentBytes 是按固定字段顺序序列化的本地商品卡片正文。
	contentBytes, marshalErr := json.Marshal(struct {
		ItemID   string `json:"item_id"`
		Title    string `json:"title"`
		ImageURL string `json:"image_url"`
		Price    string `json:"price"`
	}{ItemID: item.ItemID, Title: item.Title, ImageURL: item.ImageURL, Price: item.Price})
	if marshalErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrChatItemInvalid, marshalErr)
	}
	// content 是用于本地消息持久化与前端渲染的规范 JSON。
	content := string(contentBytes)
	// message 是发送前创建的 sending 状态消息。
	message, createErr := s.outgoing.CreateOutgoingMedia(ctx, session, "item", content)
	if createErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrChatItemCreate, createErr)
	}
	// sendErr 表示平台商品卡片发送是否失败。
	if sendErr := itemSender.SendItemCard(ctx, session.ChatID, session.PeerUserID, item, message.MessageKey); sendErr != nil {
		if errors.Is(sendErr, ErrSendUncertain) {
			// statusCtx 和 statusCancel 为商品卡片未知结果状态收口提供独立窗口。
			statusCtx, statusCancel := outgoingStatusContext(ctx)
			// uncertain 保存商品卡片未知结果状态写入结果。
			uncertain, _ := s.outgoing.SetOutgoingStatus(statusCtx, accountID, message.MessageKey, "uncertain")
			statusCancel()
			return messagePointer(uncertain, message), fmt.Errorf("%w: %v", ErrSendUncertain, sendErr)
		}
		// statusCtx 和 statusCancel 是平台失败后不受客户端断开影响的有界状态补偿上下文。
		statusCtx, statusCancel := outgoingStatusContext(ctx)
		// failed 是尽力标记为 failed 后的最新消息。
		failed, _ := s.outgoing.SetOutgoingStatus(statusCtx, accountID, message.MessageKey, "failed")
		statusCancel()
		return messagePointer(failed, message), fmt.Errorf("%w: %v", ErrSend, sendErr)
	}
	// statusCtx 和 statusCancel 是远端已成功后用于本地收口的有界上下文。
	statusCtx, statusCancel := outgoingStatusContext(ctx)
	// sent 和 statusErr 是最终 sent 消息及可能的本地写入错误。
	sent, statusErr := s.outgoing.SetOutgoingStatus(statusCtx, accountID, message.MessageKey, "sent")
	statusCancel()
	if statusErr != nil {
		return messagePointer(sent, message), fmt.Errorf("%w: %v", ErrStatusSave, statusErr)
	}
	return messagePointer(sent, message), nil
}

// lookupOwnedSession 通过窄仓储端口精确读取当前用户的会话。
func (s *Service) lookupOwnedSession(ctx context.Context, userID int64, accountID, chatID string) (Session, error) {
	// repository 和 ok 是否具备精确会话查询能力的消费者窄接口。
	repository, ok := s.repository.(SessionLookupRepository)
	if !ok {
		return Session{}, ErrSessionUnavailable
	}
	// session 和 lookupErr 是归属过滤后的会话及查询错误。
	session, lookupErr := repository.FindSession(ctx, userID, accountID, chatID)
	if lookupErr != nil {
		if errors.Is(lookupErr, ErrChatSessionNotFound) {
			return Session{}, ErrChatSessionNotFound
		}
		return Session{}, lookupErr
	}
	if strings.TrimSpace(session.ChatID) == "" {
		return Session{}, ErrChatSessionNotFound
	}
	return session, nil
}

// normalizeChatItem 校验并归一化列表快照，返回可直接持久化和发送的商品。
func normalizeChatItem(input ChatItem) (ChatItem, error) {
	// item 是去除所有字段首尾空白后的商品快照。
	item := ChatItem{ItemID: strings.TrimSpace(input.ItemID), Title: strings.TrimSpace(input.Title), ImageURL: strings.TrimSpace(input.ImageURL), Price: strings.TrimSpace(input.Price)}
	item.Price = strings.TrimSpace(strings.TrimPrefix(item.Price, "¥"))
	if strings.HasPrefix(item.ImageURL, "//") {
		item.ImageURL = "https:" + item.ImageURL
	}
	if item.ItemID == "" || len([]rune(item.ItemID)) > 128 || item.Title == "" || len([]rune(item.Title)) > 200 || item.ImageURL == "" || len([]rune(item.ImageURL)) > 2048 || item.Price == "" || len([]rune(item.Price)) > 64 || containsControl(item.ItemID+item.Title+item.Price) {
		return ChatItem{}, ErrChatItemInvalid
	}
	// parsedURL 和 parseErr 是用于限制主图协议的标准 URL 解析结果。
	parsedURL, parseErr := url.Parse(item.ImageURL)
	if parseErr != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return ChatItem{}, ErrChatItemInvalid
	}
	return item, nil
}

// containsControl 报告文本是否包含不允许进入聊天商品卡片的控制字符。
func containsControl(value string) bool {
	for _ /* character 是当前检查是否属于控制类别的 Unicode 字符。 */, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
