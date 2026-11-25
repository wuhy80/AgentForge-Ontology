import { useState } from 'react'
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { Schema } from '../../lib/types'
import { SchemaLifecycleEditor } from './SchemaLifecycleEditor'
import { validateLifecycle } from './model-helpers'

afterEach(cleanup)

const baseSchema = (): Schema => ({ mode: 'schema_2_db', storageLayout: 'single', properties: [] })

function Harness({ initial, disabled = false }: { initial: Schema; disabled?: boolean }) {
  const [schema, setSchema] = useState(initial)
  return <>
    <SchemaLifecycleEditor schema={schema} disabled={disabled} onChange={setSchema} />
    <output data-testid="lifecycle">{JSON.stringify(schema.lifecycle ?? null)}</output>
  </>
}

const current = () => JSON.parse(screen.getByTestId('lifecycle').textContent ?? 'null')

describe('记录流程编辑', () => {
  it('未声明时按需创建初始阶段，并补齐阶段和流转', async () => {
    render(<Harness initial={baseSchema()} />)
    expect(screen.getByText('尚未声明记录流程。未声明时记录详情不显示阶段与流转入口。')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: '添加记录流程' }))
    expect((screen.getByLabelText('阶段1名称') as HTMLInputElement).value).toBe('草稿')
    expect((screen.getByLabelText('设为初始阶段1') as HTMLInputElement).checked).toBe(true)
    expect(current().stages).toHaveLength(1)

    await userEvent.click(screen.getByRole('button', { name: '添加阶段' }))
    await userEvent.clear(screen.getByLabelText('阶段2名称'))
    await userEvent.type(screen.getByLabelText('阶段2名称'), '审核中')
    expect(current().stages[1].name).toBe('审核中')

    const [first, second] = current().stages
    await userEvent.click(screen.getByRole('button', { name: '添加流转' }))
    expect(current().transitions).toHaveLength(1)
    expect(current().transitions[0]).toMatchObject({ from: first.id, to: second.id, name: '草稿 → 审核中' })
    expect((screen.getByLabelText('流转1起点') as HTMLSelectElement).value).toBe(first.id)
    expect((screen.getByLabelText('流转1终点') as HTMLSelectElement).value).toBe(second.id)

    await userEvent.click(screen.getByRole('button', { name: '删除流转1' }))
    expect(current().transitions).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: '移除记录流程' }))
    expect(current()).toBeNull()
    expect(screen.getByRole('button', { name: '添加记录流程' })).toBeTruthy()
  })

  it('初始阶段与被引用阶段不能删除，切换初始阶段后仍保持稳定 ID', async () => {
    const draft = { id: 'stage-draft', name: '草稿' }, review = { id: 'stage-review', name: '审核中' }
    render(<Harness initial={{ ...baseSchema(), lifecycle: { initialStageId: draft.id, stages: [draft, review], transitions: [] } }} />)
    expect((screen.getByRole('button', { name: '删除阶段1' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: '删除阶段2' }) as HTMLButtonElement).disabled).toBe(false)
    await userEvent.click(screen.getByLabelText('设为初始阶段2'))
    expect(current().initialStageId).toBe(review.id)
    expect((screen.getByRole('button', { name: '删除阶段2' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: '删除阶段1' }) as HTMLButtonElement).disabled).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: '删除阶段1' }))
    expect(current().stages).toEqual([review])
  })

  it('删除引用中的阶段前必须先删除流转，停用状态不可编辑', async () => {
    const draft = { id: 'stage-draft', name: '草稿' }, review = { id: 'stage-review', name: '审核中' }
    const lifecycle = { initialStageId: draft.id, stages: [draft, review],
      transitions: [{ id: 'move', name: '提交审核', from: draft.id, to: review.id }] }
    render(<Harness initial={{ ...baseSchema(), lifecycle }} />)
    expect((screen.getByRole('button', { name: '删除阶段2' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: '删除阶段1' }) as HTMLButtonElement).disabled).toBe(true)
    await userEvent.click(screen.getByRole('button', { name: '删除流转1' }))
    expect((screen.getByRole('button', { name: '删除阶段2' }) as HTMLButtonElement).disabled).toBe(false)

    cleanup()
    render(<Harness initial={{ ...baseSchema(), lifecycle }} disabled />)
    const panel = screen.getByLabelText('阶段1名称').closest('section') ?? document.body
    expect((within(panel as HTMLElement).getByRole('button', { name: '添加阶段' }) as HTMLButtonElement).disabled).toBe(true)
    expect((within(panel as HTMLElement).getByRole('button', { name: '添加流转' }) as HTMLButtonElement).disabled).toBe(true)
    expect((within(panel as HTMLElement).getByRole('button', { name: '移除记录流程' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByLabelText('阶段1名称') as HTMLInputElement).disabled).toBe(true)
  })
})

describe('记录流程校验', () => {
  const schema = (lifecycle: Schema['lifecycle']): Schema => ({ ...baseSchema(), lifecycle })

  it('拒绝重复名称、缺失初始阶段、自环和超限', () => {
    const draft = { id: 'a', name: '草稿' }, review = { id: 'b', name: ' 草稿 ' }
    expect(validateLifecycle(schema({ initialStageId: 'a', stages: [draft, review] }))).toBe('阶段名称不能重复。')
    expect(validateLifecycle(schema({ initialStageId: 'missing', stages: [draft] }))).toBe('请选择一个已声明的初始阶段。')
    expect(validateLifecycle(schema({ initialStageId: 'a', stages: [draft], transitions: [{ id: 'move', name: '自环', from: 'a', to: 'a' }] }))).toBe('每条流转必须连接两个不同的已声明阶段。')
    expect(validateLifecycle(schema({ initialStageId: 'a', stages: [{ id: 'a', name: '  ' }] }))).toBe('请填写每个阶段的名称。')
    const many = Array.from({ length: 21 }, (_, index) => ({ id: `stage-${index}`, name: `阶段${index}` }))
    expect(validateLifecycle(schema({ initialStageId: 'stage-0', stages: many }))).toBe('记录流程需要 1–20 个阶段。')
    expect(validateLifecycle(schema({ initialStageId: 'a', stages: [draft, { id: 'b', name: '审核中' }],
      transitions: [{ id: 'move', name: '提交', from: 'a', to: 'b' }, { id: 'move-2', name: '提交', from: 'b', to: 'a' }] }))).toBe('流转名称不能重复。')
  })

  it('外部映射模型不声明平台记录流程', () => {
    const mapped: Schema = { ...baseSchema(), storageBinding: { dataSourceId: 'source', table: 'devices' },
      lifecycle: { initialStageId: 'a', stages: [{ id: 'a', name: '草稿' }] } }
    expect(validateLifecycle(mapped)).toBe('外部映射模型不支持记录流程。')
    render(<SchemaLifecycleEditor schema={mapped} disabled={false} onChange={() => {}} />)
    expect(screen.getByText('外部映射模型的阶段由来源系统维护，暂不支持在此声明记录流程。')).toBeTruthy()
  })
})
