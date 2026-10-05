package chat

import (
	"context"
	"sync"
	"time"
)

// subscriber 保存管理用户订阅的本地身份和事件队列。
type subscriber struct {
	// userID 是管理用户本地身份，不保存账号静态快照作为生产授权依据。
	userID int64
	// accounts 是兼容测试仓储的初始账号集合。
	accounts map[string]struct{}
	// ch 是向管理 WebSocket 发送的有界事件队列。
	ch chan Event
}

// ownerRepository 是支持动态账号归属查询的可选持久化扩展，不影响已有测试仓储兼容性。
type ownerRepository interface {
	GetOwnerID(context.Context, string) (int64, error)
}

// Subscribe 创建按管理用户归属过滤的实时事件订阅。
func (s *Service) Subscribe(ctx context.Context, userID int64) (<-chan Event, func(), error) {
	// accountIDs 和 err 保存兼容仓储的账号集合及查询错误。
	accountIDs, err := s.repository.ListOwnedIDs(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	// allowed 保存兼容仓储的初始账号集合；生产仓储发布时会动态读取归属。
	allowed := make(map[string]struct{}, len(accountIDs))
	// accountID 是当前订阅初始账号标识。
	for _, accountID := range accountIDs {
		allowed[accountID] = struct{}{}
	}
	s.mu.Lock()
	s.next++
	// id 是当前订阅的内部标识。
	id := s.next
	// ch 是当前订阅的有界事件队列。
	ch := make(chan Event, 128)
	s.subs[id] = subscriber{userID: userID, accounts: allowed, ch: ch}
	s.mu.Unlock()
	// once 保证取消动作幂等。
	var once sync.Once
	// cancel 是释放订阅队列和索引的幂等函数。
	cancel := func() {
		once.Do(func() {
			s.mu.Lock()
			// sub 和 ok 保存待取消订阅及其当前存在性。
			if sub, ok := s.subs[id]; ok {
				delete(s.subs, id)
				close(sub.ch)
			}
			s.mu.Unlock()
		})
	}
	return ch, cancel, nil
}

// Publish 发布实时事件；兼容旧调用方使用不可取消的本地上下文。
func (s *Service) Publish(accountID string, event Event) {
	s.PublishContext(context.Background(), accountID, event)
}

// PublishContext 在订阅锁外查询动态账号归属，再向匹配用户入队。
func (s *Service) PublishContext(ctx context.Context, accountID string, event Event) {
	// ownerID 保存动态归属用户；零值表示使用兼容仓储的初始集合。
	ownerID := int64(0)
	// ownerStore 和 ok 保存可选动态归属查询端口及支持状态。
	if ownerStore, ok := s.repository.(ownerRepository); ok {
		// ownerCtx 和 cancel 限制归属查询最多等待三秒。
		ownerCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		// ownerErr 保存归属查询基础设施错误。
		var ownerErr error
		ownerID, ownerErr = ownerStore.GetOwnerID(ownerCtx, accountID)
		cancel()
		if ownerErr != nil {
			s.closeSubscriptions()
			return
		}
		if ownerID <= 0 {
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// id 和 sub 保存当前遍历的订阅标识及用户队列。
	for id, sub := range s.subs {
		if (ownerID > 0 && sub.userID != ownerID) || (ownerID == 0 && !hasAccount(sub.accounts, accountID)) {
			continue
		}
		select {
		case sub.ch <- event:
		default:
			delete(s.subs, id)
			close(sub.ch)
		}
	}
}

// closeSubscriptions 在归属查询基础设施失败时关闭管理订阅，促使客户端走本地恢复路径。
func (s *Service) closeSubscriptions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// id 和 sub 保存待关闭的订阅标识及其队列。
	for id, sub := range s.subs {
		delete(s.subs, id)
		close(sub.ch)
	}
}

// hasAccount 判断兼容测试仓储的初始账号集合是否包含待发布账号。
func hasAccount(accounts map[string]struct{}, accountID string) bool {
	// ok 表示兼容初始集合是否包含目标账号。
	_, ok := accounts[accountID]
	return ok
}
