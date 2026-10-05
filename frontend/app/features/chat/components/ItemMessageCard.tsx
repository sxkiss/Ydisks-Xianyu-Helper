import { ExternalLink,PackageOpen } from 'lucide-react';
import type React from 'react';

/** ItemMessageContent 是消息正文中允许展示的规范商品快照。 */
export interface ItemMessageContent {
  /** item_id 是构造闲鱼官方详情页所需的稳定商品标识。 */
  item_id: string;
  /** title 是发送时保存的商品标题快照。 */
  title: string;
  /** image_url 是商品主图的 HTTP(S) 地址。 */
  image_url: string;
  /** price 是不含货币符号的价格文本。 */
  price: string;
}

/** ItemMessageCardProps 描述商品消息内容和方向。 */
export interface ItemMessageCardProps {
  /** content 是后端保存的规范 JSON 商品快照。 */
  content: string;
  /** outgoing 控制商品气泡朝向样式。 */
  outgoing: boolean;
}

/** parseItemMessageContent 安全解析商品正文，不信任正文中的任意详情跳转地址。 */
export const parseItemMessageContent = (content: string): ItemMessageContent | null => {
  try {
    // candidate 是从消息正文解析出的未知商品对象。
    const candidate = JSON.parse(content) as Record<string, unknown>;
    if (typeof candidate.item_id !== 'string' || !candidate.item_id.trim()) return null;
    if (typeof candidate.title !== 'string' || !candidate.title.trim()) return null;
    if (typeof candidate.image_url !== 'string' || !/^https?:\/\//i.test(candidate.image_url)) return null;
    if (typeof candidate.price !== 'string' || !candidate.price.trim()) return null;
    return { item_id: candidate.item_id, title: candidate.title, image_url: candidate.image_url, price: candidate.price };
  } catch (/* parseError 表示正文不是可展示的规范商品 JSON，界面将安全降级。 */ _parseError) {
    return null;
  }
};

/** ItemMessageCard 渲染可点击的商品消息，详情地址只由经过编码的商品标识生成。 */
export const ItemMessageCard: React.FC<ItemMessageCardProps> = ({ content, outgoing }) => {
  // item 是经过字段和图片协议校验的商品快照。
  const item = parseItemMessageContent(content);
  if (!item) {
    return <div className={`rounded-2xl border border-slate-200 bg-white px-4 py-3 text-sm text-slate-600 shadow-sm ${outgoing ? 'rounded-br-md' : 'rounded-bl-md'}`}><PackageOpen className="mr-2 inline h-4 w-4" />商品卡片暂时无法显示</div>;
  }
  // detailURL 只使用商品标识构造官方详情地址，忽略消息内容中的潜在跳转字段。
  const detailURL = `https://www.goofish.com/item?id=${encodeURIComponent(item.item_id)}`;
  return (
    <a href={detailURL} target="_blank" rel="noreferrer" className={`group block w-[286px] max-w-full overflow-hidden rounded-2xl border border-slate-200 bg-white text-left shadow-sm transition hover:-translate-y-0.5 hover:shadow-md ${outgoing ? 'rounded-br-md' : 'rounded-bl-md'}`} aria-label={`查看商品：${item.title}`}>
      <div className="flex gap-3 p-3">
        <img src={item.image_url} alt="" className="h-20 w-20 shrink-0 rounded-xl bg-slate-100 object-cover" />
        <div className="min-w-0 flex-1 py-0.5">
          <div className="line-clamp-2 text-sm font-bold leading-5 text-slate-900">{item.title}</div>
          <div className="mt-2 flex items-end justify-between gap-2">
            <span className="text-base font-black text-amber-500">¥{item.price}</span>
            <ExternalLink className="h-3.5 w-3.5 text-slate-300 transition group-hover:text-slate-600" />
          </div>
        </div>
      </div>
      <div className="border-t border-slate-100 bg-slate-50/80 px-3 py-1.5 text-[10px] font-semibold text-slate-400">闲鱼商品 · 点击查看详情</div>
    </a>
  );
};

export default ItemMessageCard;
