// @vitest-environment jsdom
import { cleanup,fireEvent,render,screen } from '@testing-library/react';
import { afterEach,describe,expect,test,vi } from 'vitest';
import { PublishImagesEditor } from './PublishImagesEditor';

// firstFile、secondFile 是商品图片编辑器测试使用的文件样本。
const firstFile = new File(['first'], 'first.jpg', { type: 'image/jpeg' });
const secondFile = new File(['second'], 'second.jpg', { type: 'image/jpeg' });
// previews 是与文件顺序对应的本地预览地址样本。
const previews = [{ key: 'first0', url: 'blob:first' }, { key: 'second1', url: 'blob:second' }];

describe('PublishImagesEditor', /* testGroup 验证商品图片追加、删除和排序交互。 */ () => {
  afterEach(/* cleanupAction 清理每个图片编辑器测试的 DOM。 */ () => cleanup());

  test('追加图片不会覆盖已有图片', /* testCase 验证继续添加行为保留原有顺序。 */ () => {
    // onChange 是发布草稿图片列表的状态更新替身。
    const onChange = vi.fn(/* onChangeAction 记录图片列表变化。 */ () => undefined);
    // view 是图片编辑器测试视图及其文件控件容器。
    const view = render(<PublishImagesEditor images={[firstFile]} previews={[previews[0]]} onChange={onChange} />);
    // input 是继续添加图片使用的原生文件控件。
    const input = view.container.querySelector<HTMLInputElement>('input[type="file"]');
    expect(input).not.toBeNull();
    fireEvent.change(input!, { target: { files: [secondFile] } });
    expect(onChange).toHaveBeenCalledWith([firstFile, secondFile]);
  });

  test('可以删除图片并用箭头调整顺序', /* testCase 验证主图顺序和删除操作。 */ () => {
    // onChange 是图片列表状态更新替身。
    const onChange = vi.fn(/* onChangeAction 记录图片列表变化。 */ () => undefined);
    render(<PublishImagesEditor images={[firstFile, secondFile]} previews={previews} onChange={onChange} />);
    fireEvent.click(screen.getByRole('button', { name: '将第 2 张图片前移' }));
    expect(onChange).toHaveBeenCalledWith([secondFile, firstFile]);
    fireEvent.click(screen.getByRole('button', { name: '删除第 1 张图片' }));
    expect(onChange).toHaveBeenLastCalledWith([secondFile]);
  });
});
