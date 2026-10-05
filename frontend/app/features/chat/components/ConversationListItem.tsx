import { Trash2,UserRound } from 'lucide-react';
import React from 'react';
import type { ChatSession } from '../models';
import { unreadBadgeClassName,unreadBadgeLabel } from '../state';

/** ConversationListItemProps 描述单条会话的展示数据和两个相互独立的操作。 */
export interface ConversationListItemProps {
  /** session 是当前账号下的一条可见会话摘要。 */
  session: ChatSession;
  /** active 表示该会话正在右侧聊天区显示。 */
  active: boolean;
  /** formatClock 把会话毫秒时间转换为列表短时间。 */
  formatClock: (timestamp: number) => string;
  /** onSelect 只切换当前聊天，不触发删除。 */
  onSelect: () => void;
	/** onDelete 只打开删除确认框，不切换当前聊天。 */
	onDelete: () => void;
	/** deleteDisabled 表示当前会话存在人工发送事务，暂时不能启动删除。 */
	deleteDisabled?: boolean;
}

/** ConversationListItem 将会话选择按钮与悬停删除按钮并列，避免嵌套交互元素。 */
export const ConversationListItem: React.FC<ConversationListItemProps> = ({ session, active, formatClock, onSelect, onDelete, deleteDisabled = false }) => (
  <div className={`group relative border-b border-slate-100 transition-colors ${active ? 'bg-white shadow-chat-active' : 'hover:bg-white/80 focus-within:bg-white/80'}`}>
    <button type="button" aria-label={`打开与${session.peer_name || `用户 ${session.peer_user_id}`}的会话`} aria-current={active ? 'true' : undefined} onClick={onSelect} className="flex w-full gap-3 p-3.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-sky-300">
      <div className="flex h-10 w-10 shrink-0 items-center justify-center overflow-hidden rounded-full bg-slate-200 text-slate-500">
        {session.peer_avatar_url ? <img src={session.peer_avatar_url} alt="" className="h-full w-full object-cover" /> : <UserRound className="h-5 w-5" />}
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-extrabold text-slate-900">{session.peer_name || `用户 ${session.peer_user_id}`}</span>
        </div>
        <div className="mt-1 flex items-center gap-2">
          <span className="truncate text-xs text-slate-500">{session.last_message || '暂无消息'}</span>
          {session.unread_count > 0 && <span aria-label={`未读消息 ${unreadBadgeLabel(session.unread_count)} 条`} className={`ml-auto ${unreadBadgeClassName(session.unread_count)}`}>{unreadBadgeLabel(session.unread_count)}</span>}
        </div>
        {session.item_title && <div className="mt-1.5 truncate text-[10px] font-medium text-sky-700">商品 · {session.item_title}</div>}
      </div>
      <div className="flex shrink-0 flex-col items-end gap-1.5">
        <span className="conversation-delete-time text-[10px] font-medium text-slate-400 transition-transform duration-150 group-hover:-translate-x-6 group-focus-within:-translate-x-6 max-sm:-translate-x-6">{formatClock(session.last_message_at)}</span>
        {session.item_image_url && <img src={session.item_image_url} alt="" className="h-9 w-11 rounded-[4px] border border-slate-200 object-cover" />}
      </div>
    </button>
	<button type="button" title={deleteDisabled ? "消息发送中，暂不能删除" : "删除会话"} aria-label={`删除与${session.peer_name || `用户 ${session.peer_user_id}`}的会话`} onClick={onDelete} disabled={deleteDisabled} className="conversation-delete-action pointer-events-none absolute right-3 top-3 flex h-5 w-5 items-center justify-center rounded-md border border-red-100 bg-white text-red-500 opacity-0 shadow-sm transition hover:border-red-200 hover:bg-red-50 hover:text-red-600 focus:pointer-events-auto focus:opacity-100 focus:outline-none focus:ring-2 focus:ring-red-100 disabled:cursor-not-allowed disabled:border-slate-100 disabled:text-slate-300 group-hover:pointer-events-auto group-hover:opacity-100 group-focus-within:pointer-events-auto group-focus-within:opacity-100 max-sm:pointer-events-auto max-sm:opacity-100">
      <Trash2 className="h-3 w-3" />
    </button>
  </div>
);

export default ConversationListItem;
