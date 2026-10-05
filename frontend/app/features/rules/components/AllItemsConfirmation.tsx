// AllItemsConfirmationProps 描述账号级付款发货规则的适用范围确认状态和变更回调。
export interface AllItemsConfirmationProps {
  // confirmed 表示用户是否已明确确认当前发货内容适用于账号下全部商品。
  confirmed: boolean;
  // onChange 在用户切换确认框时回传新的明确确认状态。
  onChange: (confirmed: boolean) => void;
}

// AllItemsConfirmation 展示账号级付款发货规则的高风险范围确认控件。
const AllItemsConfirmation = ({ confirmed, onChange }: AllItemsConfirmationProps) => (
  <label className="block rounded-2xl border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900">
    <input
      type="checkbox"
      checked={confirmed}
      onChange={
        // event 是确认框变更事件，只把布尔选择结果交给规则编辑器。
        event => onChange(event.target.checked)
      }
      className="mr-2"
    />
    我确认此发货内容适用于该账号的全部商品
    <span className="mt-2 block text-xs">未勾选时不会作为商品规则缺失时的兜底。商品专属内容请选择具体商品；下架后重新同步的商品需重新配置发货规则。</span>
  </label>
);

export default AllItemsConfirmation;
