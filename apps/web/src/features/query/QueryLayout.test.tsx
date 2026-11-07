import { forwardRef, useImperativeHandle } from 'react';
import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { ApiClient } from '../../lib/api';
import type { QueryContextModel } from '../../lib/types';
import { OntologySQLWorkspace } from './OntologySQLWorkspace';

vi.mock('./OntologySQLEditor', () => ({ default: forwardRef(function TestEditor({ text, onChange }: { text: string; onChange: (text: string) => void }, ref) {
  useImperativeHandle(ref, () => ({ focus: vi.fn(), locate: vi.fn(), complete: vi.fn(), insert: (value: string) => onChange(text + value) }));
  return <textarea aria-label="OntologySQL 编辑器" value={text} onChange={(event) => onChange(event.target.value)} />;
}) }));

const models: QueryContextModel[] = [
  { schemaId: 'orders', categoryId: 'business', category: '业务', name: '订单', fields: [{ id: 'amount', name: '金额', dataType: 'decimal', precision: 24, scale: 6, nullable: true, filterable: true, sortable: true }] },
  { schemaId: 'suppliers', categoryId: 'business', category: '业务', name: '供应商', fields: [{ id: 'code', name: '供应商编码', dataType: 'text', nullable: false, filterable: true, sortable: true }] },
];

function mount() {
  const api = {
    queryContext: vi.fn().mockResolvedValue({ models }),
    analyzeOntologySQL: vi.fn().mockResolvedValue({ requestId: '', textHash: '', diagnostics: [], parameters: [], fields: [], ready: false }),
    queryOntologySQL: vi.fn(), query: vi.fn(),
  } as unknown as ApiClient;
  render(<OntologySQLWorkspace api={api} categoryId="business" schemas={[]} categories={[]} onModel={vi.fn()} initialDraft={{ text: '', drafts: {} }} onDraft={vi.fn()} />);
  return api;
}

describe('查询工作台布局', () => {
  it('指导显示在编辑区上方，模型目录与选中模型的字段各自独立', async () => {
    mount();
    await screen.findByRole('button', { name: /订单.*1.*属性/ });
    const guide = screen.getByRole('region', { name: '查询指导' });
    const directory = screen.getByRole('complementary', { name: '模型目录' });
    const center = screen.getByRole('region', { name: 'OntologySQL 查询' });
    const fields = screen.getByRole('complementary', { name: '模型字段' });
    expect(guide.compareDocumentPosition(directory) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(directory.compareDocumentPosition(center) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(center.compareDocumentPosition(fields) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(within(directory).queryByRole('button', { name: '插入字段 金额' })).toBeNull();
    expect(within(fields).getByRole('button', { name: '插入字段 金额' })).toBeTruthy();
    expect(within(guide).getByText('语法速查').closest('details')?.open).toBe(false);
  });

  it('三个辅助面板独立折叠与恢复，保留查询文本和当前模型', async () => {
    const api = mount();
    await screen.findByRole('button', { name: /供应商.*1.*属性/ });
    fireEvent.click(screen.getByRole('button', { name: /供应商.*1.*属性/ }));
    const editor = await screen.findByRole('textbox', { name: 'OntologySQL 编辑器' });
    const text = 'SELECT "金额" FROM "业务"."订单" LIMIT 25;';
    fireEvent.change(editor, { target: { value: text } });
    for (const name of ['查询指导', '模型目录', '模型字段']) fireEvent.click(screen.getByRole('button', { name }));
    expect(screen.queryByRole('region', { name: '查询指导' })).toBeNull();
    expect(screen.queryByRole('complementary', { name: '模型目录' })).toBeNull();
    expect(screen.queryByRole('complementary', { name: '模型字段' })).toBeNull();
    expect((editor as HTMLTextAreaElement).value).toBe(text);
    for (const name of ['查询指导', '模型目录', '模型字段']) fireEvent.click(screen.getByRole('button', { name }));
    expect(screen.getByRole('region', { name: '查询指导' })).toBeTruthy();
    expect(screen.getByRole('complementary', { name: '模型目录' })).toBeTruthy();
    expect(within(screen.getByRole('complementary', { name: '模型字段' })).getByRole('button', { name: '插入字段 供应商编码' })).toBeTruthy();
    expect((editor as HTMLTextAreaElement).value).toBe(text);
    expect(api.queryOntologySQL).not.toHaveBeenCalled();
    expect(api.query).not.toHaveBeenCalled();
  });

  it('单击模型只切换右侧字段，单击字段也不会改写或执行当前 SQL', async () => {
    const api = mount();
    await screen.findByRole('button', { name: /供应商.*1.*属性/ });
    const editor = await screen.findByRole('textbox', { name: 'OntologySQL 编辑器' });
    const text = 'SELECT "金额" FROM "业务"."订单" LIMIT 25;';
    fireEvent.change(editor, { target: { value: text } });
    fireEvent.click(screen.getByRole('button', { name: /供应商.*1.*属性/ }));
    const fields = screen.getByRole('complementary', { name: '模型字段' });
    expect(within(fields).queryByRole('button', { name: '插入字段 金额' })).toBeNull();
    fireEvent.click(within(fields).getByRole('button', { name: '插入字段 供应商编码' }));
    expect((editor as HTMLTextAreaElement).value).toBe(text);
    expect(api.queryOntologySQL).not.toHaveBeenCalled();
    expect(api.query).not.toHaveBeenCalled();
  });
});
