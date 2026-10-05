// @vitest-environment jsdom
import { cleanup,fireEvent,render,screen } from '@testing-library/react';
import { afterEach,describe,expect,test,vi } from 'vitest';
import AllItemsConfirmation from './AllItemsConfirmation';

afterEach(/* 当前回调清理全部商品确认控件测试 DOM。 */ () => cleanup());

describe('AllItemsConfirmation', /* 当前回调验证账号级付款发货规则的明确确认交互。 */ () => {
  test('展示当前状态并回传用户的新选择', /* 当前回调验证确认框受控状态和变更结果。 */ () => {
    // onChange 是记录用户确认结果的回调替身。
    const onChange = vi.fn();
    render(<AllItemsConfirmation confirmed={false} onChange={onChange} />);

    // checkbox 是账号级规则的全部商品确认框。
    const checkbox = screen.getByRole('checkbox');
    expect((checkbox as HTMLInputElement).checked).toBe(false);
    fireEvent.click(checkbox);
    expect(onChange).toHaveBeenCalledWith(true);
  });
});
