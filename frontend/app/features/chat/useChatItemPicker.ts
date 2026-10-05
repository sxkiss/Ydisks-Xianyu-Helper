import { useCallback,useEffect,useRef,useState } from 'react';
import { confirmedOutgoingMessageFromError,getChatItems,sendChatItemCard,uncertainOutgoingMessageFromError } from './api';
import type { ChatItem,ChatMessage } from './models';
import { isChatAbortError,isCurrentChatRequest } from './state';

/** ChatItemRole 表示商品列表归属；peer 对应对方商品，self 对应当前账号商品。 */
export type ChatItemRole = 'peer' | 'self';

/** UseChatItemPickerOptions 描述商品选择弹窗依赖的当前会话和发送回调。 */
export interface UseChatItemPickerOptions {
  /** open 控制弹窗是否可见；关闭时会取消列表和发送请求。 */
  open: boolean;
  /** accountID 是当前登录闲鱼账号标识。 */
  accountID: string;
  /** chatID 是当前个人会话标识。 */
  chatID: string;
  /** onSent 接收后端状态机返回的商品消息和可选状态提示。 */
  onSent: (message: ChatMessage, notice?: string) => void;
  /** onClose 在成功发送或用户主动关闭时收起弹窗。 */
  onClose: () => void;
}

/** UseChatItemPickerResult 暴露商品查询、分页和单商品发送状态。 */
export interface UseChatItemPickerResult {
  /** role 是当前商品归属标签。 */
  role: ChatItemRole;
  /** queryDraft 是尚未提交的搜索输入。 */
  queryDraft: string;
  /** items 是已加载并按商品标识去重的商品列表。 */
  items: ChatItem[];
  /** hasMore 表示当前查询是否还有下一页。 */
  hasMore: boolean;
  /** loading 表示第一页加载状态。 */
  loading: boolean;
  /** loadingMore 表示下一页加载状态。 */
  loadingMore: boolean;
  /** sendingItemID 是当前正在发送的商品标识。 */
  sendingItemID: string;
  /** error 保存当前列表或发送失败提示。 */
  error: string;
  /** setQueryDraft 只更新输入内容，不触发平台请求。 */
  setQueryDraft: React.Dispatch<React.SetStateAction<string>>;
  /** selectRole 切换商品归属并清空搜索。 */
  selectRole: (role: ChatItemRole) => void;
  /** submitSearch 提交当前搜索输入并从第一页重新查询。 */
  submitSearch: () => void;
  /** loadMore 请求当前查询的下一页。 */
  loadMore: () => Promise<void>;
  /** sendItem 发送指定商品卡片。 */
  sendItem: (item: ChatItem) => Promise<void>;
}

/** mergeChatItems 将分页商品按商品标识去重，并保留新页补充的最新快照。 */
const mergeChatItems = (current: ChatItem[], incoming: ChatItem[]): ChatItem[] => {
  // merged 按商品标识保存列表顺序和最新展示快照。
  const merged = new Map<string, ChatItem>();
  for (const /* item 是当前待合并的商品快照。 */ item of [...current, ...incoming]) merged.set(item.item_id, item);
  return [...merged.values()];
};

/** useChatItemPicker 管理弹窗内独立的查询代次、取消、分页和单项发送。 */
export const useChatItemPicker = (options: UseChatItemPickerOptions): UseChatItemPickerResult => {
	// open、accountID、chatID、onSent 和 onClose 是当前渲染拥有的弹窗上下文，避免回调依赖整个 options 对象。
	const { open, accountID, chatID, onSent, onClose } = options;
  // role 保存当前商品归属标签，首次打开默认展示对方商品。
  const [role, setRole] = useState<ChatItemRole>('peer');
  // queryDraft 保存用户尚未提交的平台搜索词。
  const [queryDraft, setQueryDraft] = useState('');
  // appliedQuery 保存最近一次已提交的平台搜索词。
  const [appliedQuery, setAppliedQuery] = useState('');
  // reloadKey 允许相同搜索词再次提交时显式刷新第一页。
  const [reloadKey, setReloadKey] = useState(0);
  // items 保存当前标签和搜索条件下已加载的商品。
  const [items, setItems] = useState<ChatItem[]>([]);
  // page 保存最近成功加载的页码。
  const [page, setPage] = useState(1);
  // hasMore 表示平台声明是否存在下一页。
  const [hasMore, setHasMore] = useState(false);
  // loading 表示第一页请求是否进行中。
  const [loading, setLoading] = useState(false);
  // loadingMore 表示下一页请求是否进行中。
  const [loadingMore, setLoadingMore] = useState(false);
	// sendingItemID 标识当前发送项；非空时弹窗会禁用全部发送入口，保持单次投递语义。
  const [sendingItemID, setSendingItemID] = useState('');
  // error 保存最新可见错误，成功请求会清除旧错误。
  const [error, setError] = useState('');
  // listSequence 隔离标签、搜索、会话切换和分页产生的旧响应。
  const listSequence = useRef(0);
  // listController 保存当前商品列表请求控制器。
  const listController = useRef<AbortController | null>(null);
  // sendSequence 隔离关闭弹窗或切换会话后的旧发送响应。
  const sendSequence = useRef(0);
  // sendController 保存当前商品发送请求控制器。
  const sendController = useRef<AbortController | null>(null);

  useEffect(/* 当前副作用在弹窗打开或查询条件变化时加载第一页，并取消旧请求。 */ () => {
    listController.current?.abort();
    // sequence 是当前第一页请求代次。
    const sequence = ++listSequence.current;
    if (!open || !accountID || !chatID) {
      setLoading(false);
      setLoadingMore(false);
      return;
    }
    // controller 负责取消当前第一页商品查询。
    const controller = new AbortController();
    listController.current = controller;
    setItems([]);
    setPage(1);
    setHasMore(false);
    setLoading(true);
    setLoadingMore(false);
    setError('');
    /** loadFirstPage 执行当前标签和搜索词的第一页查询。 */
    const loadFirstPage = async (): Promise<void> => {
      try {
        // result 是当前第一页平台商品响应。
        const result = await getChatItems(accountID, chatID, role, appliedQuery, 1, { signal: controller.signal });
        if (!isCurrentChatRequest(listSequence.current, sequence, controller.signal)) return;
        setItems(result.items);
        setPage(result.page);
        setHasMore(result.has_more);
      } catch (/* requestError 是商品第一页查询失败原因。 */ requestError) {
        if (isCurrentChatRequest(listSequence.current, sequence, controller.signal) && !isChatAbortError(requestError)) {
          setError(requestError instanceof Error ? requestError.message : '商品加载失败');
        }
      } finally {
        if (isCurrentChatRequest(listSequence.current, sequence, controller.signal)) setLoading(false);
      }
    };
    void loadFirstPage();
    return /* cleanup 取消已失去页面所有权的商品查询。 */ () => controller.abort();
  }, [accountID, appliedQuery, chatID, open, reloadKey, role]);

	useEffect(/* 当前副作用在关闭、会话切换或卸载时取消未完成发送，防止旧会话回写。 */ () => {
		// 当前上下文开始时清理上一个会话的发送占用；旧响应由 cleanup 的代次推进隔离。
		setSendingItemID('');
		return /* cleanup 取消失去弹窗上下文所有权的发送请求，并使忽略 AbortSignal 的旧 Promise 失效。 */ () => {
			sendController.current?.abort();
			++sendSequence.current;
		};
	}, [accountID, chatID, open]);

  /** 切换商品归属并清空搜索，状态变化会触发第一页请求。 */
  const selectRole = useCallback(/* nextRole 是用户选中的商品归属。 */ (nextRole: ChatItemRole): void => {
    setQueryDraft('');
    setAppliedQuery('');
    setError('');
    if (nextRole === role) setReloadKey(/* current 是同标签显式刷新使用的递增键。 */ current => current + 1);
    else setRole(nextRole);
  }, [role]);

  /** 提交输入框搜索词；输入变化本身不会请求平台。 */
  const submitSearch = useCallback(/* 当前回调应用搜索词并触发第一页刷新。 */ (): void => {
    // normalized 是去除首尾空白后的平台搜索词。
    const normalized = queryDraft.trim();
    setAppliedQuery(normalized);
    setReloadKey(/* current 是允许相同搜索词再次刷新的递增键。 */ current => current + 1);
  }, [queryDraft]);

  /** 加载下一页并拒绝重复触底请求及过期响应。 */
  const loadMore = useCallback(/* 当前回调加载当前查询的下一页。 */ async (): Promise<void> => {
    if (!open || !accountID || !chatID || loading || loadingMore || !hasMore) return;
    listController.current?.abort();
    // sequence 是当前分页请求代次。
    const sequence = ++listSequence.current;
    // controller 负责取消当前分页商品查询。
    const controller = new AbortController();
    listController.current = controller;
    // nextPage 是平台从 1 开始的下一页页码。
    const nextPage = page + 1;
    setLoadingMore(true);
    setError('');
    try {
      // result 是当前查询条件下的下一页响应。
      const result = await getChatItems(accountID, chatID, role, appliedQuery, nextPage, { signal: controller.signal });
      if (!isCurrentChatRequest(listSequence.current, sequence, controller.signal)) return;
      setItems(/* current 是加载下一页前已有的商品列表。 */ current => mergeChatItems(current, result.items));
      setPage(result.page);
      setHasMore(result.has_more);
    } catch (/* requestError 是商品分页请求失败原因。 */ requestError) {
      if (isCurrentChatRequest(listSequence.current, sequence, controller.signal) && !isChatAbortError(requestError)) {
        setError(requestError instanceof Error ? requestError.message : '加载更多商品失败');
      }
    } finally {
      if (isCurrentChatRequest(listSequence.current, sequence, controller.signal)) setLoadingMore(false);
    }
  }, [accountID, appliedQuery, chatID, hasMore, loading, loadingMore, open, page, role]);

  /** 发送当前商品；失败时保留弹窗与列表，成功或远端已确认时合并消息并关闭。 */
  const sendItem = useCallback(/* item 是用户点击发送的商品快照。 */ async (item: ChatItem): Promise<void> => {
    if (!open || !accountID || !chatID || sendingItemID) return;
    sendController.current?.abort();
    // sequence 是当前商品发送请求代次。
    const sequence = ++sendSequence.current;
    // controller 负责在弹窗关闭或新发送开始时取消当前发送请求。
    const controller = new AbortController();
    sendController.current = controller;
    setSendingItemID(item.item_id);
    setError('');
    try {
      // result 是后端出站状态机确认后的商品消息。
      const result = await sendChatItemCard(accountID, chatID, item, { signal: controller.signal });
      if (!isCurrentChatRequest(sendSequence.current, sequence, controller.signal)) return;
		if (result.message.account_id !== accountID || result.message.chat_id !== chatID) {
			setError('商品卡片返回的会话与当前会话不一致，请刷新后重试');
			return;
		}
      onSent(result.message);
      onClose();
    } catch (/* sendError 是商品卡片发送或本地状态收口失败原因。 */ sendError) {
      if (!isCurrentChatRequest(sendSequence.current, sequence, controller.signal)) return;
      // confirmed 是平台已发送、仅本地状态收口失败时返回的不可重发消息。
      const confirmed = confirmedOutgoingMessageFromError(sendError);
      if (confirmed && confirmed.account_id === accountID && confirmed.chat_id === chatID) {
        onSent(confirmed, '商品卡片已发送，但本地状态同步失败，请刷新会话确认状态。');
        onClose();
      } else if (!isChatAbortError(sendError)) {
        // uncertain 保存未知结果消息，必须展示核对提示并结束本次发送，不能开放普通重试。
        const uncertain = uncertainOutgoingMessageFromError(sendError);
        if (uncertain && uncertain.account_id === accountID && uncertain.chat_id === chatID) {
          onSent(uncertain, '商品卡片发送结果待确认，请先到闲鱼核对，避免重复发送。');
          onClose();
          return;
        }
        setError(sendError instanceof Error ? sendError.message : '商品卡片发送失败');
      }
    } finally {
      if (isCurrentChatRequest(sendSequence.current, sequence, controller.signal)) setSendingItemID('');
    }
  }, [accountID, chatID, onClose, onSent, open, sendingItemID]);

  return { role, queryDraft, items, hasMore, loading, loadingMore, sendingItemID, error, setQueryDraft, selectRole, submitSearch, loadMore, sendItem };
};
