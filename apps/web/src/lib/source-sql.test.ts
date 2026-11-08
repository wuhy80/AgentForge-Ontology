import { describe, expect, it } from 'vitest'
import { buildSourceSQL, sourceSQLPresets } from './source-sql'

describe('数据源只读查询模板', () => {
  it('当前表查询按方言引用转义，不把表名当可执行文本', () => {
    expect(buildSourceSQL('mysql', 'current_table', { namespace: '业务`库', name: 'order`items', type: 'table' })).toBe('SELECT * FROM `业务``库`.`order``items` LIMIT 100;')
    expect(buildSourceSQL('postgresql', 'current_table', { namespace: '业务"库', name: 'order"items', type: 'table' })).toBe('SELECT * FROM "业务""库"."order""items" LIMIT 100;')
    expect(() => buildSourceSQL('mysql', 'current_table')).toThrow('选择数据表')
  })
  it.each(['mysql', 'postgresql'] as const)('%s 统计模板固定当前库范围与平台表排除，估算行数不声称精确', (driver) => {
    expect(sourceSQLPresets).toHaveLength(3)
    const storage = buildSourceSQL(driver, 'table_storage'), rows = buildSourceSQL(driver, 'table_rows')
    expect(storage).toContain('AS total_bytes'); expect(storage).toContain('ORDER BY total_bytes DESC')
    expect(rows).toContain('AS estimated_rows'); expect(rows).toContain('ORDER BY estimated_rows DESC')
    for (const sql of [storage, rows]) {
      expect(sql).toContain("<> 'af_'")
      expect(sql).toMatch(/^SELECT /)
      expect(sql).toMatch(/LIMIT 20;$/)
      if (driver === 'mysql') expect(sql).toContain('TABLE_SCHEMA = DATABASE()')
      else { expect(sql).toContain("has_table_privilege(c.oid, 'SELECT')"); expect(sql).toContain("has_schema_privilege(n.oid, 'USAGE')") }
    }
  })
})
