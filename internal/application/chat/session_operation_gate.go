package chat

import "sync"

// sessionOperationGate 按账号与会话串行化人工发送和本地删除。
// 锁会跨越一次平台发送 I/O，这是为了保证“发送完成后删除”能够原子表现为最终清空；不同会话互不阻塞。
type sessionOperationGate struct {
	// mu 只保护 entries 的创建、引用计数和回收，不覆盖平台或数据库 I/O。
	mu sync.Mutex
	// entries 保存当前正在使用或等待使用的会话锁。
	entries map[string]*sessionOperationEntry
}

// sessionOperationEntry 保存单个会话的互斥锁及等待者引用计数。
type sessionOperationEntry struct {
	// mu 串行化同一账号和会话的人工发送与删除。
	mu sync.Mutex
	// refs 统计已取得条目引用但尚未释放的调用数量。
	refs int
}

// newSessionOperationGate 创建不启动后台任务的会话操作门。
func newSessionOperationGate() *sessionOperationGate {
	return &sessionOperationGate{entries: make(map[string]*sessionOperationEntry)}
}

// lock 获取指定账号和会话的独占操作权，并返回可安全重复调用的释放函数。
func (gate *sessionOperationGate) lock(accountID, chatID string) func() {
	if gate == nil {
		return func() {}
	}
	// key 使用不可出现在平台标识中的分隔符组合账号与会话，避免跨账号互相阻塞。
	key := accountID + "\x00" + chatID
	gate.mu.Lock()
	// entry 是当前调用持有引用的会话锁条目。
	entry := gate.entries[key]
	if entry == nil {
		entry = &sessionOperationEntry{}
		gate.entries[key] = entry
	}
	entry.refs++
	gate.mu.Unlock()
	entry.mu.Lock()
	// once 保证调用方的重复 defer 或补偿路径不会破坏锁引用计数。
	var once sync.Once
	return func() {
		once.Do(func() {
			entry.mu.Unlock()
			gate.mu.Lock()
			entry.refs--
			if entry.refs == 0 && gate.entries[key] == entry {
				delete(gate.entries, key)
			}
			gate.mu.Unlock()
		})
	}
}
