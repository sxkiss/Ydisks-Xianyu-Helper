// @vitest-environment jsdom
import { act,fireEvent,render,renderHook,screen,waitFor } from '@testing-library/react';
import { beforeEach,describe,expect,test,vi } from 'vitest';
import { confirmedOutgoingMessageFromError,getChatItems,sendChatItemCard,uncertainOutgoingMessageFromError } from './api';
import { ChatItemPickerDialog } from './components/ChatItemPickerDialog';
import type { ChatItem,ChatMessage } from './models';
import { useChatItemPicker } from './useChatItemPicker';

vi.mock('./api', /* chatItemApiMockFactory 提供商品选择 Hook 的确定性 API 替身。 */ () => ({
  getChatItems: vi.fn(),
  sendChatItemCard: vi.fn(),
  confirmedOutgoingMessageFromError: vi.fn(),
  uncertainOutgoingMessageFromError: vi.fn(),
}));

// getChatItemsMock 是商品分页请求的可控替身。
const getChatItemsMock = vi.mocked(getChatItems);
// sendChatItemCardMock 是商品卡片发送请求的可控替身。
const sendChatItemCardMock = vi.mocked(sendChatItemCard);
// confirmedOutgoingMessageMock 是远端已发但本地状态收口失败的适配器替身。
const confirmedOutgoingMessageMock = vi.mocked(confirmedOutgoingMessageFromError);
// firstItem 是商品选择测试中的第一页商品。
const firstItem: ChatItem = { item_id: 'item-1', title: '第一件宝贝', image_url: 'https://img.example/1.png', price: '10.00', description: '第一页摘要' };
// secondItem 是商品选择测试中的第二页商品。
const secondItem: ChatItem = { item_id: 'item-2', title: '第二件宝贝', image_url: 'https://img.example/2.png', price: '20.00' };
// sentMessage 是商品卡片发送成功后返回的规范聊天消息。
const sentMessage: ChatMessage = { id: 3, account_id: 'account-1', chat_id: 'chat-1', message_key: 'item-message', direction: 'outgoing', sender_id: 'self', sender_name: '我', message_type: 'item', content: '{"item_id":"item-1","title":"第一件宝贝","image_url":"https://img.example/1.png","price":"10.00"}', status: 'sent', sent_at: 1 };

/** PickerProps 是会话切换测试传给 Hook 的最小属性。 */
interface PickerProps {
  /** chatID 是当前测试渲染使用的会话标识。 */
  chatID: string;
}

/** ItemPageFixture 是受控旧请求完成时使用的商品分页形状。 */
interface ItemPageFixture {
  /** items 是当前页商品快照。 */
  items: ChatItem[];
  /** page 是从 1 开始的当前页码。 */
  page: number;
  /** has_more 表示测试分页是否仍有下一页。 */
  has_more: boolean;
}

/** deferred 创建可由测试显式完成的 Promise，用于验证旧请求不会覆盖新上下文。 */
const deferred = <T,>(): { /** promise 是等待测试完成的异步结果。 */ promise: Promise<T>; /** resolve 使用指定结果完成 Promise。 */ resolve: (value: T) => void } => {
  // resolvePromise 保存由 Promise 构造器提供的完成函数。
  let resolvePromise!: (value: T) => void;
  // promise 是交给被测 Hook 等待的可控异步结果。
  const promise = new Promise<T>(/* executor 捕获 Promise 完成函数供测试后续调用。 */ resolve => { resolvePromise = resolve; });
  return { promise, resolve: resolvePromise };
};

describe('useChatItemPicker', /* 当前测试组覆盖商品弹窗查询、分页、取消和发送状态。 */ () => {
  beforeEach(/* 当前回调重置商品 API 替身并提供默认成功响应。 */ () => {
    vi.clearAllMocks();
    getChatItemsMock.mockResolvedValue({ items: [firstItem], page: 1, has_more: true });
    sendChatItemCardMock.mockResolvedValue({ message: sentMessage });
    confirmedOutgoingMessageMock.mockReturnValue(undefined);
    vi.mocked(uncertainOutgoingMessageFromError).mockReturnValue(undefined);
  });

  test('默认查询 TA 的宝贝，搜索需提交且切换标签会清空搜索', /* 当前回调验证默认标签和提交式搜索语义。 */ async () => {
    // onSent 是成功发送回调替身。
    const onSent = vi.fn();
    // onClose 是弹窗关闭回调替身。
    const onClose = vi.fn();
    // hook 是当前商品选择状态的渲染结果。
    const hook = renderHook(/* pickerFactory 创建固定会话的商品选择 Hook。 */ () => useChatItemPicker({ open: true, accountID: 'account-1', chatID: 'chat-1', onSent, onClose }));
    await waitFor(/* firstPageAssertion 等待默认商品页加载。 */ () => expect(hook.result.current.items).toEqual([firstItem]));
    expect(getChatItemsMock).toHaveBeenLastCalledWith('account-1', 'chat-1', 'peer', '', 1, expect.objectContaining({ signal: expect.any(AbortSignal) }));

    await act(/* draftAction 只编辑搜索输入，不提交请求。 */ () => hook.result.current.setQueryDraft('麦克风'));
    expect(getChatItemsMock).toHaveBeenCalledTimes(1);
    await act(/* submitAction 提交搜索条件并触发第一页刷新。 */ () => hook.result.current.submitSearch());
    await waitFor(/* searchAssertion 等待搜索请求到达平台适配器。 */ () => expect(getChatItemsMock).toHaveBeenLastCalledWith('account-1', 'chat-1', 'peer', '麦克风', 1, expect.anything()));

    await act(/* roleAction 切换到当前账号商品并清空搜索。 */ () => hook.result.current.selectRole('self'));
    expect(hook.result.current.queryDraft).toBe('');
    await waitFor(/* roleAssertion 等待 own 语义对应的 self 查询。 */ () => expect(getChatItemsMock).toHaveBeenLastCalledWith('account-1', 'chat-1', 'self', '', 1, expect.anything()));
  });

  test('触底分页去重且会话切换后丢弃旧响应', /* 当前回调验证无限分页和请求代次保护。 */ async () => {
    getChatItemsMock
      .mockResolvedValueOnce({ items: [firstItem], page: 1, has_more: true })
      .mockResolvedValueOnce({ items: [{ ...firstItem, title: '更新后的第一件宝贝' }, secondItem], page: 2, has_more: false });
    // onSent 是当前场景未触发的发送回调。
    const onSent = vi.fn();
    // onClose 是当前场景未触发的关闭回调。
    const onClose = vi.fn();
    // hook 是可切换会话属性的商品选择 Hook。
    const hook = renderHook(/* pickerFactory 根据测试属性创建商品选择 Hook。 */ ({ chatID }: PickerProps) => useChatItemPicker({ open: true, accountID: 'account-1', chatID, onSent, onClose }), { initialProps: { chatID: 'chat-1' } });
    await waitFor(/* firstPageAssertion 等待第一页加载完成。 */ () => expect(hook.result.current.loading).toBe(false));
    await act(/* nextPageAction 模拟列表触底加载下一页。 */ async () => hook.result.current.loadMore());
    expect(hook.result.current.items.map(/* item 是分页合并后的商品。 */ item => item.item_id)).toEqual(['item-1', 'item-2']);
    expect(hook.result.current.items[0].title).toBe('更新后的第一件宝贝');
    expect(hook.result.current.hasMore).toBe(false);

    // oldRequest 是旧会话尚未完成的商品请求。
    const oldRequest = deferred<ItemPageFixture>();
    getChatItemsMock.mockReturnValueOnce(oldRequest.promise).mockResolvedValueOnce({ items: [secondItem], page: 1, has_more: false });
    await act(/* refreshOldChatAction 在旧会话重新提交相同搜索以启动慢请求。 */ () => hook.result.current.submitSearch());
    await act(/* switchChatAction 在慢请求完成前切换到新会话。 */ () => hook.rerender({ chatID: 'chat-2' }));
    await waitFor(/* newChatAssertion 等待新会话商品先完成。 */ () => expect(hook.result.current.items).toEqual([secondItem]));
    await act(/* resolveOldAction 延迟完成旧会话请求。 */ async () => oldRequest.resolve({ items: [firstItem], page: 1, has_more: false }));
    expect(hook.result.current.items).toEqual([secondItem]);
  });

  test('发送失败保留弹窗，成功后合并消息并关闭', /* 当前回调验证单商品发送的失败重试和成功关闭语义。 */ async () => {
    // onSent 收集发送成功后的聊天消息。
    const onSent = vi.fn();
    // onClose 收集商品弹窗关闭动作。
    const onClose = vi.fn();
    // hook 是商品发送场景的 Hook 渲染结果。
    const hook = renderHook(/* pickerFactory 创建商品发送场景 Hook。 */ () => useChatItemPicker({ open: true, accountID: 'account-1', chatID: 'chat-1', onSent, onClose }));
    await waitFor(/* loadedAssertion 等待可发送商品出现。 */ () => expect(hook.result.current.items).toEqual([firstItem]));
    sendChatItemCardMock.mockRejectedValueOnce(new Error('平台暂时不可用'));
    await act(/* failedSendAction 首次发送模拟平台失败。 */ async () => hook.result.current.sendItem(firstItem));
    expect(hook.result.current.error).toBe('平台暂时不可用');
    expect(onClose).not.toHaveBeenCalled();

    await act(/* successfulSendAction 重试同一商品并完成发送。 */ async () => hook.result.current.sendItem(firstItem));
    expect(onSent).toHaveBeenCalledWith(sentMessage);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

	test('切换会话会取消发送并丢弃忽略取消信号的旧响应', /* 当前回调验证发送请求不会回写已经失去所有权的聊天。 */ async () => {
		// request 是刻意忽略 AbortSignal 的慢发送响应，用于验证代次保护。
		const request = deferred<{ /** message 是后端返回的商品聊天消息。 */ message: ChatMessage }>();
		sendChatItemCardMock.mockReturnValue(request.promise);
		// onSent 和 onClose 记录旧响应是否错误影响当前页面。
		const onSent = vi.fn();
		const onClose = vi.fn();
		// hook 是可切换会话且稍后卸载的商品选择状态。
		const hook = renderHook(/* pickerFactory 根据当前会话属性构造 Hook。 */ ({ chatID }: PickerProps) => useChatItemPicker({ open: true, accountID: 'account-1', chatID, onSent, onClose }), { initialProps: { chatID: 'chat-1' } });
		await waitFor(/* loadedAssertion 等待商品列表加载完成。 */ () => expect(hook.result.current.items).toEqual([firstItem]));
		// pendingSend 保存尚未完成的发送 Promise。
		let pendingSend!: Promise<void>;
		act(/* startSendAction 启动慢发送但不等待其完成。 */ () => { pendingSend = hook.result.current.sendItem(firstItem); });
		await waitFor(/* sendingAssertion 等待 Hook 进入发送状态。 */ () => expect(hook.result.current.sendingItemID).toBe('item-1'));
		// signal 是 API 适配器收到的取消信号。
		const signal = sendChatItemCardMock.mock.calls[0]?.[3]?.signal;
		await act(/* switchChatAction 在慢发送完成前切换到另一会话。 */ () => hook.rerender({ chatID: 'chat-2' }));
		expect(signal?.aborted).toBe(true);
		await act(/* resolveStaleAction 完成已经过期且忽略取消的旧请求。 */ async () => {
			request.resolve({ message: sentMessage });
			await pendingSend;
		});
		expect(onSent).not.toHaveBeenCalled();
		expect(onClose).not.toHaveBeenCalled();
		hook.unmount();
	});

	test('卸载弹窗会取消发送并忽略随后完成的响应', /* 当前回调验证关闭页面后旧发送结果不会触发残留回调。 */ async () => {
		// request 是弹窗卸载后才完成的慢发送响应。
		const request = deferred<{ /** message 是后端返回的商品聊天消息。 */ message: ChatMessage }>();
		sendChatItemCardMock.mockReturnValue(request.promise);
		// onSent 和 onClose 记录已经卸载的 Hook 是否被旧响应调用。
		const onSent = vi.fn();
		const onClose = vi.fn();
		// hook 是将在发送期间直接卸载的商品选择状态。
		const hook = renderHook(/* pickerFactory 创建固定会话的商品选择 Hook。 */ () => useChatItemPicker({ open: true, accountID: 'account-1', chatID: 'chat-1', onSent, onClose }));
		await waitFor(/* loadedAssertion 等待商品列表加载完成。 */ () => expect(hook.result.current.items).toEqual([firstItem]));
		// pendingSend 保存跨越卸载时刻的发送 Promise。
		let pendingSend!: Promise<void>;
		act(/* startSendAction 启动慢发送但不等待其完成。 */ () => { pendingSend = hook.result.current.sendItem(firstItem); });
		await waitFor(/* sendingAssertion 等待 Hook 进入发送状态。 */ () => expect(hook.result.current.sendingItemID).toBe('item-1'));
		// signal 是卸载时必须被终止的发送请求信号。
		const signal = sendChatItemCardMock.mock.calls[0]?.[3]?.signal;
		hook.unmount();
		expect(signal?.aborted).toBe(true);
		await act(/* resolveStaleAction 完成已经失去组件所有权的旧请求。 */ async () => {
			request.resolve({ message: sentMessage });
			await pendingSend;
		});
		expect(onSent).not.toHaveBeenCalled();
		expect(onClose).not.toHaveBeenCalled();
	});

	test('发送一件商品时禁用其他发送入口和关闭入口', /* 当前回调验证弹窗对单次投递语义给出一致的可见状态。 */ async () => {
		getChatItemsMock.mockResolvedValue({ items: [firstItem, secondItem], page: 1, has_more: false });
		// request 控制第一件商品的发送完成时机。
		const request = deferred<{ /** message 是商品卡片发送成功结果。 */ message: ChatMessage }>();
		sendChatItemCardMock.mockReturnValue(request.promise);
		// onSent 是弹窗完成商品发送后的消息回调替身。
		const onSent = vi.fn();
		// onClose 是弹窗完成商品发送后的关闭回调替身。
		const onClose = vi.fn();
		render(<ChatItemPickerDialog open accountID="account-1" chatID="chat-1" onSent={onSent} onClose={onClose} />);
		await screen.findByText('第二件宝贝');
		// sendButtons 是两条商品行各自的发送按钮。
		const sendButtons = screen.getAllByRole('button', { name: '发送' });
		fireEvent.click(sendButtons[0]);
		await waitFor(/* disabledAssertion 等待发送状态同步到全部入口。 */ () => {
			expect((sendButtons[0] as HTMLButtonElement).disabled).toBe(true);
			expect((sendButtons[1] as HTMLButtonElement).disabled).toBe(true);
			expect((screen.getByRole('button', { name: '关闭商品选择' }) as HTMLButtonElement).disabled).toBe(true);
		});
		await act(/* finishSendAction 完成发送并释放弹窗状态。 */ async () => request.resolve({ message: sentMessage }));
		expect(onSent).toHaveBeenCalledWith(sentMessage);
	});
  test('未知发送结果展示核对消息并关闭弹窗，跨会话结果不能合并', /* 当前测试覆盖待确认消息及其账号会话隔离。 */ async () => {
    // onSent 和 onClose 记录消息合并与发送流程结束。
    const onSent = vi.fn();
    const onClose = vi.fn();
    // hook 提供固定账号会话的发送入口。
    const hook = renderHook(/* pickerFactory 创建待确认发送场景。 */ () => useChatItemPicker({ open: true, accountID: 'account-1', chatID: 'chat-1', onSent, onClose }));
    await waitFor(/* loadedAssertion 等待商品可发送。 */ () => expect(hook.result.current.items).toEqual([firstItem]));
    sendChatItemCardMock.mockRejectedValue(new Error('发送结果待确认'));
    vi.mocked(uncertainOutgoingMessageFromError).mockReturnValue({ ...sentMessage, chat_id: 'other-chat', status: 'uncertain' });
    await act(/* wrongChatAction 验证错误归属结果不会污染当前会话。 */ async () => hook.result.current.sendItem(firstItem));
    expect(onSent).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    vi.mocked(uncertainOutgoingMessageFromError).mockReturnValue({ ...sentMessage, status: 'uncertain' });
    await act(/* uncertainAction 处理正确归属的待确认消息。 */ async () => hook.result.current.sendItem(firstItem));
    expect(onSent).toHaveBeenCalledWith(expect.objectContaining({ status: 'uncertain', chat_id: 'chat-1' }), expect.stringContaining('核对'));
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(hook.result.current.error).toBe('');
  });


  test('发送中切换会话后解除旧发送占用并忽略迟到成功', /* 当前回调验证取消旧发送与新会话发送能力同时收口。 */ async () => {
    // oldSend 保存第一会话迟到的发送响应。
    const oldSend = deferred<{ /** message 是迟到的旧会话出站消息。 */ message: ChatMessage }>();
    sendChatItemCardMock.mockImplementationOnce(/* 当前回调阻塞第一会话发送。 */ () => oldSend.promise);
    // onSent、onClose 分别记录出站合并和弹窗关闭。
    const onSent = vi.fn();
    const onClose = vi.fn();
    // hook 允许保持弹窗打开并切换当前会话。
    const hook = renderHook(/* props 是当前测试会话身份。 */ (props: PickerProps) => useChatItemPicker({ open: true, accountID: 'account-1', chatID: props.chatID, onSent, onClose }), { initialProps: { chatID: 'chat-1' } });
    await waitFor(/* 当前回调等待商品首页就绪。 */ () => expect(hook.result.current.loading).toBe(false));
    // sending 保存旧发送异步调用，必须在测试结束前完成。
    let sending: Promise<void> | undefined;
    await act(/* 当前回调开始第一会话发送。 */ () => { sending = hook.result.current.sendItem(firstItem); });
    expect(hook.result.current.sendingItemID).toBe(firstItem.item_id);
    hook.rerender({ chatID: 'chat-2' });
    await waitFor(/* 当前回调等待新会话解除发送占用。 */ () => expect(hook.result.current.sendingItemID).toBe(''));
    await act(/* 当前回调完成旧发送，新会话不得收到回调。 */ async () => { oldSend.resolve({ message: sentMessage }); await sending; });
    expect(onSent).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    sendChatItemCardMock.mockResolvedValueOnce({ message: { ...sentMessage, chat_id: 'chat-2' } });
    await act(/* 当前回调发送新会话商品，验证入口没有被旧状态卡住。 */ async () => { await hook.result.current.sendItem(firstItem); });
    expect(onSent).toHaveBeenCalledTimes(1);
    hook.unmount();
  });
});
