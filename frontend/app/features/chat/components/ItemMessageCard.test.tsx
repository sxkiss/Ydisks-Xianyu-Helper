// @vitest-environment jsdom
import { cleanup,render,screen } from '@testing-library/react';
import { afterEach,describe,expect,test } from 'vitest';
import { ItemMessageCard,parseItemMessageContent } from './ItemMessageCard';

afterEach(/* cleanupRenderedCards 在每个场景后移除已渲染商品卡片。 */ () => cleanup());

describe('ItemMessageCard', /* 当前测试组验证商品消息的安全解析、展示和降级。 */ () => {
  test('只按商品标识构造官方详情地址并展示规范快照', /* 当前回调验证合法商品消息不会采用正文中的任意跳转地址。 */ () => {
    // content 是包含额外恶意跳转字段的规范商品快照。
    const content = JSON.stringify({ item_id: 'item/1', title: '测试宝贝', image_url: 'https://img.example/item.png', price: '19.90', url: 'javascript:alert(1)' });
    render(<ItemMessageCard content={content} outgoing />);
    // link 是商品消息生成的官方详情链接。
    const link = screen.getByRole('link', { name: '查看商品：测试宝贝' });
    expect(link.getAttribute('href')).toBe('https://www.goofish.com/item?id=item%2F1');
    expect(screen.getByText('¥19.90')).toBeTruthy();
  });

  test('畸形 JSON 或非 HTTP 图片安全降级为占位文本', /* 当前回调验证破损卡片不会渲染不可信资源。 */ () => {
    expect(parseItemMessageContent('{')).toBeNull();
    expect(parseItemMessageContent(JSON.stringify({ item_id: '1', title: '危险商品', image_url: 'javascript:alert(1)', price: '1' }))).toBeNull();
    render(<ItemMessageCard content="{" outgoing={false} />);
    expect(screen.getByText('商品卡片暂时无法显示')).toBeTruthy();
    expect(screen.queryByRole('link')).toBeNull();
  });
});
