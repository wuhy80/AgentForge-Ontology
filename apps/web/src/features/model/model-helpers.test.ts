import { describe, expect, it } from 'vitest'
import { newDefinition, validateDefinition } from './model-helpers'
import { initialRuntime } from '../../lib/runtime-language'
import type { ScriptLanguage } from '../../lib/types'

function schema(properties = 1, indexes = 0) {
  const definition = newDefinition('schema', 'category', 'group')
  definition.name = '设备'
  definition.schema!.properties = Array.from({ length: properties }, (_, index) => ({ id: `field-${index}`, name: `属性${index}`, dataType: 'int64' }))
  definition.schema!.indexes = Array.from({ length: indexes }, (_, index) => ({ id: `index-${index}`, name: `索引${index}`, unique: false, fieldIds: ['field-0'] }))
  return definition
}

describe('可发布的对象模型范围', () => {
  it('允许独立超长文本属性并拒绝文本索引', () => {
    for (const dataType of ['text', 'longtext'] as const) {
      const definition = schema(1);
      definition.schema!.properties[0].dataType = dataType;
      expect(validateDefinition(definition)).toBeUndefined();
      definition.schema!.indexes = [{ id: 'index', name: '正文索引', unique: false, fieldIds: ['field-0'] }];
      expect(validateDefinition(definition)).toBe('长文本和超长文本属性不支持索引。');
    }
  })

  it('允许 100 个属性与 32 个索引，超过存储预算时保存前明确拒绝', () => {
    expect(validateDefinition(schema(100, 32))).toBeUndefined()
    expect(validateDefinition(schema(101))).toBe('对象模型最多支持 100 个属性。')
    expect(validateDefinition(schema(1, 33))).toBe('对象模型最多支持 32 个索引。')
  })

  it('对象属性与调用参数支持短文本枚举绑定，拒绝其他类型引用', () => {
    const definition = schema()
    definition.schema!.properties[0].dataType = 'string'
    definition.schema!.properties[0].enumId = 'enum'
    expect(validateDefinition(definition)).toBeUndefined()
    const fn = newDefinition('function', 'category', 'group')
    fn.name = '校验设备'
    fn.function!.input = definition.schema!.properties
    expect(validateDefinition(fn)).toBeUndefined()
    definition.schema!.properties[0].dataType = 'int64'
    expect(validateDefinition(definition)).toBe('枚举绑定只支持短文本属性。')
  })

  it('默认值区分未设置、空字符串、NULL和精确标量，并拒绝非法默认值', () => {
    const definition = schema()
    const property = definition.schema!.properties[0]
    property.default = '9223372036854775807'
    expect(validateDefinition(definition)).toBeUndefined()
    property.default = '9223372036854775808'
    expect(validateDefinition(definition)).toContain('超出 int64')
    property.default = null
    expect(validateDefinition(definition)).toContain('NULL 默认值')
    property.nullable = true
    expect(validateDefinition(definition)).toBeUndefined()
    property.dataType = 'string'; property.default = ''
    expect(validateDefinition(definition)).toBeUndefined()
  })

  it.each(['function', 'action'] as const)('%s 接受 JS/Go 定义，拒绝未知语言及各语言保留入口', (kind) => {
    const definition = newDefinition(kind, 'category', 'group')
    definition.name = '纯计算'
    const callable = definition.function ?? definition.action!
    callable.sideEffects = 'none'
    for (const language of ['javascript', 'go'] as const) {
      callable.runtime = initialRuntime(language)
      expect(validateDefinition(definition)).toBeUndefined()
      callable.runtime.entrypoint = language === 'go' ? 'main' : 'eval'
      expect(validateDefinition(definition)).toContain('入口')
    }
    callable.runtime = { ...initialRuntime('python'), language: 'ruby' as ScriptLanguage }
    expect(validateDefinition(definition)).toContain('当前仅支持')
  })
})
