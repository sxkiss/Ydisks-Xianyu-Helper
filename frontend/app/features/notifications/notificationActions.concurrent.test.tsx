// @vitest-environment jsdom
import { act, renderHook, waitFor } from '@testing-library/react';
import { afterEach, expect, test, vi } from 'vitest';
import type { NotificationChannel } from './api';
import { useNotifications } from './hooks';
import { createNotificationChannel, deleteNotificationChannel, getNotificationChannels, testNotificationChannel, updateNotificationChannel } from './api';

// API 替身只控制通知请求，不发送任何消息。
vi.mock('./api', () => ({ getNotificationChannels: vi.fn(), getSystemSettings: vi.fn(), getNotificationChannel: vi.fn(), createNotificationChannel: vi.fn(), deleteNotificationChannel: vi.fn(), testNotificationChannel: vi.fn(), updateNotificationChannel: vi.fn(), updateSystemSettings: vi.fn() }));
// channel 是本地构造的渠道摘要。
const channel: NotificationChannel = { id: 'one', name: '测试', type: 'bark', config: {}, enabled: true, event_types: [] };

afterEach(/* 恢复确认框与 API 替身。 */ () => { vi.restoreAllMocks(); vi.resetAllMocks(); });

test.each(['toggle', 'delete', 'save', 'close', 'unmount'])('通知测试被 %s 取消后清除忙碌且忽略取消错误', /* action 指定中断测试的用户动作。 */ async action => {
  vi.mocked(getNotificationChannels).mockResolvedValue({ success: true, data: [channel] });
  vi.mocked(updateNotificationChannel).mockResolvedValue({ success: true });
  vi.mocked(deleteNotificationChannel).mockResolvedValue({ success: true });
  vi.mocked(createNotificationChannel).mockResolvedValue({ success: true, id: 1 });
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  // signal 保存测试请求的取消状态，用于验证实际发出了取消。
  let signal: AbortSignal | undefined;
  vi.mocked(testNotificationChannel).mockImplementation(/* id 是渠道标识，options 提供生产取消信号。 */ (_id, options) => new Promise(/* resolve 保留成功入口，reject 在取消时拒绝。 */ (_resolve, reject) => {
    signal = options?.signal;
    signal?.addEventListener('abort', /* 取消行为与生产契约客户端一致。 */ () => reject(new Error('请求已取消')), { once: true });
  }));
  // hook 是待检验的通知状态。
  const hook = renderHook(/* 创建通知页面状态。 */ () => useNotifications(false));
  await waitFor(/* 等待初次渠道读取收束。 */ () => expect(hook.result.current.loading).toBe(false));
  if (action === 'save') act(/* 填写合法表单以触发保存。 */ () => {
    hook.result.current.openCreate();
    hook.result.current.setForm(/* current 保留默认事件订阅并设置本地测试地址。 */ current => ({ ...current, name: '测试', type: 'bark', config: { server_url: 'https://example.invalid', device_key: 'test' } }));
  });
  // task 是尚未完成的渠道测试请求。
  let task!: Promise<void>;
  act(/* 提交测试请求。 */ () => { task = hook.result.current.handleTest(channel); });
  expect(hook.result.current.testingId).toBe(channel.id);
  await act(/* 新用户动作接管当前请求代次。 */ async () => {
    if (action === 'toggle') await hook.result.current.handleToggleEnabled(channel);
    if (action === 'delete') await hook.result.current.handleDelete(channel);
    if (action === 'save') await hook.result.current.handleSave();
    if (action === 'close') hook.result.current.closeModal();
    if (action === 'unmount') hook.unmount();
  });
  expect(signal?.aborted).toBe(true);
  await act(/* 等待被取消测试的 finally 完成。 */ async () => { await task; });
  if (action !== 'unmount') {
    expect(hook.result.current.testingId).toBe('');
    expect(hook.result.current.toast?.type).not.toBe('error');
    hook.unmount();
  }
});

test('旧测试结束不能清理新渠道测试的忙碌状态或显示旧错误', /* 验证连续两次测试由最新请求拥有提示与忙碌状态。 */ async () => {
  vi.mocked(getNotificationChannels).mockResolvedValue({ success: true, data: [channel] });
  // rejectOld 控制忽略取消的旧传输层错误到达时机。
  let rejectOld!: (error: Error) => void;
  // resolveNew 控制当前渠道测试成功时机。
  let resolveNew!: (value: Awaited<ReturnType<typeof testNotificationChannel>>) => void;
  vi.mocked(testNotificationChannel)
    .mockReturnValueOnce(new Promise(/* resolve 不使用，reject 释放旧错误。 */ (_resolve, reject) => { rejectOld = reject; }))
    .mockReturnValueOnce(new Promise(/* resolve 释放新测试的成功结果。 */ resolve => { resolveNew = resolve; }));
  // hook 管理连续渠道测试的生产状态。
  const hook = renderHook(/* 创建通知状态。 */ () => useNotifications(false));
  await waitFor(/* 等待列表初始化。 */ () => expect(hook.result.current.loading).toBe(false));
  // oldTask 保存被替换的测试。
  let oldTask!: Promise<void>;
  // newTask 保存当前测试。
  let newTask!: Promise<void>;
  act(/* 连续提交两个不同渠道的测试。 */ () => {
    oldTask = hook.result.current.handleTest(channel);
    newTask = hook.result.current.handleTest({ ...channel, id: 'two' });
  });
  await act(/* 旧错误不应替代新状态。 */ async () => { rejectOld(new Error('旧请求错误')); await oldTask; });
  expect(hook.result.current.testingId).toBe('two');
  expect(hook.result.current.toast).toBeNull();
  await act(/* 当前测试拥有收束忙碌状态的责任。 */ async () => { resolveNew({ success: true }); await newTask; });
  expect(hook.result.current.testingId).toBe('');
  expect(hook.result.current.toast?.type).toBe('success');
  hook.unmount();
});
