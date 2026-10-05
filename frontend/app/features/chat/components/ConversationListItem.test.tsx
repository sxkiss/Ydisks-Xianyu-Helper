// @vitest-environment jsdom
import { cleanup,fireEvent,render,screen } from '@testing-library/react';
import { afterEach,describe,expect,test,vi } from 'vitest';
import type { ChatSession } from '../models';
import { ConversationListItem } from './ConversationListItem';

afterEach(/* 当前回调隔离每条会话组件测试创建的 DOM。 */ () => cleanup());

describe('ConversationListItem', /* 当前测试组验证会话选择与悬停删除保持独立。 */ () => {
  test('选择按钮和红色删除按钮分别触发自己的操作', /* 当前测试验证删除操作不会冒泡切换会话。 */ () => {
    // session 是同时包含时间、商品图和未读数的会话行夹具。
    const session: ChatSession = { account_id: 'account-1', chat_id: 'chat-1', peer_user_id: 'buyer-1', peer_name: '测试买家', item_title: '测试商品', item_image_url: 'https://img.example/item.jpg', last_message: '你好', last_message_at: 1, unread_count: 2 };
    // onSelect 记录会话选择动作。
    const onSelect = vi.fn();
    // onDelete 记录删除确认框打开动作。
    const onDelete = vi.fn();
    render(<ConversationListItem session={session} active={false} formatClock={/* 当前回调为测试提供稳定时间文本。 */ () => '15:04'} onSelect={onSelect} onDelete={onDelete} />);
    // deleteButton 是默认透明、悬停或焦点时显示的小型红色垃圾桶按钮。
    const deleteButton = screen.getByRole('button', { name: '删除与测试买家的会话' });
    expect(deleteButton.className).toContain('text-red-500');
    expect(deleteButton.className).toContain('group-hover:opacity-100');
    expect(screen.getByText('15:04').className).toContain('group-hover:-translate-x-6');
    fireEvent.click(deleteButton);
    expect(onDelete).toHaveBeenCalledTimes(1);
    expect(onSelect).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: '打开与测试买家的会话' }));
    expect(onSelect).toHaveBeenCalledTimes(1);
  });

	test('当前会话发送中禁用删除按钮', /* 当前测试验证前端不会在人工发送请求进行中启动删除。 */ () => {
		// session 是发送中删除门禁使用的会话摘要。
		const session: ChatSession = { account_id: 'account-1', chat_id: 'chat-1', peer_user_id: 'buyer-1', peer_name: '测试买家', item_title: '', item_image_url: '', last_message: '发送中', last_message_at: 1, unread_count: 0 };
		// onDelete 记录禁用按钮是否仍错误触发删除流程。
		const onDelete = vi.fn();
		render(<ConversationListItem session={session} active formatClock={/* 当前回调提供稳定时间文本。 */ () => '15:04'} onSelect={/* 当前回调保持选择操作为空。 */ () => {}} onDelete={onDelete} deleteDisabled />);
		// deleteButton 是发送期间被禁用的会话删除按钮。
		const deleteButton = screen.getByRole('button', { name: '删除与测试买家的会话' });
		expect((deleteButton as HTMLButtonElement).disabled).toBe(true);
		fireEvent.click(deleteButton);
		expect(onDelete).not.toHaveBeenCalled();
	});
});
