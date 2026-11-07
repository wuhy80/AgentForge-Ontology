import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useState } from 'react'
import type { ApiClient } from '../../lib/api'
import type { DefinitionRecord, Link } from '../../lib/types'
import { ModelEditor } from './ModelEditor'
import { LinkStorageEditor, validateLinkStorage } from './LinkStorageEditor'

afterEach(cleanup)
function schema(id: string, dataSourceId?: string): DefinitionRecord {
  return { status: 'active', etag: id, definition: { id, tenantId: 'tenant', categoryId: 'category', groupId: 'group', kind: 'schema', name: id === 'notes' ? '笔记' : '标签', schema: {
    mode: dataSourceId ? 'db_2_schema' : 'schema_2_db', storageLayout: 'single', properties: [{ id: 'name', name: '名称', dataType: 'string' }], ...(dataSourceId ? { storageBinding: { dataSourceId, namespace: 'public', table: id, idColumn: 'id' } } : {}),
  } } }
}
const link: Link = { sourceSchemaId: 'notes', targetSchemaId: 'tags', forwardName: '标记标签', reverseName: '标记笔记', cardinality: 'N:M', onDelete: 'restrict' }
const schemas = [schema('notes'), schema('tags')]

describe('Link物理映射', () => {
  it('已生效的纯定义允许补充映射，草稿仍可更改，发布后锁定', async () => {
    const initialRecord: DefinitionRecord = { status: 'active', etag: 'current', definition: { ...schemas[0].definition, id: 'note-tags', kind: 'link', name: '笔记标记标签', schema: undefined, link } }
    const api = {
      listGroups: vi.fn().mockResolvedValue([{ id: 'group', categoryId: 'category', tenantId: 'tenant', name: '基础资料' }]),
      saveDraft: vi.fn().mockImplementation(async (_id, definition) => ({ definition, status: 'draft', etag: 'draft-current' })),
      publish: vi.fn().mockResolvedValue({ id: 'publication', tenantId: 'tenant', definitionId: 'note-tags', status: 'succeeded' }),
    }
    render(<ModelEditor api={api as unknown as ApiClient} categoryId="category" categories={[{ id: 'category', tenantId: 'tenant', name: '知识库' }]} entries={[...schemas, initialRecord]} currentEntries={[...schemas, initialRecord]} initialKind="link" initialRecord={initialRecord} onClose={vi.fn()} onOrganizations={vi.fn()} onSaved={vi.fn().mockResolvedValue(undefined)} onPending={vi.fn()} onPublished={vi.fn().mockResolvedValue(undefined)} />)
    expect(screen.getByText(/当前只有关系定义/)).toBeTruthy()
    expect((screen.getByLabelText('来源模型') as HTMLSelectElement).disabled).toBe(true)
    fireEvent.change(screen.getByLabelText('关系存储方式'), { target: { value: 'managed' } })
    fireEvent.click(screen.getByRole('button', { name: '保存草稿' }))
    await waitFor(() => expect(api.saveDraft).toHaveBeenCalled())
    expect(api.saveDraft.mock.calls[0][1].link.storageBinding).toEqual({ mode: 'managed' })
    expect((screen.getByLabelText('关系存储方式') as HTMLSelectElement).disabled).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: '发布定义' }))
    await screen.findByText('发布成功，当前定义已生效。')
    expect((screen.getByLabelText('关系存储方式') as HTMLSelectElement).disabled).toBe(true)
  })

  it('外部关联表从真实目录选表列并继承两端数据源', async () => {
    const api = {
      listDataSources: vi.fn().mockResolvedValue([{ id: 'source', name: '知识库数据库', driver: 'postgresql', enabled: true }]),
      listSourceTables: vi.fn().mockResolvedValue([{ namespace: 'public', name: 'note_tags', type: 'table' }]),
      inspectSourceTable: vi.fn().mockResolvedValue({ columns: ['note_id', 'tag_id', 'tenant_id'].map((name) => ({ name, databaseType: 'text' })) }),
    }
    function Editor() {
      const [value, setValue] = useState(link)
      return <><LinkStorageEditor api={api as unknown as ApiClient} link={value} schemas={[schema('notes', 'source'), schema('tags', 'source')]} locked={false} active onChange={setValue} /><output aria-label="配置结果">{JSON.stringify(value.storageBinding)}</output></>
    }
    render(<Editor />)
    fireEvent.change(screen.getByLabelText('关系存储方式'), { target: { value: 'junction' } })
    await screen.findByRole('option', { name: 'public.note_tags' })
    fireEvent.change(screen.getByLabelText('关联表'), { target: { value: JSON.stringify(['public', 'note_tags']) } })
    await waitFor(() => expect(api.inspectSourceTable).toHaveBeenCalled())
    await screen.findAllByRole('option', { name: 'note_id · text' })
    fireEvent.change(screen.getByLabelText('来源对象关联列'), { target: { value: 'note_id' } })
    fireEvent.change(screen.getByLabelText('目标对象关联列'), { target: { value: 'tag_id' } })
    fireEvent.change(screen.getByLabelText('关联表租户列'), { target: { value: 'tenant_id' } })
    expect(JSON.parse(screen.getByLabelText('配置结果').textContent!)).toEqual({ mode: 'junction', dataSourceId: 'source', namespace: 'public', table: 'note_tags', sourceColumn: 'note_id', targetColumn: 'tag_id', tenantColumn: 'tenant_id' })
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('禁止跨源关系、外部端点使用平台表及不匹配的外键基数', () => {
    expect(validateLinkStorage({ ...link, storageBinding: { mode: 'managed' } }, [schema('notes', 'source'), schema('tags')])).toContain('平台默认存储')
    expect(validateLinkStorage({ ...link, storageBinding: { mode: 'junction', dataSourceId: 'source', table: 'edges', sourceColumn: 'a', targetColumn: 'b' } }, [schema('notes', 'source'), schema('tags', 'other')])).toContain('跨数据源')
    expect(validateLinkStorage({ ...link, storageBinding: { mode: 'foreign_key', dataSourceId: 'source', foreignKeySide: 'source', foreignKeyColumn: 'tag_id' } }, [schema('notes', 'source'), schema('tags', 'source')])).toContain('基数不匹配')
    expect(validateLinkStorage({ ...link, cardinality: 'N:1', storageBinding: { mode: 'foreign_key', dataSourceId: 'source', foreignKeySide: 'source', foreignKeyColumn: 'tag_id' } }, [schema('notes', 'source'), schema('tags', 'source')])).toBeUndefined()
  })
})
