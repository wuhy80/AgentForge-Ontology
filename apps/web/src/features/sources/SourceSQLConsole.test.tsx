import { useState } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { EditorView } from '@codemirror/view'
import { describe, expect, it, vi } from 'vitest'
import { ApiError, type ApiClient } from '../../lib/api'
import type { DataSource, SourceSQLToolResult, SourceTableInfo } from '../../lib/types'
import { buildSourceSQL } from '../../lib/source-sql'
import { SourceSQLConsole, type SourceSQLConsoleProps } from './SourceSQLConsole'

const source: DataSource = { id: 'db', tenantId: 'tenant', name: '订单库', driver: 'mysql', database: 'business', etag: 'source-etag', enabled: true, tenantIsolation: 'dedicated' }
const table: SourceTableInfo = { namespace: 'business', name: 'orders', type: 'table' }
const success: SourceSQLToolResult = { sql: 'SELECT amount FROM orders LIMIT 10;', valid: true, dialect: 'mysql', message: '语法校验通过' }
function fixture(sql = 'select amount from orders limit 10;') {
  const api = { analyzeSourceSQL: vi.fn().mockResolvedValue(success), formatSourceSQL: vi.fn().mockResolvedValue({ ...success, message: 'SQL 已格式化' }) }
  const props: SourceSQLConsoleProps = { api: api as unknown as ApiClient, source, table, serverVersion: '26.7.0', sql, onSQLChange: vi.fn(), onRun: vi.fn(), onLimitChange: vi.fn(), onError: vi.fn(), busy: false, disabled: false }
  return { api, props }
}
function editor() { return EditorView.findFromDOM(screen.getByRole('textbox', { name: 'SQL 语句' }))! }
function edit(text: string) { const view = editor(); act(() => view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } })) }

describe('独立数据源 SQL 控制台', () => {
  it('初始空白仅开放只读查询，展示方言和当前源，空状态不伪造查询结果', () => {
    const { props } = fixture('')
    render(<SourceSQLConsole {...props} />)
    expect(editor().state.doc.toString()).toBe('')
    expect(screen.getByText(/此控制台仅支持只读查询/)).toBeTruthy()
    expect(screen.getByText('MySQL')).toBeTruthy(); expect(screen.getByText('26.7.0')).toBeTruthy()
    expect(screen.getByText(/当前数据源：订单库/)).toBeTruthy()
    expect(screen.getByText('编写 SQL 并执行查询，结果将在这里展示')).toBeTruthy()
    expect((screen.getByRole('button', { name: '执行 SQL' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByLabelText('查询行数上限') as HTMLInputElement).value).toBe('100')
  })

  it('限制1..100整数，每次上限变更通知父级，仅有效值执行并支持快捷键', () => {
    const { props } = fixture()
    render(<SourceSQLConsole {...props} />)
    for (const value of ['', '0', '101', '2.5']) {
      fireEvent.change(screen.getByLabelText('查询行数上限'), { target: { value } })
      expect(screen.getByRole('alert').textContent).toContain('1–100')
      expect((screen.getByRole('button', { name: '执行 SQL' }) as HTMLButtonElement).disabled).toBe(true)
    }
    expect(props.onLimitChange).toHaveBeenCalledTimes(4)
    fireEvent.change(screen.getByLabelText('查询行数上限'), { target: { value: '7' } })
    fireEvent.click(screen.getByRole('button', { name: '执行 SQL' }))
    expect(props.onRun).toHaveBeenLastCalledWith(7)
    fireEvent.keyDown(editor().contentDOM, { key: 'Enter', code: 'Enter', ctrlKey: true })
    expect(props.onRun).toHaveBeenCalledTimes(2)
  })

  it('真实菜单支持Esc恢复焦点、方向键、选择模板和清空，不自动执行查询', async () => {
    const { props } = fixture('')
    function Controlled() {
      const [sql, setSQL] = useState('')
      return <SourceSQLConsole {...props} sql={sql} onSQLChange={(text) => { setSQL(text); props.onSQLChange(text) }} />
    }
    render(<Controlled />)
    const trigger = screen.getByRole('button', { name: '填入查询' })
    fireEvent.keyDown(trigger, { key: 'ArrowDown' })
    expect(screen.getAllByRole('menuitem').map((item) => item.textContent)).toEqual(['当前表查询', '表占用排行 (数据+索引)', '表行数排行 (估算)'])
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: '当前表查询' }))
    fireEvent.keyDown(document.activeElement!, { key: 'Escape' })
    expect(screen.queryByRole('menu')).toBeNull(); expect(document.activeElement).toBe(trigger)
    fireEvent.click(trigger)
    fireEvent.click(screen.getByRole('menuitem', { name: '当前表查询' }))
    expect(editor().state.doc.toString()).toBe(buildSourceSQL('mysql', 'current_table', table))
    fireEvent.click(trigger)
    fireEvent.keyDown(document.activeElement!, { key: 'End' })
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: '表行数排行 (估算)' }))
    fireEvent.click(document.activeElement!)
    expect(editor().state.doc.toString()).toBe(buildSourceSQL('mysql', 'table_rows'))
    fireEvent.click(screen.getByRole('button', { name: '清空' }))
    expect(editor().state.doc.toString()).toBe('')
    expect(props.onRun).not.toHaveBeenCalled()
  })

  it('未选表仍可填入排行模板，当前表菜单项禁用', () => {
    const { props } = fixture('')
    render(<SourceSQLConsole {...props} table={undefined} source={{ ...source, driver: 'postgresql' }} />)
    fireEvent.click(screen.getByRole('button', { name: '填入查询' }))
    expect((screen.getByRole('menuitem', { name: '当前表查询' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByRole('menuitem', { name: '表占用排行 (数据+索引)' }))
    expect(props.onSQLChange).toHaveBeenLastCalledWith(buildSourceSQL('postgresql', 'table_storage'))
  })

  it('格式化和校验使用源etag及上限，仅显示工具状态，不提交查询', async () => {
    const { props, api } = fixture()
    function Controlled() {
      const [sql, setSQL] = useState(props.sql)
      return <SourceSQLConsole {...props} sql={sql} onSQLChange={(text) => { setSQL(text); props.onSQLChange(text) }} />
    }
    render(<Controlled />)
    fireEvent.change(screen.getByLabelText('查询行数上限'), { target: { value: '25' } })
    fireEvent.click(screen.getByRole('button', { name: '一键格式化' }))
    await screen.findByText('SQL 已格式化')
    expect(api.formatSourceSQL).toHaveBeenCalledWith('db', props.sql, 'source-etag', expect.any(AbortSignal), 25)
    expect(editor().state.doc.toString()).toBe(success.sql)
    fireEvent.click(screen.getByRole('button', { name: '校验语法' }))
    await screen.findByText('语法校验通过')
    expect(api.analyzeSourceSQL).toHaveBeenCalledWith('db', success.sql, 'source-etag', expect.any(AbortSignal), 25)
    expect(props.onRun).not.toHaveBeenCalled()
  })

  it('语法未通过保留原SQL，重试前可继续编辑且清除旧工具状态', async () => {
    const { props, api } = fixture('SELECT broken')
    api.formatSourceSQL.mockResolvedValue({ ...success, valid: false, sql: 'must not overwrite', message: 'SQL 未通过当前只读协议' })
    function Controlled() { const [sql, setSQL] = useState(props.sql); return <SourceSQLConsole {...props} sql={sql} onSQLChange={setSQL} /> }
    render(<Controlled />)
    fireEvent.click(screen.getByRole('button', { name: '一键格式化' }))
    expect((await screen.findByRole('alert')).textContent).toContain('SQL 未通过当前只读协议')
    expect(editor().state.doc.toString()).toBe('SELECT broken')
    edit('SELECT amount FROM orders LIMIT 10;')
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it.each(['sql', 'limit', 'source', 'table', 'unmount'] as const)('%s 变化取消工具请求并拒绝迟到格式化结果', async (change) => {
    const { props, api } = fixture()
    let resolve!: (value: SourceSQLToolResult) => void
    api.formatSourceSQL.mockImplementationOnce(() => new Promise((done) => { resolve = done }))
    const rendered = render(<SourceSQLConsole {...props} />)
    fireEvent.click(screen.getByRole('button', { name: '一键格式化' }))
    const signal = api.formatSourceSQL.mock.calls[0][3] as AbortSignal
    if (change === 'sql') rendered.rerender(<SourceSQLConsole {...props} sql="SELECT new_value FROM orders LIMIT 1;" />)
    else if (change === 'limit') fireEvent.change(screen.getByLabelText('查询行数上限'), { target: { value: '9' } })
    else if (change === 'source') rendered.rerender(<SourceSQLConsole {...props} source={{ ...source, etag: 'new-etag' }} />)
    else if (change === 'table') rendered.rerender(<SourceSQLConsole {...props} table={{ ...table, name: 'new_table' }} />)
    else rendered.unmount()
    expect(signal.aborted).toBe(true)
    await act(async () => resolve(success))
    expect(props.onSQLChange).not.toHaveBeenCalled()
    expect(screen.queryByText('语法校验通过')).toBeNull()
  })

  it.each([401, 403, 412])('工具 %i 交父级清理且不修改当前SQL', async (status) => {
    const { props, api } = fixture()
    const error = new ApiError(status, 'forbidden', '源配置或权限已变化')
    api.analyzeSourceSQL.mockRejectedValueOnce(error)
    render(<SourceSQLConsole {...props} />)
    fireEvent.click(screen.getByRole('button', { name: '校验语法' }))
    await waitFor(() => expect(props.onError).toHaveBeenCalledExactlyOnceWith(error))
    expect(props.onSQLChange).not.toHaveBeenCalled()
  })

  it('普通工具失败可在原文重试，运行中空结果区域显示加载状态', async () => {
    const { props, api } = fixture()
    api.analyzeSourceSQL.mockRejectedValueOnce(new ApiError(503, 'unavailable', '语法工具暂不可用'))
    const rendered = render(<SourceSQLConsole {...props} />)
    fireEvent.click(screen.getByRole('button', { name: '校验语法' }))
    expect((await screen.findByRole('alert')).textContent).toContain('语法工具暂不可用')
    expect(props.onError).not.toHaveBeenCalled()
    expect(editor().state.doc.toString()).toBe(props.sql)
    fireEvent.click(screen.getByRole('button', { name: '校验语法' }))
    await screen.findByText('语法校验通过')
    rendered.rerender(<SourceSQLConsole {...props} busy />)
    expect(screen.getByText('正在执行只读查询…')).toBeTruthy()
    expect(screen.queryByText('编写 SQL 并执行查询，结果将在这里展示')).toBeNull()
  })

  it('中文组词禁止工具与执行，disabled不编辑，已有查询结果通过children显示', async () => {
    const { props, api } = fixture()
    const rendered = render(<SourceSQLConsole {...props}><section aria-label="查询结果">真实结果</section></SourceSQLConsole>)
    fireEvent.compositionStart(editor().contentDOM)
    fireEvent.click(screen.getByRole('button', { name: '一键格式化' }))
    fireEvent.click(screen.getByRole('button', { name: '执行 SQL' }))
    expect(api.formatSourceSQL).not.toHaveBeenCalled(); expect(props.onRun).not.toHaveBeenCalled()
    fireEvent.compositionEnd(editor().contentDOM)
    await act(async () => { await Promise.resolve() })
    rendered.rerender(<SourceSQLConsole {...props} disabled><section aria-label="查询结果">真实结果</section></SourceSQLConsole>)
    expect(editor().state.readOnly).toBe(true)
    expect(screen.getByRole('region', { name: '查询结果' }).textContent).toBe('真实结果')
    expect(screen.queryByText('编写 SQL 并执行查询，结果将在这里展示')).toBeNull()
  })
})
