import { beforeEach, describe, expect, test, vi } from 'vitest';

// postMock 是版本化商品发布请求的共享契约客户端替身。
const { postMock } = vi.hoisted(/* postMockFactory 创建契约客户端 POST 方法替身。 */ () => ({ postMock: vi.fn() }));

// ContractMockResult 是测试契约请求回调返回的最小响应模型。
interface ContractMockResult {
  // data 是契约客户端解码出的成功响应。
  data?: unknown;
}

// PublishCallOptions 是测试中读取 multipart 请求载荷的最小参数模型。
interface PublishCallOptions {
  // body 是契约客户端收到的原生 FormData。
  body?: unknown;
}

vi.mock('../../../shared/api-contract/client', /* contractClientModuleMockFactory 提供商品 API 适配测试所需的契约依赖。 */ () => ({
  // contractClientMock 只提供本测试覆盖的 POST 操作。
  contractClient: { POST: postMock },
  // contractMultipartBodyMock 保留测试中的原生 FormData 对象。
  contractMultipartBody: <T>(form: FormData) => form as unknown as T,
  // runContractRequestMock 执行真实 feature adapter 传入的请求回调。
  runContractRequest: /* runContractRequestMock 执行 feature adapter 传入的契约请求回调。 */ async (/* execute 是 feature adapter 构造的契约请求动作。 */ execute: (signal: AbortSignal) => Promise<ContractMockResult>) => (await execute(new AbortController().signal)).data,
}));

import { contractClient } from '../../../shared/api-contract/client';
import { publishItem } from './api';

// publishPostMock 是带有生成契约类型的 POST 方法替身。
const publishPostMock = vi.mocked(contractClient.POST);

describe('items publish API adapter', /* 商品发布 API 适配测试覆盖 multipart 类目字段映射。 */ () => {
  beforeEach(/* 当前回调重置商品发布契约请求替身。 */ () => {
    vi.clearAllMocks();
    publishPostMock.mockResolvedValue({ data: { success: true }, response: { status: 200 } } as never);
  });

  test('提交选中类目并保留电子资料空淘宝类目', /* 当前回调验证类目字段不会丢失或伪造淘宝 ID。 */ async () => {
    // image 是发布请求使用的最小图片文件。
    const image = new File(['image'], 'cover.jpg', { type: 'image/jpeg' });
    await publishItem({
      cookie_id: 'account-1', title: '资料', description: '描述', price: '10', quantity: '1',
      postage_mode: 'free', images: [image],
      category: { cat_id: '50023914', cat_name: '电子资料', channel_cat_id: '202036301' },
    });
    // body 是适配器传给契约客户端的 multipart 表单。
    const body = (publishPostMock.mock.calls[0][1] as PublishCallOptions).body as FormData;
    expect(body.get('category_id')).toBe('50023914');
    expect(body.get('category_name')).toBe('电子资料');
    expect(body.get('channel_category_id')).toBe('202036301');
    expect(body.get('tb_category_id')).toBe('');
    expect(body.get('images')).toBe(image);
  });

  test('未选择类目时不发送空覆盖字段，交给后端自动识别和兜底', /* 当前回调验证旧调用仍保留原有类目策略。 */ async () => {
    await publishItem({
      cookie_id: 'account-1', title: '资料', description: '描述', price: '10', quantity: '1',
      postage_mode: 'free', images: [new File(['image'], 'cover.jpg', { type: 'image/jpeg' })],
    });
    // body 是未选择类目时的 multipart 表单。
    const body = (publishPostMock.mock.calls[0][1] as PublishCallOptions).body as FormData;
    expect(body.has('category_id')).toBe(false);
    expect(body.has('category_name')).toBe(false);
    expect(body.has('channel_category_id')).toBe(false);
    expect(body.has('tb_category_id')).toBe(false);
  });
});
