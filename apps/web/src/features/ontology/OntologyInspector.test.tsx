import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, within } from '@testing-library/react'
import type { DefinitionRecord } from '../../lib/types'
import { OntologyInspector } from './OntologyInspector'

const base = { tenantId: 'tenant', categoryId: 'knowledge', groupId: 'models' }
const categories = [{ id: 'knowledge', name: '个人知识库', tenantId: 'tenant' }, { id: 'shared', name: '公共字典', tenantId: 'tenant' }]
const groups = [{ id: 'models', categoryId: 'knowledge', name: '知识建模', tenantId: 'tenant' }]
const note: DefinitionRecord = { status: 'active', etag: 'note', definition: { ...base, id: 'note', kind: 'schema', name: '笔记', description: '保存知识条目', schema: { mode: 'db_2_schema', storageLayout: 'single', storageBinding: { dataSourceId: 'secret-source-id', namespace: 'public', table: 'notes', idColumn: 'id' }, properties: [{ id: 'status', name: '状态', dataType: 'string', enumId: 'status-enum', mapping: { dataSourceId: 'secret-source-id', column: 'status_code' } }] } } }
const tag: DefinitionRecord = { ...note, etag: 'tag', definition: { ...base, id: 'tag', kind: 'schema', name: '标签', schema: { mode: 'schema_2_db', storageLayout: 'single', properties: [] } } }
const enumeration: DefinitionRecord = { status: 'active', etag: 'enum', definition: { ...base, categoryId: 'shared', id: 'status-enum', kind: 'enum', name: '笔记状态', enum: { values: [{ code: '001', label: '草稿' }, { code: '9007199254740993', label: '历史状态', deprecated: true }] } } }
const link: DefinitionRecord = { status: 'active', etag: 'link', definition: { ...base, id: 'note-tag', name: '笔记标记标签', kind: 'link', link: { sourceSchemaId: 'note', targetSchemaId: 'tag', forwardName: '标记标签', reverseName: '用于笔记', cardinality: 'N:M', onDelete: 'restrict', storageBinding: { mode: 'foreign_key', foreignKeySide: 'source', foreignKeyColumn: 'tag_id' } } } }
const fn: DefinitionRecord = { status: 'active', etag: 'function', definition: { ...base, id: 'summarize', kind: 'function', name: '提取摘要', function: { input: [{ id: 'text', name: '原文', dataType: 'longtext' }], output: [{ id: 'summary', name: '摘要', dataType: 'string' }], sideEffects: 'none', runtime: { language: 'python', entrypoint: 'summarize', timeoutSeconds: 8, source: 'TOP_SECRET_IMPLEMENTATION' } } } }
const action: DefinitionRecord = { status: 'active', etag: 'action', definition: { ...base, id: 'summarize-note', name: '生成笔记摘要', kind: 'action', action: { targetSchemaIds: ['note'], functionId: 'summarize', input: [{ id: 'text', name: '原文', dataType: 'longtext' }], output: [{ id: 'summary', name: '摘要', dataType: 'string' }], sideEffects: 'none' } } }
const records = [note, tag, enumeration, link, fn, action]
function props(record: DefinitionRecord | undefined = note, entries = records) {
  return { record, records: entries, categories, groups, categoryId: 'knowledge', onSelect: vi.fn(), onOpenDefinition: vi.fn(), onQuery: vi.fn() }
}

describe('本体全景只读详情', () => {
  it('对象属性、枚举引用、当前分类和来源概要联动，跳转只调用指定回调', () => {
    const options = props()
    render(<OntologyInspector {...options} />)
    const details = screen.getByRole('article', { name: '本体资源详情' })
    expect(within(details).getByRole('heading', { name: '笔记' })).toBeTruthy()
    expect(screen.getByText('知识建模')).toBeTruthy()
    expect(screen.getByText('public.notes')).toBeTruthy()
    expect(screen.getByText('状态')).toBeTruthy()
    expect(screen.getByText('status_code')).toBeTruthy()
    expect(screen.getByText('标记标签')).toBeTruthy()
    fireEvent.click(screen.getAllByRole('button', { name: '查看本体资源：笔记状态' })[0])
    expect(options.onSelect).toHaveBeenCalledWith('status-enum')
    fireEvent.click(screen.getByRole('button', { name: '查看定义' }))
    fireEvent.click(screen.getByRole('button', { name: '查看数据' }))
    expect(options.onOpenDefinition).toHaveBeenCalledWith('note')
    expect(options.onQuery).toHaveBeenCalledWith('note')
    expect(details.textContent).not.toContain('secret-source-id')
  })

  it('Link展示两端、中文正反名称和实际映射只读边界', () => {
    const options = props(link)
    render(<OntologyInspector {...options} />)
    expect(screen.getByText('外部外键 · 只读')).toBeTruthy()
    expect(screen.getByText('标记标签')).toBeTruthy()
    expect(screen.getByText('用于笔记')).toBeTruthy()
    expect(screen.getByText('N:M')).toBeTruthy()
    expect(screen.queryByRole('button', { name: '查看数据' })).toBeNull()
    fireEvent.click(screen.getAllByRole('button', { name: '查看本体资源：标签' })[0])
    expect(options.onSelect).toHaveBeenCalledWith('tag')
  })

  it('枚举稳定编码与标签保留精确文本并显示废弃状态及可见引用', () => {
    render(<OntologyInspector {...props(enumeration)} />)
    const members = screen.getByRole('table', { name: '枚举成员' })
    expect(within(members).getByText('001')).toBeTruthy()
    expect(within(members).getByText('9007199254740993')).toBeTruthy()
    expect(within(members).getByText('历史状态')).toBeTruthy()
    expect(within(members).getByText('已废弃')).toBeTruthy()
    expect(screen.getByRole('button', { name: '查看本体资源：笔记' })).toBeTruthy()
  })

  it('动作显示作用对象、绑定函数和输入输出，不提供执行或源码内容', () => {
    render(<OntologyInspector {...props(action)} />)
    expect(screen.getAllByText('绑定函数').length).toBeGreaterThan(0)
    expect(screen.getByRole('heading', { name: '作用对象' })).toBeTruthy()
    expect(screen.getByText('原文')).toBeTruthy()
    expect(screen.getByText('摘要')).toBeTruthy()
    expect(screen.getAllByRole('button', { name: '查看本体资源：提取摘要' }).length).toBeGreaterThan(0)
    expect(document.body.textContent).not.toContain('TOP_SECRET_IMPLEMENTATION')
    expect(screen.queryByRole('button', { name: /运行|调用|执行/ })).toBeNull()
  })

  it('函数显示语言、入口和绑定动作，可从引用进入动作', () => {
    const options = props(fn)
    render(<OntologyInspector {...options} />)
    expect(screen.getByText('Python')).toBeTruthy()
    expect(screen.getByText('summarize')).toBeTruthy()
    expect(screen.getByText('8 秒')).toBeTruthy()
    expect(screen.getByRole('heading', { name: '绑定动作与其他引用' })).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '查看本体资源：生成笔记摘要' }))
    expect(options.onSelect).toHaveBeenCalledWith('summarize-note')
    expect(document.body.textContent).not.toContain('TOP_SECRET_IMPLEMENTATION')
  })

  it('直接源码动作按自身声明显示语言与入口，浏览不会渲染其源码', () => {
    const inline: DefinitionRecord = { ...action, definition: { ...action.definition, action: { sideEffects: 'none', runtime: { language: 'javascript', entrypoint: 'run', timeoutSeconds: 6, source: 'PRIVATE_ACTION_SOURCE' } } } }
    render(<OntologyInspector {...props(inline, [inline])} />)
    expect(screen.getByText('直接源码')).toBeTruthy()
    expect(screen.getByText('JavaScript')).toBeTruthy()
    expect(screen.getByText('run')).toBeTruthy()
    expect(document.body.textContent).not.toContain('PRIVATE_ACTION_SOURCE')
    expect(screen.queryByRole('button', { name: /运行|调用|执行/ })).toBeNull()
  })

  it('缺失、其他租户或类型不符的引用只显示不可查看，不暴露名称和ID', () => {
    const hidden: DefinitionRecord = { ...tag, definition: { ...tag.definition, id: 'private-target-id', tenantId: 'other-tenant', name: '其他租户秘密对象' } }
    const invalidLink: DefinitionRecord = { ...link, definition: { ...link.definition, link: { ...link.definition.link!, sourceSchemaId: 'private-target-id', targetSchemaId: 'status-enum' } } }
    render(<OntologyInspector {...props(invalidLink, [invalidLink, hidden, enumeration])} />)
    expect(screen.getAllByText('当前不可查看').length).toBeGreaterThan(0)
    expect(document.body.textContent).not.toContain('其他租户秘密对象')
    expect(document.body.textContent).not.toContain('private-target-id')
    expect(screen.queryByRole('button', { name: '查看本体资源：笔记状态' })).toBeNull()
  })

  it('授权目录清空立即清除旧详情，改名按同一稳定ID读取当前名称', () => {
    const options = props(note)
    const view = render(<OntologyInspector {...options} />)
    const renamed = { ...note, definition: { ...note.definition, name: '知识笔记' } }
    view.rerender(<OntologyInspector {...options} records={[renamed]} />)
    expect(screen.getByRole('heading', { name: '知识笔记' })).toBeTruthy()
    expect(screen.queryByRole('heading', { name: '笔记' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '查看定义' }))
    expect(options.onOpenDefinition).toHaveBeenCalledWith('note')
    view.rerender(<OntologyInspector {...options} records={[]} />)
    expect(screen.getByRole('heading', { name: '选择一项本体资源' })).toBeTruthy()
    expect(screen.queryByText('保存知识条目')).toBeNull()
    expect(screen.queryByRole('button', { name: '查看定义' })).toBeNull()
  })
})
