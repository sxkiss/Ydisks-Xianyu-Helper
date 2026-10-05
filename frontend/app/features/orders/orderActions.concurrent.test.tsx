// @vitest-environment jsdom
import { useState } from 'react';
import { act, renderHook } from '@testing-library/react';
import { afterEach, expect, test, vi } from 'vitest';
import type { Order } from './api';
import { useOrderActions } from './orderActions';
import { deleteOrder, manualShipOrder } from './api';

// API 替身仅控制订单请求完成时机，不发起远端动作。
vi.mock('./api', () => ({ deleteOrder: vi.fn(), manualShipOrder: vi.fn(), syncOrders: vi.fn(), syncSingleOrder: vi.fn(), updateOrder: vi.fn() }));
// order 是分页只剩一条记录时的真实模型夹具。
const order: Order = { id: 'row', order_id: 'a', cookie_id: 'a', item_id: 'item', item_title: '商品', buyer_id: 'buyer', quantity: 1, amount: '1', status: 'pending_ship' };

// deferred 为类型 T 的请求保存完成与失败入口，不依赖计时器制造交错。
const deferred = <T,>() => {
  // resolve 允许测试提交类型正确的 API 响应。
  let resolve!: (value: T) => void;
  // reject 允许测试模拟晚到网络错误。
  let reject!: (reason: Error) => void;
  // promise 是交给生产 Hook 等待的请求。
  const promise = new Promise<T>(/* done、fail 分别接收请求成功和失败入口。 */ (done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
};

afterEach(/* 恢复浏览器替身并清除 API 调用记录。 */ () => { vi.restoreAllMocks(); vi.resetAllMocks(); });

test.each(['account', 'status', 'search', 'page', 'roundtrip'])('删除期间切换 %s 后旧响应不得改变分页或刷新列表', /* change 指定当前查询切换方式。 */ async change => {
  // pending 控制删除响应到达时间。
  const pending = deferred<Awaited<ReturnType<typeof deleteOrder>>>();
  vi.mocked(deleteOrder).mockReturnValue(pending.promise);
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  // loadOrders 检查过期动作不会调用旧查询闭包。
  const loadOrders = vi.fn().mockResolvedValue(undefined);
  // hook 用真实 React state 持有查询条件，重现页面切换。
  const hook = renderHook(/* 创建带真实页码的动作状态。 */ () => {
    // page、setPage 管理当前页码。
    const [page, setPage] = useState(2);
    // accountFilter、setAccountFilter 管理账号筛选。
    const [accountFilter, setAccountFilter] = useState('a');
    // filter、setFilter 管理订单状态筛选。
    const [filter, setFilter] = useState('all');
    // searchText、setSearchText 管理文本筛选。
    const [searchText, setSearchText] = useState('');
    // actions 处理当前查询的订单操作。
    const actions = useOrderActions({ page, accountFilter, filter, searchText, orders: [order], setPage, loadOrders });
    return { ...actions, page, setPage, setAccountFilter, setFilter, setSearchText };
  });
  // task 保留第二页尚未完成的删除。
  let task!: Promise<void>;
  act(/* 在旧页面提交删除。 */ () => { task = hook.result.current.handleDelete('a'); });
  act(/* 切换查询条件并回到第一页。 */ () => {
    if (change === 'account' || change === 'roundtrip') hook.result.current.setAccountFilter('b');
    if (change === 'status') hook.result.current.setFilter('paid');
    if (change === 'search') hook.result.current.setSearchText('商品');
    hook.result.current.setPage(1);
  });
  if (change === 'roundtrip') act(/* 切回同一账号仍不能复活旧请求。 */ () => hook.result.current.setAccountFilter('a'));
  expect(hook.result.current.deletingOrderId).toBeNull();
  await act(/* 释放旧删除结果并等待 finally 收束。 */ async () => { pending.resolve({ success: true }); await task; });
  expect(hook.result.current.page).toBe(1);
  expect(loadOrders).not.toHaveBeenCalled();
  hook.unmount();
});

test.each(['success', 'failure', 'unmount'])('旧发货 %s 结果不得写入另一弹窗或刷新旧列表', /* outcome 指定请求终态或卸载方式。 */ async outcome => {
  // pending 控制旧订单发货完成时机。
  const pending = deferred<Awaited<ReturnType<typeof manualShipOrder>>>();
  vi.mocked(manualShipOrder).mockReturnValue(pending.promise);
  // loadOrders 记录过期发货是否错误刷新列表。
  const loadOrders = vi.fn().mockResolvedValue(undefined);
  // hook 是当前订单动作状态实例。
  const hook = renderHook(/* 创建可切换弹窗的动作状态。 */ () => useOrderActions({ orders: [], page: 1, accountFilter: '', filter: 'all', setPage: vi.fn(), loadOrders }));
  act(/* 选择旧订单。 */ () => hook.result.current.handleShip('a'));
  // task 保存订单 A 的在途发货请求。
  let task!: Promise<void>;
  act(/* 提交订单 A 发货。 */ () => { task = hook.result.current.executeShip('full_delivery'); });
  act(/* 关闭旧弹窗并选择订单 B。 */ () => { hook.result.current.closeShipModal(); hook.result.current.handleShip('b'); });
  expect(hook.result.current.shipLoading).toBe(true);
  await act(/* 在途发货未收束时不能重复提交。 */ async () => { await hook.result.current.executeShip('full_delivery'); });
  expect(manualShipOrder).toHaveBeenCalledTimes(1);
  if (outcome === 'unmount') hook.unmount();
  await act(/* 释放旧订单响应。 */ async () => {
    if (outcome === 'failure') pending.reject(new Error('旧订单失败'));
    else pending.resolve({ partial_failure: false, message: '完成', success_count: 1, failed_count: 0, results: [{ success: true, message: '订单 A 发货成功' }] });
    await task;
  });
  expect(loadOrders).not.toHaveBeenCalled();
  if (outcome !== 'unmount') {
    expect(hook.result.current.shipOrderId).toBe('b');
    expect(hook.result.current.shipResult).toBeNull();
    expect(hook.result.current.shipLoading).toBe(false);
    hook.unmount();
  }
});

test('旧删除失败不能清理新删除的忙碌状态或弹出过期错误', /* 验证连续删除请求的结果归属。 */ async () => {
  // oldRequest、newRequest 控制连续删除的响应顺序。
  const oldRequest = deferred<Awaited<ReturnType<typeof deleteOrder>>>();
  // newRequest 是当前用户等待的删除请求。
  const newRequest = deferred<Awaited<ReturnType<typeof deleteOrder>>>();
  vi.mocked(deleteOrder).mockReturnValueOnce(oldRequest.promise).mockReturnValueOnce(newRequest.promise);
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  // alertSpy 检查过期错误不会弹窗。
  const alertSpy = vi.spyOn(window, 'alert').mockImplementation(/* 屏蔽本地错误弹窗。 */ () => undefined);
  // loadOrders 只应由当前删除刷新。
  const loadOrders = vi.fn().mockResolvedValue(undefined);
  // hook 管理相同查询中的连续删除。
  const hook = renderHook(/* 使用第一页确保成功删除走列表刷新。 */ () => useOrderActions({ orders: [order], page: 1, accountFilter: 'a', filter: 'all', setPage: vi.fn(), loadOrders }));
  // oldTask 保存旧删除任务。
  let oldTask!: Promise<void>;
  // newTask 保存当前删除任务。
  let newTask!: Promise<void>;
  act(/* 先后提交两笔删除。 */ () => { oldTask = hook.result.current.handleDelete('a'); newTask = hook.result.current.handleDelete('b'); });
  await act(/* 释放旧失败。 */ async () => { oldRequest.reject(new Error('旧删除失败')); await oldTask; });
  expect(hook.result.current.deletingOrderId).toBe('b');
  expect(alertSpy).not.toHaveBeenCalled();
  expect(loadOrders).not.toHaveBeenCalled();
  await act(/* 当前删除成功才刷新页面。 */ async () => { newRequest.resolve({ success: true }); await newTask; });
  expect(hook.result.current.deletingOrderId).toBeNull();
  expect(loadOrders).toHaveBeenCalledTimes(1);
  hook.unmount();
});
