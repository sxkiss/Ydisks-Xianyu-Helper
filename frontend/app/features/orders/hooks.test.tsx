// @vitest-environment jsdom
import { act,renderHook,waitFor } from '@testing-library/react';
import { beforeEach,describe,expect,test,vi } from 'vitest';
import type { AccountDetail,Item } from './api';
import { getAccountDetails,getItems,getOrders } from './api';
import { useOrderQuery } from './hooks';

vi.mock('./api', /* ordersApiMockFactory 提供订单 Hook 的确定性 API 替身。 */ () => ({
  getAccountDetails: vi.fn(),
  getItems: vi.fn(),
  getOrders: vi.fn(),
}));

// getAccountsMock 是订单辅助账号请求的可控替身。
const getAccountsMock = vi.mocked(getAccountDetails);
// getItemsMock 是订单辅助商品请求的可控替身。
const getItemsMock = vi.mocked(getItems);
// getOrdersMock 是订单分页请求的可控替身。
const getOrdersMock = vi.mocked(getOrders);

// accountFixture 是订单筛选账号测试对象。
const accountFixture: AccountDetail = { id: 'account-1', enabled: true, auto_confirm: false, nickname: '测试账号', remark: '账号备注' };
// itemFixture 是订单商品名称映射测试对象。
const itemFixture: Item = { id: 'item-row', cookie_id: 'account-1', item_id: 'item-1', item_title: '测试商品' };
// orderFixture 是订单列表测试使用的最小订单对象。
const orderFixture = { id: 'order-1', order_id: 'order-1', cookie_id: 'account-1', item_id: 'item-1', item_title: '', status: 'pending_ship', buyer_id: 'buyer-1' } as never;


describe('useOrderQuery', /* 当前回调处理订单查询和导入 Hook 的请求边界。 */ () => {
  beforeEach(/* 当前回调重置订单 API 替身和浏览器提示。 */ () => {
    vi.clearAllMocks();
    getAccountsMock.mockResolvedValue([accountFixture]);
    getItemsMock.mockResolvedValue([itemFixture]);
    getOrdersMock.mockResolvedValue({ success: true, data: [orderFixture], total: 1, page: 1, page_size: 20, total_pages: 2, trigger_counts: {} });
    vi.spyOn(window, 'alert').mockImplementation(
      // alertImplementation 屏蔽订单导入成功时的浏览器提示。
      () => undefined,
    );
  });

  test('订单查询加载辅助数据并提供展示名称解析', /* 当前回调验证订单列表 Hook 成功路径。 */ async () => {
    // hook 是订单查询 Hook 的渲染结果。
    const hook = renderHook(
      // queryHookFactory 创建订单查询 Hook。
      () => useOrderQuery({ pageSize: 20 }),
    );
    await waitFor(
      // loadingAssertion 等待订单请求完成。
      () => expect(hook.result.current.loading).toBe(false),
    );
    expect(hook.result.current.orders).toEqual([orderFixture]);
    expect(hook.result.current.accounts).toEqual([accountFixture]);
    expect(hook.result.current.items).toEqual([itemFixture]);
    expect(hook.result.current.totalPages).toBe(2);
    expect(hook.result.current.accountName('account-1')).toBe('账号备注 · accoun');
    expect(hook.result.current.accountNickname('account-1')).toBe('账号备注');
    expect(hook.result.current.getItemNameById('account-1', 'item-1')).toBe('测试商品');
    expect(hook.result.current.getItemNameById('account-1', 'missing', '订单商品标题')).toBe('订单商品标题');
    expect(hook.result.current.getItemNameById('account-1', 'missing')).toBe('未知商品');
    expect(hook.result.current.accountName('missing-account')).toBe('账号 missing-');
    expect(hook.result.current.accountNickname('missing-account')).toBe('未命名账号');
  });

  test('订单查询失败时记录错误并结束加载状态', /* 当前回调验证订单列表请求错误收口。 */ async () => {
    getOrdersMock.mockRejectedValueOnce(new Error('订单查询失败'));
    // consoleError 是订单查询错误日志的可控替身。
    const consoleError = vi.spyOn(console, 'error').mockImplementation(/* errorLogger 忽略测试日志输出。 */ () => undefined);
    // hook 是订单查询失败场景的 Hook 渲染结果。
    const hook = renderHook(
      // queryFailureHookFactory 创建订单查询失败场景的 Hook。
      () => useOrderQuery({ pageSize: 20 }),
    );
    await waitFor(
      // loadingAssertion 等待订单查询失败后的加载状态收口。
      () => expect(hook.result.current.loading).toBe(false),
    );
    expect(consoleError).toHaveBeenCalledWith('加载订单失败:', expect.any(Error));
    hook.unmount();
    consoleError.mockRestore();
  });

  test('订单搜索输入经过防抖后以去空格文本重新查询', /* 当前回调验证订单搜索防抖和参数标准化。 */ async () => {
    // hook 是订单搜索防抖场景的 Hook 渲染结果。
    const hook = renderHook(
      // searchHookFactory 创建订单搜索场景的 Hook。
      () => useOrderQuery({ pageSize: 20 }),
    );
    await waitFor(
      // loadingAssertion 等待订单初始查询完成。
      () => expect(hook.result.current.loading).toBe(false),
    );
    await act(
      // searchAction 写入带有首尾空格的搜索文本。
      () => hook.result.current.setSearchText('  买家  '),
    );
    await waitFor(
      // searchAssertion 等待防抖查询携带标准化搜索文本。
      () => expect(getOrdersMock).toHaveBeenLastCalledWith(undefined, 'all', 1, 20, '买家', expect.objectContaining({ signal: expect.any(AbortSignal) })),
      { timeout: 1_000 },
    );
    hook.unmount();
  });

  test('订单账号商品辅助数据失败时仅记录辅助数据错误', /* 当前回调验证订单展示辅助请求的独立失败分支。 */ async () => {
    getAccountsMock.mockRejectedValueOnce(new Error('账号辅助数据失败'));
    // consoleError 是辅助数据错误日志的可控替身。
    const consoleError = vi.spyOn(console, 'error').mockImplementation(/* errorLogger 忽略测试日志输出。 */ () => undefined);
    // hook 是订单辅助数据失败场景的 Hook 渲染结果。
    const hook = renderHook(
      // auxiliaryFailureHookFactory 创建订单辅助数据失败场景的 Hook。
      () => useOrderQuery({ pageSize: 20 }),
    );
    await waitFor(
      // loadingAssertion 等待订单主查询完成。
      () => expect(hook.result.current.loading).toBe(false),
    );
    await waitFor(
      // auxiliaryErrorAssertion 等待辅助数据错误日志产生。
      () => expect(consoleError).toHaveBeenCalledWith('加载订单辅助数据失败:', expect.any(Error)),
    );
    expect(hook.result.current.orders).toEqual([orderFixture]);
    hook.unmount();
    consoleError.mockRestore();
  });

  test('重复查询订单时丢弃先发出的旧响应', /* 当前回调验证订单列表请求代次隔离。 */ async () => {
    // OrderPageResponse 是旧订单查询使用的最小成功响应。
    type OrderPageResponse = {
      // success 表示请求成功。
      success: true;
      // data 保存订单列表。
      data: typeof orderFixture[];
      // total 保存订单总数。
      total: number;
      // page 保存当前页码。
      page: number;
      // pageSize 保存分页大小。
      page_size: number;
      // totalPages 保存总页数。
      total_pages: number;
      // triggerCounts 保存状态聚合数量。
      trigger_counts: Record<string, number>;
    };
    // resolveFirst 是旧订单查询的完成控制器。
    let resolveFirst: (value: OrderPageResponse) => void = () => undefined;
    // firstRequest 是保持未完成的旧订单查询 Promise。
    const firstRequest = new Promise<OrderPageResponse>(/* firstExecutor 保存旧请求完成函数。 */ resolve => { resolveFirst = resolve; });
    getOrdersMock.mockReset();
    getOrdersMock.mockReturnValueOnce(firstRequest);
    getOrdersMock.mockResolvedValue({ success: true, data: [orderFixture], total: 1, page: 1, page_size: 20, total_pages: 2, trigger_counts: {} });
    // hook 是订单查询刷新竞态场景的 Hook 渲染结果。
    const hook = renderHook(
      // staleOrderHookFactory 创建订单旧响应场景的 Hook。
      () => useOrderQuery({ pageSize: 20 }),
    );
    await act(
      // refreshAction 发起第二次订单查询并使首次请求过期。
      async () => hook.result.current.loadOrders(),
    );
    resolveFirst({ success: true, data: [orderFixture], total: 1, page: 1, page_size: 20, total_pages: 2, trigger_counts: {} });
    await act(
      // staleResolveAction 完成已过期的首次订单响应。
      async () => { await firstRequest; },
    );
    expect(hook.result.current.orders).toEqual([orderFixture]);
    hook.unmount();
  });
});
