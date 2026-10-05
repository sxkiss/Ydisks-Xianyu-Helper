package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

// chatSessionVisibilityIndexColumns 读取指定方言中的会话列表索引键，结果顺序与数据库实际索引顺序一致。
func chatSessionVisibilityIndexColumns(t *testing.T, database *sql.DB, dialect Dialect) []string {
	t.Helper()
	// query 和 args 是当前方言读取固定会话列表索引元数据的只读语句及参数。
	query, args := `SELECT name FROM pragma_index_info(?) ORDER BY seqno`, []any{"idx_chat_sessions_account_visibility_recent"}
	switch dialect {
	case DialectMySQL:
		query = `SELECT COLUMN_NAME FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND INDEX_NAME=? ORDER BY SEQ_IN_INDEX`
		args = []any{"chat_sessions", "idx_chat_sessions_account_visibility_recent"}
	case DialectPostgres:
		query = `SELECT pg_get_indexdef(i.indexrelid, positions.n, true)
			FROM pg_index i JOIN pg_class idx ON idx.oid=i.indexrelid
			JOIN pg_class tbl ON tbl.oid=i.indrelid
			CROSS JOIN LATERAL generate_series(1, i.indnkeyatts) AS positions(n)
			WHERE idx.relname=? AND tbl.relname=? AND tbl.relnamespace=current_schema()::regnamespace
			ORDER BY positions.n`
		args = []any{"idx_chat_sessions_account_visibility_recent", "chat_sessions"}
	}
	// rows 和 queryErr 保存索引元数据游标及读取失败原因。
	rows, queryErr := database.Query(query, args...)
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	defer rows.Close()
	// columns 收集按键位排序的索引列名，不读取任何聊天业务数据。
	columns := make([]string, 0, 5)
	for rows.Next() {
		// column 是当前索引键位对应的列名。
		var column string
		// scanErr 保存当前索引列名映射失败原因。
		if scanErr := rows.Scan(&column); scanErr != nil {
			t.Fatal(scanErr)
		}
		columns = append(columns, column)
	}
	// rowsErr 保存索引元数据游标结束时由驱动返回的读取错误。
	if rowsErr := rows.Err(); rowsErr != nil {
		t.Fatal(rowsErr)
	}
	return columns
}

// TestMultiDBChatSessionDeletionMigration 验证三方言 44 迁移的字段、列表索引和可回退结构保持一致。
func TestMultiDBChatSessionDeletionMigration(t *testing.T) {
	// target 是当前可用数据库方言的隔离测试实例。
	for _, target := range allTestTargets(t) {
		t.Run(target.name, func(t *testing.T) {
			defer target.cleanup()
			// expectedUpColumns 是升级后必须覆盖隐藏语义与稳定排序的有序索引列。
			expectedUpColumns := "cookie_id,is_visible,user_hidden_at,last_message_at,chat_id"
			// columns 是当前升级结构读取到的实际索引列顺序。
			if columns := strings.Join(chatSessionVisibilityIndexColumns(t, target.store.DB, target.dialect), ","); columns != expectedUpColumns {
				t.Fatalf("up index columns=%s want=%s", columns, expectedUpColumns)
			}
			if !columnExistsForDialect(t, target.store.DB, target.dialect, "chat_sessions", "user_hidden_at") ||
				!columnExistsForDialect(t, target.store.DB, target.dialect, "chat_sessions", "messages_cleared_at") {
				t.Fatal("migration 44 deletion columns are missing")
			}
			// subdir 和 gooseDialect 选择当前方言的嵌入迁移目录与 Goose 方言名。
			subdir, gooseDialect := migrationTestSubdir(t, target.dialect)
			// dialectErr 保存为当前真实数据库设置 Goose 方言时的失败原因。
			if dialectErr := goose.SetDialect(gooseDialect); dialectErr != nil {
				t.Fatal(dialectErr)
			}
			goose.SetBaseFS(migrationsFS)
			// downErr 回滚到 43，先撤销 46、45 再撤销 44，验证旧列表索引和字段结构可以恢复。
			if downErr := goose.DownTo(target.store.DB, "migrations/"+subdir, 43); downErr != nil {
				t.Fatal(downErr)
			}
			if columnExistsForDialect(t, target.store.DB, target.dialect, "chat_sessions", "user_hidden_at") ||
				columnExistsForDialect(t, target.store.DB, target.dialect, "chat_sessions", "messages_cleared_at") {
				t.Fatal("migration 44 deletion columns remain after down")
			}
			// expectedDownColumns 是回滚后 41 迁移建立的原列表索引列。
			expectedDownColumns := "cookie_id,is_visible,last_message_at,chat_id"
			// columns 是回滚后读取到的旧索引列顺序。
			if columns := strings.Join(chatSessionVisibilityIndexColumns(t, target.store.DB, target.dialect), ","); columns != expectedDownColumns {
				t.Fatalf("down index columns=%s want=%s", columns, expectedDownColumns)
			}
			// upErr 再次应用 44，验证回滚没有遗留重名索引或不可重建结构。
			if upErr := goose.Up(target.store.DB, "migrations/"+subdir); upErr != nil {
				t.Fatal(upErr)
			}
			// columns 是再次升级后读取到的索引列顺序。
			if columns := strings.Join(chatSessionVisibilityIndexColumns(t, target.store.DB, target.dialect), ","); columns != expectedUpColumns {
				t.Fatalf("re-up index columns=%s want=%s", columns, expectedUpColumns)
			}
		})
	}
}

// TestHideAndClearSessionKeepsAutomationIdentity 验证用户删除只清空展示消息，并保留自动化定位与独立 AI 记忆。
func TestHideAndClearSessionKeepsAutomationIdentity(t *testing.T) {
	// store 和 cleanup 提供带最新迁移的隔离 SQLite 数据库及释放函数。
	store, cleanup := newTestDB(t)
	defer cleanup()
	// ctx 是本测试全部本地事务使用的非取消上下文。
	ctx := context.Background()
	// userID 和 accountID 是待删除会话所属用户及账号。
	userID, accountID := seedAccount(t, store)
	// session 保存自动化后续仍需使用的买家、商品与聊天标识，遮罩昵称应在清空前被真实历史昵称替换。
	session := ChatSession{CookieID: accountID, ChatID: "delete-chat", BuyerID: "buyer-1", BuyerName: "买***家", ItemID: "item-1", ItemTitle: "商品", LastMessage: "旧消息", LastMessageAt: 100}
	// upsertErr 保存删除前会话摘要写入失败原因。
	if upsertErr := store.Chats.UpsertSession(ctx, session); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	// oldMessage 是删除前可见且携带可信买家昵称的聊天消息。
	oldMessage := ChatMessage{MessageKey: "old-message", Direction: "incoming", SenderID: "buyer-1", SenderName: "真实买家", MessageType: "text", Content: "删除前消息", Status: "received", SentAt: 100}
	// saveErr 保存删除前可信消息写入失败原因。
	if _, _, saveErr := store.Chats.SaveMessage(ctx, session, oldMessage, true); saveErr != nil {
		t.Fatal(saveErr)
	}
	// aiWriteErr 保存独立 AI 对话上下文的写入结果；用户删除后该记录必须保留。
	aiWriteErr := store.AIReply.AddConversation(ctx, accountID, session.ChatID, session.BuyerID, session.ItemID, AIConversationMessage{Role: "user", Content: "还能便宜吗", BargainCount: 1})
	if aiWriteErr != nil {
		t.Fatal(aiWriteErr)
	}
	// found 和 deleteErr 保存本次归属校验后的隐藏清空结果。
	found, deleteErr := store.Chats.HideAndClearSession(ctx, userID, accountID, session.ChatID, 1_000)
	if deleteErr != nil || !found {
		t.Fatalf("hide and clear found=%v err=%v", found, deleteErr)
	}
	// visibleSessions 和 listErr 验证用户隐藏会话不再出现在聊天列表。
	visibleSessions, listErr := store.Chats.ListSessions(ctx, userID, accountID, 20)
	if listErr != nil || len(visibleSessions) != 0 {
		t.Fatalf("visible sessions=%+v err=%v", visibleSessions, listErr)
	}
	// messageRows 和 messagesErr 验证该会话全部展示消息已经物理删除。
	messageRows, messagesErr := store.Chats.ListMessages(ctx, userID, accountID, session.ChatID, 0, 20)
	if messagesErr != nil || len(messageRows) != 0 {
		t.Fatalf("messages=%+v err=%v", messageRows, messagesErr)
	}
	// automationChatIDs 和 matchErr 验证隐藏会话仍可供订单与赠品自动化匹配发送目标。
	automationChatIDs, matchErr := store.Chats.FindChatIDsByBuyerAndItem(ctx, accountID, session.BuyerID, session.ItemID)
	if matchErr != nil || len(automationChatIDs) != 1 || automationChatIDs[0] != session.ChatID {
		t.Fatalf("automation chat ids=%v err=%v", automationChatIDs, matchErr)
	}
	// nickname 和 nicknameErr 验证清空前固化的未遮罩昵称仍可用于自动化模板。
	nickname, nicknameErr := store.Chats.BuyerNicknameForAutomation(ctx, accountID, session.ChatID)
	if nicknameErr != nil || nickname != "真实买家" {
		t.Fatalf("nickname=%q err=%v", nickname, nicknameErr)
	}
	// aiHistory 和 aiErr 验证 AI 记忆及砍价轮次不属于聊天页消息删除范围。
	aiHistory, aiErr := store.AIReply.ConversationHistory(ctx, accountID, session.ChatID, session.ItemID, 10)
	if aiErr != nil || len(aiHistory) != 1 || aiHistory[0].BargainCount != 1 {
		t.Fatalf("ai history=%+v err=%v", aiHistory, aiErr)
	}
	// hiddenAt、clearedAt 和 summaryAt 保存删除后会话的隐藏、截止和摘要时间状态。
	var hiddenAt, clearedAt, summaryAt int64
	// unread 保存删除后的会话未读数。
	var unread int
	// summary 保存删除后应当清空的会话摘要。
	var summary string
	// stateErr 验证清空截止线、隐藏标记、摘要和未读数在同一事务内完成更新。
	stateErr := store.DB.QueryRowContext(ctx, `SELECT user_hidden_at,messages_cleared_at,last_message,last_message_at,unread_count
		FROM chat_sessions WHERE cookie_id=? AND chat_id=?`, accountID, session.ChatID).Scan(&hiddenAt, &clearedAt, &summary, &summaryAt, &unread)
	if stateErr != nil || hiddenAt != 1_000 || clearedAt != 100 || summary != "" || summaryAt != 0 || unread != 0 {
		t.Fatalf("hidden=%d cleared=%d summary=%q at=%d unread=%d err=%v", hiddenAt, clearedAt, summary, summaryAt, unread, stateErr)
	}
	// cleanupErr 验证空会话清理不会物理删除用户主动隐藏、仍供自动化使用的会话。
	if cleanupErr := store.Chats.DeleteEmptySessions(ctx, accountID); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	// retainedCount 和 countErr 验证会话实体在空壳清理后仍然存在。
	var retainedCount int
	// countErr 保存会话保留数量查询失败原因。
	countErr := store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_sessions WHERE cookie_id=? AND chat_id=?`, accountID, session.ChatID).Scan(&retainedCount)
	if countErr != nil || retainedCount != 1 {
		t.Fatalf("retained session count=%d err=%v", retainedCount, countErr)
	}
}

// TestHiddenSessionRejectsOldHistoryAndRestoresForNewMessage 验证删除截止线阻止旧历史回灌，并仅由删除后的消息恢复会话。
func TestHiddenSessionRejectsOldHistoryAndRestoresForNewMessage(t *testing.T) {
	// store 和 cleanup 提供本次消息边界测试使用的隔离数据库及释放函数。
	store, cleanup := newTestDB(t)
	defer cleanup()
	// ctx、userID 和 accountID 分别保存数据库上下文及会话归属标识。
	ctx := context.Background()
	// userID 和 accountID 是截止线测试会话所属用户与账号。
	userID, accountID := seedAccount(t, store)
	// session 是删除前后复用同一自动化定位信息的会话摘要。
	session := ChatSession{CookieID: accountID, ChatID: "restore-chat", BuyerID: "buyer", ItemID: "item", LastMessage: "旧摘要", LastMessageAt: 500}
	// upsertErr 保存截止线测试会话写入失败原因。
	if upsertErr := store.Chats.UpsertSession(ctx, session); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	// deleteErr 保存首次隐藏与清空操作失败原因。
	if _, deleteErr := store.Chats.HideAndClearSession(ctx, userID, accountID, session.ChatID, 1_000); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	// staleSummary 模拟普通联系人刷新返回删除前摘要，不能恢复会话或正文。
	staleSummary := session
	staleSummary.LastMessage, staleSummary.LastMessageAt, staleSummary.UnreadCount = "删除前摘要", 450, 3
	// upsertErr 保存平台旧摘要同步失败原因。
	if upsertErr := store.Chats.UpsertSession(ctx, staleSummary); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	// syncErr 保存权威摘要旧时间边界更新结果。
	if syncErr := store.Chats.SyncSessionSummary(ctx, accountID, session.ChatID, "删除前同步摘要", 480, 480, 4); syncErr != nil {
		t.Fatal(syncErr)
	}
	// staleStored、staleInserted 和 staleErr 验证删除前消息被明确忽略且不会触发恢复事件。
	staleStored, staleInserted, staleErr := store.Chats.SaveMessage(ctx, session, ChatMessage{MessageKey: "stale", Direction: "incoming", MessageType: "text", Content: "旧历史", Status: "received", SentAt: 500}, false)
	if staleErr != nil || staleInserted || staleStored != nil {
		t.Fatalf("stale stored=%+v inserted=%v err=%v", staleStored, staleInserted, staleErr)
	}
	// hiddenRows 和 hiddenErr 验证旧摘要及旧消息均未让会话重新出现在列表。
	hiddenRows, hiddenErr := store.Chats.ListSessions(ctx, userID, accountID, 20)
	if hiddenErr != nil || len(hiddenRows) != 0 {
		t.Fatalf("hidden rows=%+v err=%v", hiddenRows, hiddenErr)
	}
	// newMessage 是删除后在同一平台秒内到达的实时消息；本地观察时间应使它恢复会话。
	newMessage := ChatMessage{MessageKey: "new", Direction: "incoming", SenderID: "buyer", SenderName: "买家", MessageType: "text", Content: "重新开始", Status: "received", SentAt: 500, ObservedAt: 1_001}
	// newStored、newInserted 和 newErr 保存新消息的落库结果、插入状态及错误。
	newStored, newInserted, newErr := store.Chats.SaveMessage(ctx, session, newMessage, true)
	if newErr != nil || !newInserted || newStored == nil {
		t.Fatalf("new stored=%+v inserted=%v err=%v", newStored, newInserted, newErr)
	}
	// restoredRows 和 restoredErr 验证新消息恢复列表可见性并生成新的摘要。
	restoredRows, restoredErr := store.Chats.ListSessions(ctx, userID, accountID, 20)
	if restoredErr != nil || len(restoredRows) != 1 || restoredRows[0].LastMessage != "重新开始" {
		t.Fatalf("restored rows=%+v err=%v", restoredRows, restoredErr)
	}
	// restoredMessages 和 messagesErr 验证恢复后只有删除截止时间之后的消息。
	restoredMessages, messagesErr := store.Chats.ListMessages(ctx, userID, accountID, session.ChatID, 0, 20)
	if messagesErr != nil || len(restoredMessages) != 1 || restoredMessages[0].MessageKey != "new" {
		t.Fatalf("restored messages=%+v err=%v", restoredMessages, messagesErr)
	}
	// repeatedFound 和 repeatedErr 验证会话恢复后再次删除会推进截止线并清空新一轮消息。
	repeatedFound, repeatedErr := store.Chats.HideAndClearSession(ctx, userID, accountID, session.ChatID, 2_000)
	if repeatedErr != nil || !repeatedFound {
		t.Fatalf("second delete found=%v err=%v", repeatedFound, repeatedErr)
	}
	// idempotentFound 和 idempotentErr 验证隐藏状态下重复请求不会推进清空时间。
	idempotentFound, idempotentErr := store.Chats.HideAndClearSession(ctx, userID, accountID, session.ChatID, 3_000)
	if idempotentErr != nil || !idempotentFound {
		t.Fatalf("idempotent delete found=%v err=%v", idempotentFound, idempotentErr)
	}
	// finalCutoff 和 cutoffErr 验证幂等请求保留第一次成功删除的截止时间。
	var finalCutoff int64
	// cutoffErr 保存最终消息清空截止线读取失败原因。
	cutoffErr := store.DB.QueryRowContext(ctx, `SELECT messages_cleared_at FROM chat_sessions WHERE cookie_id=? AND chat_id=?`, accountID, session.ChatID).Scan(&finalCutoff)
	if cutoffErr != nil || finalCutoff != 500 {
		t.Fatalf("final cutoff=%d err=%v", finalCutoff, cutoffErr)
	}
}

// TestHiddenSessionHistoryUsesPlatformWatermarkNotLocalClock 验证离线新消息只和删除前平台水位比较，不受本机删除时钟领先影响。
func TestHiddenSessionHistoryUsesPlatformWatermarkNotLocalClock(t *testing.T) {
	// store 和 cleanup 提供平台与本机时钟域隔离测试使用的数据库及释放函数。
	store, cleanup := newTestDB(t)
	defer cleanup()
	// ctx 是本次离线历史恢复场景使用的数据库上下文。
	ctx := context.Background()
	// userID 和 accountID 是本次离线历史恢复场景的用户及账号归属标识。
	userID, accountID := seedAccount(t, store)
	// session 保存删除前最后一条已知平台消息时间。
	session := ChatSession{CookieID: accountID, ChatID: "platform-watermark-chat", BuyerID: "buyer", LastMessage: "删除前消息", LastMessageAt: 500}
	// upsertErr 是删除前会话摘要的保存结果。
	if upsertErr := store.Chats.UpsertSession(ctx, session); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	// deleteErr 使用明显领先的平台外本机时间执行删除，历史水位仍必须保持为 500。
	if _, deleteErr := store.Chats.HideAndClearSession(ctx, userID, accountID, session.ChatID, 10_000); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	// offlineMessage 模拟删除后离线期间到达、平台时间只比原水位增加一毫秒的新消息。
	offlineMessage := ChatMessage{MessageKey: "offline-new", Direction: "incoming", SenderID: "buyer", SenderName: "买家", MessageType: "text", Content: "离线新消息", Status: "received", SentAt: 501}
	// stored、inserted 和 saveErr 验证历史导入能够恢复会话且不会被本机 10000 毫秒截止值吞掉。
	stored, inserted, saveErr := store.Chats.SaveMessage(ctx, session, offlineMessage, false)
	if saveErr != nil || !inserted || stored == nil {
		t.Fatalf("stored=%+v inserted=%v err=%v", stored, inserted, saveErr)
	}
	// visible 和 listErr 验证离线新消息让会话重新出现在本地列表。
	visible, listErr := store.Chats.ListSessions(ctx, userID, accountID, 20)
	if listErr != nil || len(visible) != 1 || visible[0].LastMessage != "离线新消息" {
		t.Fatalf("visible=%+v err=%v", visible, listErr)
	}
}

// TestHideAndClearSessionClearsPreexistingFutureMessage 验证删除事务会清空此前已落库的未来偏移消息，并把它纳入历史水位。
func TestHideAndClearSessionClearsPreexistingFutureMessage(t *testing.T) {
	// store 和 cleanup 提供并发顺序确定化测试使用的隔离数据库及释放函数。
	store, cleanup := newTestDB(t)
	defer cleanup()
	// ctx、userID 和 accountID 分别保存数据库上下文及会话归属标识。
	ctx := context.Background()
	// userID 和 accountID 是并发边界测试会话所属用户与账号。
	userID, accountID := seedAccount(t, store)
	// session 是包含未来偏移平台摘要的会话，模拟客户端或平台时钟领先本机。
	session := ChatSession{CookieID: accountID, ChatID: "concurrent-chat", BuyerID: "buyer", LastMessage: "旧摘要", LastMessageAt: 900}
	// messages 保存两条都在删除事务前完成落库的消息，平台时间是否领先都不能影响清空语义。
	messages := []ChatMessage{
		{MessageKey: "before-cutoff", Direction: "incoming", MessageType: "text", Content: "删除前消息", Status: "received", SentAt: 900},
		{MessageKey: "future-skew", Direction: "incoming", MessageType: "text", Content: "未来偏移旧消息", Status: "received", SentAt: 1_100},
	}
	// message 是当前按时间顺序写入的聊天消息。
	for _, message := range messages {
		// saveErr 保存当前边界消息写入失败原因。
		if _, _, saveErr := store.Chats.SaveMessage(ctx, session, message, true); saveErr != nil {
			t.Fatal(saveErr)
		}
	}
	// found 和 deleteErr 保存使用两条消息之间截止线执行删除的结果。
	found, deleteErr := store.Chats.HideAndClearSession(ctx, userID, accountID, session.ChatID, 1_000)
	if deleteErr != nil || !found {
		t.Fatalf("hide and clear found=%v err=%v", found, deleteErr)
	}
	// visibleRows 和 listErr 验证所有事务前消息被清空后会话保持隐藏。
	visibleRows, listErr := store.Chats.ListSessions(ctx, userID, accountID, 20)
	if listErr != nil || len(visibleRows) != 0 {
		t.Fatalf("visible rows=%+v err=%v", visibleRows, listErr)
	}
	// retainedMessages 和 messagesErr 验证未来偏移的旧消息也没有残留。
	retainedMessages, messagesErr := store.Chats.ListMessages(ctx, userID, accountID, session.ChatID, 0, 20)
	if messagesErr != nil || len(retainedMessages) != 0 {
		t.Fatalf("retained messages=%+v err=%v", retainedMessages, messagesErr)
	}
	// cutoff 保存删除后记录的平台历史水位，必须覆盖未来偏移旧消息的时间。
	var cutoff int64
	// cutoffErr 保存历史水位读取结果。
	cutoffErr := store.DB.QueryRowContext(ctx, `SELECT messages_cleared_at FROM chat_sessions WHERE cookie_id=? AND chat_id=?`, accountID, session.ChatID).Scan(&cutoff)
	if cutoffErr != nil || cutoff != 1_100 {
		t.Fatalf("cutoff=%d err=%v", cutoff, cutoffErr)
	}
}

// TestHideAndClearSessionEnforcesOwnership 验证跨用户或不存在的会话不能被清空。
func TestHideAndClearSessionEnforcesOwnership(t *testing.T) {
	// store 和 cleanup 提供归属隔离测试数据库及释放函数。
	store, cleanup := newTestDB(t)
	defer cleanup()
	// ctx、userID 和 accountID 保存测试会话的真实归属。
	ctx := context.Background()
	// userID 和 accountID 是归属隔离测试会话所属用户与账号。
	userID, accountID := seedAccount(t, store)
	// session 是应当对其他用户保持不可删除的会话。
	session := ChatSession{CookieID: accountID, ChatID: "owned-chat", BuyerID: "buyer", LastMessageAt: 10}
	// upsertErr 保存归属隔离测试会话写入失败原因。
	if upsertErr := store.Chats.UpsertSession(ctx, session); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	// found 和 forbiddenErr 验证已知账号及会话标识仍不能绕过用户归属。
	found, forbiddenErr := store.Chats.HideAndClearSession(ctx, userID+1, accountID, session.ChatID, 20)
	if forbiddenErr != nil || found {
		t.Fatalf("cross-user found=%v err=%v", found, forbiddenErr)
	}
	// missingFound 和 missingErr 验证不存在会话使用稳定的未命中结果。
	missingFound, missingErr := store.Chats.HideAndClearSession(ctx, userID, accountID, "missing", 20)
	if missingErr != nil || missingFound {
		t.Fatalf("missing found=%v err=%v", missingFound, missingErr)
	}
	// visibleSession 和 visibleErr 验证失败删除没有改变真实会话的可见状态。
	visibleSession, visibleErr := store.Chats.FindSession(ctx, userID, accountID, session.ChatID)
	if visibleErr != nil || visibleSession == nil {
		t.Fatalf("visible session=%+v err=%v", visibleSession, visibleErr)
	}
}
