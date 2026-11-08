import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ApiClient, ApiError } from '../../lib/api';
import type { SavedQuery } from '../../lib/types';
import { SavedQueries } from './SavedQueries';

const record: SavedQuery = { id: 'saved', name: '订单筛选', etag: 'saved-etag', createdAt: '2026-11-08T00:00:00Z', updatedAt: '2026-11-08T00:00:00Z', source: { kind: 'ontology-sql', text: 'SELECT "旧名称" FROM "旧模型" WHERE "金额" >= :最低金额', defaultCategoryId: 'category' }, query: { from: { schemaId: 'stable-orders' }, select: ['amount'], where: { op: 'gte', field: 'amount', parameter: '最低金额' }, page: { limit: 25 } }, parameters: [{ name: '最低金额', dataType: 'decimal', nullable: false, precision: 24, scale: 6 }] };
function fixture() {
  const api = { dataCapabilities: vi.fn().mockResolvedValue({ savedQueries: true }), listSavedQueries: vi.fn().mockResolvedValue({ data: [record], hasMore: false }), getSavedQuery: vi.fn().mockResolvedValue(record), queryContext: vi.fn().mockResolvedValue({ models: [{ schemaId: 'stable-orders', fields: [{ id: 'amount', name: '最新金额', dataType: 'decimal', precision: 24, scale: 6 }] }] }), executeSavedQuery: vi.fn().mockResolvedValue({ data: [{ id: 'row', values: { amount: '9007199254740993.000001' } }], fields: [{ id: 'amount', name: '最新金额', dataType: 'decimal' }], hasMore: true, nextCursor: 'signed-cursor' }), createSavedQuery: vi.fn().mockResolvedValue(record), updateSavedQuery: vi.fn().mockImplementation(async (_id, input) => ({ ...record, ...input, etag: 'renamed' })), deleteSavedQuery: vi.fn().mockResolvedValue(undefined) };
  const capture = vi.fn().mockResolvedValue({ query: record.query, parameters: record.parameters, source: record.source });
  const onDenied = vi.fn();
  const view = render(<SavedQueries api={api as unknown as ApiClient} capture={capture} onDenied={onDenied}><p>原查询草稿</p></SavedQueries>);
  return { api, capture, onDenied, view };
}
async function load() { fireEvent.click(await screen.findByRole('button', { name: /我的查询/ })); fireEvent.click(await screen.findByRole('button', { name: '订单筛选' })); await screen.findByRole('button', { name: '运行保存查询' }); }

describe('保存查询目录与稳定模板运行', () => {
  it('旧后端没有声明保存查询能力时只显示原编写区', async () => {
    const api = { dataCapabilities: vi.fn().mockResolvedValue({}) } as unknown as ApiClient;
    render(<SavedQueries api={api} capture={vi.fn()} onDenied={vi.fn()}><p>原查询草稿</p></SavedQueries>);
    await waitFor(() => expect(api.dataCapabilities).toHaveBeenCalled());
    expect(screen.queryByRole('region', { name: '我的查询' })).toBeNull();
    expect(screen.getByText('原查询草稿')).toBeTruthy();
  });

  it('加载不执行，使用ID模板运行而不按旧SQL重绑；分页沿用执行实参且修改参数使续页失效', async () => {
    const { api } = fixture(); await load();
    expect(api.executeSavedQuery).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText('最低金额参数方式'), { target: { value: 'value' } });
    fireEvent.change(screen.getByLabelText(/最低金额decimal/), { target: { value: '9007199254740993.000001' } });
    fireEvent.click(screen.getByRole('button', { name: '运行保存查询' }));
    await screen.findByText('9007199254740993.000001');
    expect(api.executeSavedQuery.mock.calls[0].slice(0, 3)).toEqual(['saved', { 最低金额: '9007199254740993.000001' }, undefined]);
    fireEvent.click(screen.getByRole('button', { name: '保存查询下一页' }));
    await waitFor(() => expect(api.executeSavedQuery).toHaveBeenCalledTimes(2));
    expect(api.executeSavedQuery.mock.calls[1].slice(0, 3)).toEqual(['saved', { 最低金额: '9007199254740993.000001' }, 'signed-cursor']);
    fireEvent.change(screen.getByLabelText(/最低金额decimal/), { target: { value: '1.000000' } });
    expect(screen.getByRole('button', { name: '保存查询下一页' }).matches(':disabled')).toBe(true);
  });

  it('重命名保留查询身份且使用读取时ETag，失效标题可直接删除', async () => {
    const { api } = fixture();
    fireEvent.click(await screen.findByRole('button', { name: /我的查询/ }));
    fireEvent.click(await screen.findByRole('button', { name: '重命名查询 订单筛选' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByRole('textbox', { name: '查询名称' }), { target: { value: '改名查询' } });
    fireEvent.click(within(dialog).getByRole('button', { name: '确认保存' }));
    await waitFor(() => expect(api.updateSavedQuery).toHaveBeenCalledTimes(1));
    expect(api.updateSavedQuery.mock.calls[0].slice(0, 3)).toEqual(['saved', { name: '改名查询', query: record.query, parameters: record.parameters, source: record.source }, 'saved-etag']);
    await screen.findByRole('button', { name: '删除查询 改名查询' });
    api.getSavedQuery.mockRejectedValue(new ApiError(403, 'forbidden', '已撤权'));
    fireEvent.click(screen.getByRole('button', { name: '删除查询 改名查询' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: '确认删除' }));
    await waitFor(() => expect(api.deleteSavedQuery).toHaveBeenCalledTimes(1));
    expect(api.deleteSavedQuery.mock.calls[0].slice(0, 2)).toEqual(['saved', 'renamed']);
  });

  it.each([403, 404])('运行模板失效（%s）时清除模板、参数和结果', async (status) => {
    const { api, onDenied } = fixture(); await load();
    fireEvent.change(screen.getByLabelText('最低金额参数方式'), { target: { value: 'value' } });
    fireEvent.change(screen.getByLabelText(/最低金额decimal/), { target: { value: '1' } });
    fireEvent.click(screen.getByRole('button', { name: '运行保存查询' })); await screen.findByText('9007199254740993.000001');
    api.executeSavedQuery.mockRejectedValue(new ApiError(status, 'forbidden', '查询权限已撤销'));
    fireEvent.click(screen.getByRole('button', { name: '运行保存查询' }));
    await waitFor(() => expect(onDenied).toHaveBeenCalledWith('查询权限已撤销'));
    expect(screen.queryByRole('region', { name: '已保存查询' })).toBeNull();
    expect(screen.queryByText('9007199254740993.000001')).toBeNull();
    expect(screen.queryByLabelText('最低金额参数方式')).toBeNull();
  });

  it('创建响应丢失后不自动重试或重复创建，原草稿保留', async () => {
    const { api, capture } = fixture();
    api.createSavedQuery.mockRejectedValue(new ApiError(0, 'network_error', '网络中断'));
    fireEvent.click(await screen.findByRole('button', { name: '保存当前查询' }));
    fireEvent.change(screen.getByRole('textbox', { name: '查询名称' }), { target: { value: '待确认查询' } });
    fireEvent.click(screen.getByRole('button', { name: '确认保存' }));
    await screen.findByText('保存结果尚未确认，请先刷新「我的查询」核对，确认未保存后再创建。');
    expect(api.createSavedQuery).toHaveBeenCalledTimes(1);
    expect(capture).toHaveBeenCalledTimes(1);
    expect(screen.getByRole('button', { name: '保存当前查询' }).matches(':disabled')).toBe(true);
    fireEvent.click(screen.getByRole('button', { name: '刷新并核对' }));
    await waitFor(() => expect(api.listSavedQueries).toHaveBeenCalledTimes(1));
    expect(api.createSavedQuery).toHaveBeenCalledTimes(1);
    expect(screen.getByText('原查询草稿')).toBeTruthy();
  });

  it('创建提交后返回撤权时清除模板并要求先核对目录', async () => {
    const { api, onDenied } = fixture();
    api.createSavedQuery.mockRejectedValue(new ApiError(403, 'forbidden', '权限已改变'));
    fireEvent.click(await screen.findByRole('button', { name: '保存当前查询' }));
    fireEvent.change(screen.getByRole('textbox', { name: '查询名称' }), { target: { value: '待确认查询' } });
    fireEvent.click(screen.getByRole('button', { name: '确认保存' }));
    await waitFor(() => expect(onDenied).toHaveBeenCalledWith('权限已改变'));
    expect(screen.getByRole('button', { name: '保存当前查询' }).matches(':disabled')).toBe(true);
    expect(api.createSavedQuery).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('region', { name: '已保存查询' })).toBeNull();
  });

  it('取消加载或切换用户后忽略旧响应', async () => {
    const { api, view } = fixture();
    let resolve!: (value: SavedQuery) => void;
    api.getSavedQuery.mockImplementation(() => new Promise((yes) => { resolve = yes; }));
    fireEvent.click(await screen.findByRole('button', { name: /我的查询/ }));
    fireEvent.click(await screen.findByRole('button', { name: '订单筛选' }));
    await waitFor(() => expect(api.getSavedQuery).toHaveBeenCalled());
    const signal = api.getSavedQuery.mock.calls[0][1] as AbortSignal;
    view.unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => resolve(record));
    expect(screen.queryByRole('region', { name: '已保存查询' })).toBeNull();
  });
});
