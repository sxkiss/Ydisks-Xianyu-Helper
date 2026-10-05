// @vitest-environment jsdom
import { cleanup,fireEvent,render,screen,waitFor } from '@testing-library/react';
import { afterEach,describe,expect,test,vi } from 'vitest';
import { DeleteConversationDialog } from './DeleteConversationDialog';

afterEach(/* 当前回调隔离每个确认框场景创建的 DOM。 */ () => cleanup());

describe('DeleteConversationDialog', /* 当前测试组验证不可撤销会话清空前的无障碍确认交互。 */ () => {
  test('默认聚焦取消并支持确认、Escape 与焦点恢复', /* 当前测试验证安全默认焦点和完整关闭路径。 */ async () => {
    // origin 是打开确认框前持有焦点的会话删除按钮。
    const origin = document.createElement('button');
    document.body.append(origin);
    origin.focus();
    // onCancel 记录用户取消删除的次数。
    const onCancel = vi.fn();
    // onConfirm 记录用户确认删除的次数。
    const onConfirm = vi.fn();
    // view 保存确认框渲染句柄，用于验证卸载后的焦点恢复。
    const view = render(<DeleteConversationDialog buyerName="测试买家" deleting={false} error="" onCancel={onCancel} onConfirm={onConfirm} />);
    expect(screen.getByText(/订单、自动化和 AI 记忆不会删除/)).toBeTruthy();
    await waitFor(
      // defaultFocusAssertion 等待动画帧把焦点移到安全的取消按钮。
      () => expect(document.activeElement).toBe(screen.getByRole('button', { name: '取消' })),
    );
    fireEvent.click(screen.getByRole('button', { name: '确认删除' }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(onCancel).toHaveBeenCalledTimes(1);
    view.unmount();
    expect(document.activeElement).toBe(origin);
    origin.remove();
  });

  test('请求期间锁定关闭和重复提交并展示失败信息', /* 当前测试验证删除中的模态锁和可重试错误呈现。 */ () => {
    // onCancel 记录锁定期间不应发生的关闭动作。
    const onCancel = vi.fn();
    // onConfirm 记录禁用按钮不应重复触发的确认动作。
    const onConfirm = vi.fn();
    render(<DeleteConversationDialog buyerName="测试买家" deleting error="删除失败，请重试" onCancel={onCancel} onConfirm={onConfirm} />);
    // dialog 是用于定位遮罩容器的确认框主体。
    const dialog = screen.getByRole('dialog');
    expect(screen.getByRole('alert').textContent).toContain('删除失败，请重试');
    expect((screen.getByRole('button', { name: '取消' }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole('button', { name: '删除中' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.keyDown(document, { key: 'Escape' });
    fireEvent.mouseDown(dialog.parentElement!);
    fireEvent.click(screen.getByRole('button', { name: '删除中' }));
    expect(onCancel).not.toHaveBeenCalled();
    expect(onConfirm).not.toHaveBeenCalled();
  });
});
