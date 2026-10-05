package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"xianyu-go/internal/xianyu/protocol"
)

// dispatch 是 ws.ReceiveLoop 的回调，对每条解密后的消息做：
// 标记消息接收时间 → 提取消息 ID 去重 → 信号量限并发 → 分类（聊天/系统）→ 防抖投递。
// dispatch 封装dispatch业务协调。
func (a *Account) dispatch(decrypted map[string]any) {
	a.messageDispatcher.dispatch(decrypted)
}

// handleMessage 分类并投递消息。
func (a *Account) handleMessage(decrypted map[string]any) {
	a.messageDispatcher.handleMessage(decrypted)
}

// extractMessageReadEvent 从解密 WebSocket 事件中提取出站消息已读回执。
func extractMessageReadEvent(v map[string]any) (MessageReadEvent, bool) {
	// 40103 解密后不会保留外层 objectType，只剩下固定的紧凑字段：
	// 1=PNM 消息 ID、2=状态(2=已读)、4=会话 ID、6=事件时间。
	// 不能只在解密后的 map 里继续寻找 "bizType":40103，否则真实回执
	// 会被当成普通消息静默丢弃。
	// messageID 保存紧凑回执或嵌套事件中的平台 PNM 消息 ID。
	messageID := strings.TrimSpace(toString(v["1"]))
	// ids 保存批量回执携带的 PNM ID 列表；ok 表示字段 1 是否采用批量编码。
	if ids, ok := v["1"].([]any); ok && len(ids) > 0 {
		messageID = strings.TrimSpace(toString(ids[0]))
	}
	if strings.HasSuffix(messageID, ".PNM") && toString(v["2"]) == "2" {
		// chatID 保存紧凑回执携带的会话 ID，旧信封缺失时回退字段 3。
		chatID := strings.TrimSpace(toString(v["4"]))
		if !strings.Contains(chatID, "@goofish") {
			chatID = strings.TrimSpace(toString(v["3"]))
		}
		chatID = strings.TrimSuffix(chatID, "@goofish")
		return MessageReadEvent{ChatID: chatID, MessageID: messageID, ReadAt: time.Now().UTC().UnixMilli()}, true
	}
	// found 标记已定位 40103 信封；ev 累积其会话和消息标识；walk 递归读取兼容嵌套结构。
	var found bool
	// ev 保存从嵌套兼容回执中提取的平台会话与消息标识。
	var ev MessageReadEvent
	// walk 解析对象、数组或 JSON 字符串形式的嵌套事件，命中后停止后续遍历。
	var walk func(any)
	walk = func(x any) {
		if found {
			return
		}
		// m 保存当前遍历节点被识别出的具体 JSON 数据类型。
		switch m := x.(type) {
		case map[string]any:
			// k 为当前字段名；val 为其原始 JSON 值，用来定位回执事件类型。
			for k, val := range m {
				// lk 是小写化字段名，兼容平台信封的不同字段命名。
				lk := strings.ToLower(k)
				if lk == "biztype" || lk == "biz_type" || lk == "type" {
					if toString(val) == "40103" {
						found = true
					}
				}
			}
			if found {
				ev.MessageID = extractNestedString(m, "messageid", "message_id")
				ev.ChatID = extractNestedString(m, "cid", "chatid", "chat_id")
				if ev.MessageID == "" {
					ev.MessageID = extractNestedString(m, "id")
				}
				return
			}
			// child 为当前对象中的嵌套值，继续搜索未解包的回执信封。
			for _, child := range m {
				walk(child)
			}
		case []any:
			// child 为当前批量信封中的一个候选事件。
			for _, child := range m {
				walk(child)
			}
		case string:
			// 部分同步信封将事件体编码为转义 JSON 字符串，而非已解码对象。
			// decoded 保存反序列化后的事件体，兼容字符串包装的同步消息。
			var decoded any
			if json.Unmarshal([]byte(m), &decoded) == nil {
				walk(decoded)
			}
		}
	}
	walk(v)
	if !found || ev.MessageID == "" {
		return MessageReadEvent{}, false
	}
	return ev, true
}

// extractNestedString 在嵌套信封中按大小写无关键名查找第一个非空字符串。
func extractNestedString(m map[string]any, keys ...string) string {
	// k 是当前字段名；v 是字段原值，用于大小写无关地匹配候选键。
	for k, v := range m {
		// want 为调用方允许的目标字段名。
		for _, want := range keys {
			if strings.EqualFold(k, want) && strings.TrimSpace(toString(v)) != "" {
				return strings.TrimSpace(toString(v))
			}
		}
	}
	// v 为当前对象中的嵌套值，继续查询其中的兼容子信封。
	for _, v := range m {
		// child 为可继续递归解析的对象；ok 表示当前值是否是对象结构。
		if child, ok := v.(map[string]any); ok {
			// s 保存子信封中找到的第一个非空目标字段值。
			if s := extractNestedString(child, keys...); s != "" {
				return s
			}
		}
	}
	return ""
}

// markAndCheckDedup 提取消息 ID，检查 1 小时内是否已处理；未处理则标记。
// 返回 true 表示应继续处理。移植自 _schedule_debounced_reply 的去重段。
// markAndCheckDedup 封装markAndCheckDedup业务协调。
func (a *Account) markAndCheckDedup(decrypted map[string]any, chat *ChatMessage) bool {
	return a.messageDispatcher.markAndCheckDedup(decrypted, chat)
}

// cleanupDedupLocked 封装cleanupDedupLocked业务协调。
func (a *Account) cleanupDedupLocked(now time.Time) {
	a.messageDispatcher.cleanupDedupLocked(now)
}

// scheduleDebouncedReply 为 chat_id 调度防抖回复：
// 同一 chat_id 连续来消息时取消旧定时器、用最新消息重新计时，1s 后投递最后一条。
// scheduleDebouncedReply 封装scheduleDebounced回复业务协调。
func (a *Account) scheduleDebouncedReply(chat ChatMessage) {
	a.messageDispatcher.scheduleDebouncedReply(chat)
}

// extractMessageID 提取闲鱼消息状态接口使用的消息 ID。
//
// 实时 WS 聊天消息同时包含两种 ID：message["1"]["3"] 是消息在 IM
// 服务中的 PNM ID，/r/MessageStatus/read 使用的正是这个 ID；10.bizTag、
// extJson 和 reminderUrl 里的 messageId 是通知/推送侧的关联 ID，不能用来
// 上报会话已读。历史消息接口也返回 PNM ID，因此优先使用字段 3，其他
// 字段只作为兼容旧消息或缺失字段 3 的兜底。
func extractMessageID(decrypted map[string]any) string {
	// m1、ok 用于本次流程后续判断的m1、ok
	m1, ok := decrypted["1"].(map[string]any)
	if !ok {
		return ""
	}
	// id 保存实时消息最可靠的 PNM ID，存在时不可用推送关联 ID 覆盖。
	if id := strings.TrimSpace(toString(m1["3"])); id != "" && id != "<nil>" {
		return id
	}
	// platformID 在协议字段变体下寻找消息模型返回的 PNM，避免实时最新消息退回关联 UUID。
	if platformID := findPNMMessageID(decrypted); platformID != "" {
		return platformID
	}
	// m10、ok 保存消息展示扩展及其是否存在，用于兼容旧消息关联 ID。
	m10, ok := m1["10"].(map[string]any)
	if !ok {
		return ""
	}
	// bizTag 是 JSON 字符串：{"sourceId":"...","messageId":"..."}
	if biz, _ := m10["bizTag"].(string); biz != "" {
		if // id 用于本次流程后续判断的标识
		id := parseMessageIDFromJSON(biz); id != "" {
			return id
		}
	}
	if // ext 用于本次流程后续判断的ext
	ext, _ := m10["extJson"].(string); ext != "" {
		if // id 用于本次流程后续判断的标识
		id := parseMessageIDFromJSON(ext); id != "" {
			return id
		}
	}
	// reminderURL 保存提醒链接，旧消息会将推送关联 ID 编码在其查询参数中。
	if reminderURL, _ := m10["reminderUrl"].(string); reminderURL != "" {
		// parsed 保存已解析的提醒链接；err 表示 URL 格式不合法时的解析失败。
		if parsed, err := url.Parse(reminderURL); err == nil {
			// id 保存 reminderUrl 查询参数中的兼容消息关联 ID。
			if id := strings.TrimSpace(parsed.Query().Get("messageId")); id != "" {
				return id
			}
		}
	}
	return findMessageID(decrypted)
}

// findPNMMessageID 递归寻找闲鱼消息模型使用的 PNM 标识。
func findPNMMessageID(value any) string {
	// current 是当前递归层待检查的协议值。
	switch current := value.(type) {
	case map[string]any:
		// keys 按协议语义优先级和字典序排列，避免 map 随机遍历决定消息归属。
		keys := orderedMessageKeys(current)
		// key 是按协议优先级选出的当前字段名。
		for _, key := range keys {
			// child 是当前字段对应的嵌套值。
			child := current[key]
			// id 是子值递归解析得到的消息模型标识。
			if id := findPNMMessageID(child); id != "" {
				return id
			}
		}
	case []any:
		// child 是当前数组元素的嵌套协议值。
		for _, child := range current {
			// id 是数组元素递归解析得到的消息模型标识。
			if id := findPNMMessageID(child); id != "" {
				return id
			}
		}
	case string:
		// id 是当前字符串去除空白后的候选消息模型标识。
		id := strings.TrimSpace(current)
		if strings.HasSuffix(id, ".PNM") {
			return id
		}
		// decoded 兼容平台把消息模型嵌在 JSON 字符串中的实时帧格式。
		var decoded any
		if json.Unmarshal([]byte(current), &decoded) == nil {
			return findPNMMessageID(decoded)
		}
	}
	return ""
}

// orderedMessageKeys 返回稳定的消息模型字段顺序；未知字段按字典序作为兼容兜底。
func orderedMessageKeys(values map[string]any) []string {
	// priority 是平台消息模型中应优先递归的字段顺序。
	const priority = "1,message,messageModel,data,body,content,payload,model,10,extJson,bizTag,reminderUrl"
	// priorityKeys 保存拆分后的优先字段名。
	priorityKeys := strings.Split(priority, ",")
	// seen 记录已加入优先顺序的字段，避免未知字段重复加入。
	seen := make(map[string]struct{}, len(values))
	// keys 保存最终稳定的字段遍历顺序。
	keys := make([]string, 0, len(values))
	// key 是当前优先字段名。
	for _, key := range priorityKeys {
		// ok 表示当前字段是否存在于输入消息中。
		if _, ok := values[key]; ok {
			keys = append(keys, key)
			seen[key] = struct{}{}
		}
	}
	// remaining 保存未命中协议优先级、需要按字典序补充的字段名。
	remaining := make([]string, 0, len(values)-len(keys))
	// key 是输入消息中的任意字段名。
	for key := range values {
		// ok 表示当前字段是否已经被优先顺序收录。
		if _, ok := seen[key]; !ok {
			remaining = append(remaining, key)
		}
	}
	sort.Strings(remaining)
	return append(keys, remaining...)
}

// findMessageID 递归解析兼容消息信封中可能存在的关联消息 ID。
func findMessageID(value any) string {
	// x 保存当前待搜索节点的具体值，按对象、数组和 JSON 字符串分别处理。
	switch x := value.(type) {
	case map[string]any:
		// key 是对象字段名；child 是字段值，用于先检查本层关联 ID。
		for key, child := range x {
			if strings.EqualFold(key, "messageId") || strings.EqualFold(key, "message_id") {
				// id 保存当前字段提供的非空关联消息 ID。
				if id := strings.TrimSpace(fmt.Sprint(child)); id != "" && id != "<nil>" {
					return id
				}
			}
		}
		// child 为本层嵌套字段值，递归兼容更深层信封。
		for _, child := range x {
			// id 保存子节点中首先发现的关联消息 ID。
			if id := findMessageID(child); id != "" {
				return id
			}
		}
	case []any:
		// child 为批量消息中的一个候选事件或子信封。
		for _, child := range x {
			// id 保存数组成员中首先发现的关联消息 ID。
			if id := findMessageID(child); id != "" {
				return id
			}
		}
	case string:
		// decoded 保存从 JSON 字符串解码后的兼容嵌套结构。
		var decoded any
		if json.Unmarshal([]byte(x), &decoded) == nil {
			return findMessageID(decoded)
		}
	}
	return ""
}

// parseMessageIDFromJSON 封装parse消息IDFromJSON业务协调。
func parseMessageIDFromJSON(s string) string {
	// m 用于本次流程后续判断的m
	var m map[string]any
	if // err 用于本次流程后续判断的err
	err := json.Unmarshal([]byte(s), &m); err != nil {
		return ""
	}
	if // id、ok 用于本次流程后续判断的id、ok
	id, ok := m["messageId"].(string); ok {
		return id
	}
	return ""
}

// extractChatMessage 从解密消息中提取聊天消息字段。
func extractChatMessage(decrypted map[string]any, accountID, cookieStr string) *ChatMessage {
	// m1、ok 用于本次流程后续判断的m1、ok
	m1, ok := decrypted["1"].(map[string]any)
	if !ok {
		return nil
	}
	// m10 用于本次流程后续判断的m10
	m10, _ := m1["10"].(map[string]any)
	if m10 == nil {
		return nil
	}
	// reminder 用于本次流程后续判断的reminder
	reminder, _ := m10["reminderContent"].(string)
	if reminder == "" {
		return nil
	}
	if isNonUserChatNotice(m1, m10, reminder) {
		return nil
	}
	// chatID 用于本次流程后续判断的聊天ID
	chatID := toString(m1["2"])
	// chat_id 形如 "47983389096@goofish"，去掉后缀。
	if i := strings.Index(chatID, "@"); i >= 0 {
		chatID = chatID[:i]
	}
	// senderUserID 保存经兼容字段归一后的发送者身份。
	senderUserID := extractChatSenderUserIDFromMaps(m1, m10)
	if isSelfUserID(senderUserID, protocol.TransCookies(cookieStr)["unb"]) {
		// 官方客户端会通过同一 WebSocket 回显本账号发出的消息；它仅表示投递观察，
		// 并非买家新输入，绝不能进入自动回复链。
		return nil
	}
	// senderName 保存发送者展示名称，字段缺失时由提醒标题补齐。
	senderName, _ := m10["senderNick"].(string)
	if strings.TrimSpace(senderName) == "" {
		senderName, _ = m10["reminderTitle"].(string)
	}
	// reminderURL 用于本次流程后续判断的reminderURL
	reminderURL, _ := m10["reminderUrl"].(string)
	// itemID 用于本次流程后续判断的商品ID
	itemID := extractItemID(reminderURL)
	return &ChatMessage{
		AccountID:    accountID,
		CookieStr:    cookieStr,
		ChatID:       chatID,
		SenderUserID: senderUserID,
		SenderName:   senderName,
		Text:         reminder,
		MessageID:    extractMessageID(decrypted),
		ItemID:       itemID,
		Raw:          decrypted,
	}
}

// extractOwnWebSocketEcho 识别官方客户端发出后回显到当前账号 WebSocket 的普通消息。
// decrypted 是已解密平台帧；accountID 和 cookieStr 用于归属与自身身份判断；返回值仅供出站持久化观察，绝不进入自动回复链。
func extractOwnWebSocketEcho(decrypted map[string]any, accountID, cookieStr string) *OutgoingChatMessage {
	// m1 保存平台聊天信封主体；ok 表示帧是否具备普通聊天的顶层结构。
	m1, ok := decrypted["1"].(map[string]any)
	if !ok {
		return nil
	}
	// m10 保存消息展示扩展；缺失时无法安全取得文本和发送者身份。
	m10, _ := m1["10"].(map[string]any)
	if m10 == nil {
		return nil
	}
	// text 保存平台通知层给出的消息摘要；图片回显可能没有摘要，后续改用媒体正文判断。
	text, _ := m10["reminderContent"].(string)
	// contentType 保存平台回显的消息类型，用于区分文本和图片的匹配正文。
	contentType := messageContentType(m1, m10)
	if text == "" && contentType != "2" {
		return nil
	}
	if text != "" && isNonUserChatNotice(m1, m10, text) {
		return nil
	}
	// senderUserID 保存归一化后的发送者身份，必须与当前账号身份一致才是自身回显。
	senderUserID := extractChatSenderUserIDFromMaps(m1, m10)
	// selfUserID 保存当前账号的非敏感平台身份，仅用于区分自身回显与买家入站消息。
	selfUserID := protocol.TransCookies(cookieStr)["unb"]
	if !isSelfUserID(senderUserID, selfUserID) {
		return nil
	}
	// chatID 保存去除协议后缀后的会话标识，供出站消息与既有会话关联。
	chatID := toString(m1["2"])
	// suffixIndex 保存会话协议后缀的起始下标，存在时只保留平台原始会话 ID。
	if suffixIndex := strings.Index(chatID, "@"); suffixIndex >= 0 {
		chatID = chatID[:suffixIndex]
	}
	if strings.TrimSpace(chatID) == "" {
		return nil
	}
	// reminderURL 保存通知深链，用于从 peerUserId 恢复会话对端身份。
	reminderURL, _ := m10["reminderUrl"].(string)
	// buyerID 保存深链中的对端用户标识；它不能等于当前账号，缺失时由既有会话保留原身份。
	buyerID := extractChatPeerUserID(reminderURL, selfUserID)
	// messageType 和 content 默认保留文本摘要；图片和商品卡片改为规范媒体正文。
	messageType, content := "text", text
	switch contentType {
	case "2":
		messageType = "image"
		content = extractImageObservationContent(decrypted)
	case "7":
		// itemContent 是自身跨端回显中归一化后的商品快照 JSON。
		if itemContent := extractItemCardObservationContent(decrypted); itemContent != "" {
			messageType, content = "item", itemContent
		}
	}
	return &OutgoingChatMessage{
		AccountID:   accountID,
		ChatID:      chatID,
		BuyerID:     buyerID,
		Text:        text,
		MessageKey:  extractMessageID(decrypted),
		MessageType: messageType,
		Content:     content,
	}
}

// extractImageObservationContent 从自身回显中提取第一张图片 URL，供自动化发送确认使用。
// value 是已解密但未包含凭证的 WebSocket 消息；解析失败或缺少公网 URL 时返回空值。
func extractImageObservationContent(value any) string {
	// imageURL 保存当前回显中首个可比较的图片地址；找到后停止递归，保持消息顺序稳定。
	var imageURL string
	// walk 递归展开平台对象、数组和嵌套 JSON 字符串，避免依赖单一客户端版本的固定路径。
	var walk func(any)
	walk = func(current any) {
		if imageURL != "" || current == nil {
			return
		}
		// typed 保存当前节点的具体协议类型，便于继续展开嵌套图片正文。
		switch typed := current.(type) {
		case string:
			// nested 保存可能作为字符串封装的图片消息对象。
			var nested any
			if json.Unmarshal([]byte(typed), &nested) == nil {
				walk(nested)
			}
		case map[string]any:
			// image 保存平台图片消息的对象；pics 保存其中按发送顺序排列的图片数组。
			// imageFound 表示当前对象是否包含图片正文；picsFound 表示是否找到图片数组。
			if image, imageFound := typed["image"].(map[string]any); imageFound {
				// pics、picsFound 保存图片数组及其存在性，避免把非图片节点误当作可确认正文。
				if pics, picsFound := image["pics"].([]any); picsFound {
					// picValue 保存图片数组中的单个协议节点，后续只读取其中的 URL 字段。
					for _, picValue := range pics {
						// pic 保存当前图片字段；picURL 是其可直接比较的 URL。
						pic, _ := picValue.(map[string]any)
						// picURL 保存当前图片的公网地址，用于和发送参数做幂等匹配。
						picURL := strings.TrimSpace(toString(pic["url"]))
						if strings.HasPrefix(picURL, "http://") || strings.HasPrefix(picURL, "https://") {
							imageURL = picURL
							return
						}
					}
				}
			}
			// child 保存当前对象的嵌套字段，继续寻找不同客户端版本的图片路径。
			for _, child := range typed {
				walk(child)
			}
		case []any:
			// child 保存数组中的协议节点，按平台原始顺序递归展开。
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return imageURL
}

// extractItemCardObservationContent 从已解密帧中查找 contentType=7 的 itemCard.item 并输出规范 JSON。
func extractItemCardObservationContent(value any) string {
	// content 保存首个字段完整的商品卡片正文。
	var content string
	// walk 递归穿透平台的 JSON 字符串、对象和数组封装。
	var walk func(any)
	walk = func(current any) {
		if content != "" {
			return
		}
		// typed 是当前递归节点按实际 JSON 类型展开后的值。
		switch typed := current.(type) {
		case string:
			// nested 是可能二次 JSON 编码的内层值。
			var nested any
			if json.Unmarshal([]byte(typed), &nested) == nil {
				walk(nested)
			}
		case map[string]any:
			// itemCard 是官网 contentType=7 的卡片外层对象。
			itemCard, _ := typed["itemCard"].(map[string]any)
			// item 是保存商品身份及展示字段的卡片内层对象。
			item, _ := itemCard["item"].(map[string]any)
			if item != nil {
				// itemID 和 title 是渲染商品卡片的必需身份字段。
				itemID, title := strings.TrimSpace(toString(item["itemId"])), strings.TrimSpace(toString(item["title"]))
				// imageURL 和 price 是渲染商品卡片的必需展示字段。
				imageURL, price := strings.TrimSpace(toString(item["mainPic"])), strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(toString(item["price"])), "¥"))
				if strings.HasPrefix(imageURL, "//") {
					imageURL = "https:" + imageURL
				}
				if itemID != "" && title != "" && imageURL != "" && price != "" {
					// encoded 是固定字段顺序的非敏感商品 JSON。
					encoded, marshalErr := json.Marshal(struct {
						ItemID   string `json:"item_id"`
						Title    string `json:"title"`
						ImageURL string `json:"image_url"`
						Price    string `json:"price"`
					}{ItemID: itemID, Title: title, ImageURL: imageURL, Price: price})
					if marshalErr == nil {
						content = string(encoded)
						return
					}
				}
			}
			for _ /* child 是当前对象中继续递归检查的字段值。 */, child := range typed {
				walk(child)
			}
		case []any:
			for _ /* child 是当前数组中继续递归检查的元素。 */, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return content
}

// extractChatPeerUserID 从闲鱼聊天深链读取对端用户标识，并拒绝误填为当前账号的值。
// reminderURL 是平台通知深链；selfUserID 是当前账号身份；返回空字符串表示深链缺失或身份不可信。
func extractChatPeerUserID(reminderURL, selfUserID string) string {
	// parsed 保存深链解析结果；parseErr 表示平台提供了无法按 URL 解释的兼容字符串。
	parsed, parseErr := url.Parse(strings.TrimSpace(reminderURL))
	if parseErr != nil {
		return ""
	}
	// peerUserID 保存深链查询参数中的会话对端身份，并移除 IM 协议后缀。
	peerUserID := strings.TrimSuffix(strings.TrimSpace(parsed.Query().Get("peerUserId")), "@goofish")
	if peerUserID == "" || isSelfUserID(peerUserID, selfUserID) {
		return ""
	}
	return peerUserID
}

// extractChatSenderUserIDFromMaps 优先读取展示扩展，并兼容紧凑信封中的发送者账号 ID。
func extractChatSenderUserIDFromMaps(m1, m10 map[string]any) string {
	if m10 != nil {
		// sender 保存标准 senderUserId 的去空白结果。
		if sender := strings.TrimSpace(toString(m10["senderUserId"])); sender != "" {
			return sender
		}
	}
	// 部分回显消息缺少 10.senderUserId，但仍在紧凑 1.1 信封中保留发送者身份。
	// sender 保存紧凑信封中的发送者对象；ok 表示该兼容字段是否存在。
	if sender, ok := m1["1"].(map[string]any); ok {
		return strings.TrimSpace(toString(sender["1"]))
	}
	return ""
}

// isSelfUserID 比较发送者与当前账号身份，忽略 IM 协议后缀以过滤官方客户端回显。
func isSelfUserID(senderUserID, selfUserID string) bool {
	// sender 是归一化后的消息发送者身份。
	sender := strings.TrimSuffix(strings.TrimSpace(senderUserID), "@goofish")
	// self 是归一化后的当前账号身份。
	self := strings.TrimSuffix(strings.TrimSpace(selfUserID), "@goofish")
	return sender != "" && self != "" && sender == self
}

// isNonUserChatNotice 判断闲鱼 IM 中不应进入自动回复的系统提示或交易卡片。
// 典型样本：
// - contentType=14：“有蚂蚁森林能量可领”“不想宝贝被砍价?设置不砍价回复”“退款成功”
// - contentType=26：交易卡片，如“我已拍下，待付款”“我发起了退款申请”
// - contentType=25：确认收货后的评价提醒，如“快给ta一个评价吧～”
// 付款待发货卡片已经在 handleMessage 前半段进入 automation.Center，这里不能再进入聊天回复链。
// isNonUserChatNotice 封装isNon用户聊天Notice业务协调。
func isNonUserChatNotice(m1, m10 map[string]any, reminder string) bool {
	if strings.TrimSuffix(strings.TrimSpace(toString(m10["senderUserId"])), "@goofish") == "1400" {
		return true
	}
	if strings.TrimSpace(reminder) == "发来一条新消息" {
		return true
	}
	if strings.TrimSpace(reminder) == "快给ta一个评价吧～" || strings.TrimSpace(reminder) == "快给ta一个评价吧~" {
		return true
	}
	if // sessionType 用于本次流程后续判断的会话类型
	sessionType := strings.TrimSpace(toString(m10["sessionType"])); sessionType != "" && sessionType != "1" {
		return true
	}
	// contentType 用于本次流程后续判断的内容类型
	contentType := messageContentType(m1, m10)
	switch contentType {
	case "14":
		return true
	case "25", "26":
		return true
	}
	return false
}

// messageContentType 封装消息内容类型业务协调。
func messageContentType(m1, m10 map[string]any) string {
	if // ext 用于本次流程后续判断的ext
	ext, _ := m10["extJson"].(string); ext != "" {
		// extJSON 用于本次流程后续判断的extJSON
		var extJSON map[string]any
		if json.Unmarshal([]byte(ext), &extJSON) == nil {
			if // v 用于本次流程后续判断的v
			v := toString(extJSON["contentType"]); v != "" {
				return v
			}
		}
	}
	// m6 用于本次流程后续判断的m6
	m6, _ := m1["6"].(map[string]any)
	if m6 == nil {
		return ""
	}
	// m63 用于本次流程后续判断的m63
	m63, _ := m6["3"].(map[string]any)
	if m63 == nil {
		return ""
	}
	if // v 用于本次流程后续判断的v
	v := toString(m63["4"]); v != "" {
		return v
	}
	if // contentJSON 用于本次流程后续判断的内容JSON
	contentJSON, _ := m63["5"].(string); contentJSON != "" {
		// content 用于本次流程后续判断的内容
		var content map[string]any
		if json.Unmarshal([]byte(contentJSON), &content) == nil {
			return toString(content["contentType"])
		}
	}
	return ""
}

// extractItemID 从 reminderUrl 中正则提取 itemId=xxx。
func extractItemID(url string) string {
	// key 用于本次流程后续判断的key
	const key = "itemId="
	// i 用于本次流程后续判断的i
	i := strings.Index(url, key)
	if i < 0 {
		return ""
	}
	// s 用于本次流程后续判断的s
	s := url[i+len(key):]
	if // j 用于本次流程后续判断的j
	j := strings.IndexAny(s, "&\n\r"); j >= 0 {
		s = s[:j]
	}
	return s
}

// currentCookieStr 封装current登录凭证Str业务协调。
func (a *Account) currentCookieStr() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.CookieStr
}

// CurrentCookieStr 线程安全地返回账号当前使用的 Cookie。
func (a *Account) CurrentCookieStr() string {
	return a.currentCookieStr()
}

// ---- 小工具 ----

// contains 封装contains业务协调。
func contains(s, sub string) bool { return strings.Contains(strings.ToLower(s), strings.ToLower(sub)) }

// toString 封装toString业务协调。
func toString(v any) string {
	switch // x 用于本次流程后续判断的x
	x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		// JSON 数字 → 整数字符串。
		return trimFloatInt(x)
	default:
		// b 用于本次流程后续判断的b
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// trimFloatInt 封装trimFloatInt业务协调。
func trimFloatInt(f float64) string {
	if f == float64(int64(f)) {
		return int64ToString(int64(f))
	}
	return ftoa(f)
}

// int64ToString 封装int64ToString业务协调。
func int64ToString(n int64) string {
	if n == 0 {
		return "0"
	}
	// neg 用于本次流程后续判断的neg
	neg := n < 0
	if neg {
		n = -n
	}
	// b 用于本次流程后续判断的b
	var b [20]byte
	// i 用于本次流程后续判断的i
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ftoa 封装ftoa业务协调。
func ftoa(f float64) string { return jsonNumber(f) }

// jsonNumber 封装jsonNumber业务协调。
func jsonNumber(f float64) string {
	// b 用于本次流程后续判断的b
	b, _ := json.Marshal(f)
	return string(b)
}

// truncID 封装truncID业务协调。
func truncID(id string) string {
	if len(id) > 50 {
		return id[:50] + "..."
	}
	return id
}

// errString 封装errString业务协调。
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// sleepCtx 封装sleepCtx业务协调。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	// t 用于本次流程后续判断的t
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
