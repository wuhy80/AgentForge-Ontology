import type { DataSourceDriver, SourceTableInfo } from './types'

export type SourceSQLPreset = 'current_table' | 'table_storage' | 'table_rows'
export const sourceSQLPresets: { id: SourceSQLPreset; label: string }[] = [
  { id: 'current_table', label: '当前表查询' },
  { id: 'table_storage', label: '表占用排行 (数据+索引)' },
  { id: 'table_rows', label: '表行数排行 (估算)' },
]
const statisticsSQL: Record<DataSourceDriver, Record<'table_storage' | 'table_rows', string>> = {
  mysql: {
    table_storage: "SELECT TABLE_SCHEMA AS namespace, TABLE_NAME AS table_name, DATA_LENGTH AS data_bytes, INDEX_LENGTH AS index_bytes, DATA_LENGTH + INDEX_LENGTH AS total_bytes FROM information_schema.tables WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE' AND LEFT(LOWER(TABLE_NAME), 3) <> 'af_' ORDER BY total_bytes DESC, TABLE_NAME ASC LIMIT 20;",
    table_rows: "SELECT TABLE_SCHEMA AS namespace, TABLE_NAME AS table_name, TABLE_ROWS AS estimated_rows FROM information_schema.tables WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE' AND LEFT(LOWER(TABLE_NAME), 3) <> 'af_' ORDER BY estimated_rows DESC, TABLE_NAME ASC LIMIT 20;",
  },
  postgresql: {
    table_storage: "SELECT n.nspname AS namespace, c.relname AS table_name, pg_catalog.pg_table_size(c.oid) AS data_bytes, pg_catalog.pg_indexes_size(c.oid) AS index_bytes, pg_catalog.pg_total_relation_size(c.oid) AS total_bytes FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind = 'r' AND LEFT(LOWER(n.nspname), 3) <> 'pg_' AND n.nspname <> 'information_schema' AND LEFT(LOWER(c.relname), 3) <> 'af_' AND pg_catalog.has_table_privilege(c.oid, 'SELECT') AND pg_catalog.has_schema_privilege(n.oid, 'USAGE') ORDER BY total_bytes DESC, n.nspname ASC, c.relname ASC LIMIT 20;",
    table_rows: "SELECT n.nspname AS namespace, c.relname AS table_name, CASE WHEN c.reltuples < 0 THEN NULL ELSE CAST(c.reltuples AS bigint) END AS estimated_rows FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind = 'r' AND LEFT(LOWER(n.nspname), 3) <> 'pg_' AND n.nspname <> 'information_schema' AND LEFT(LOWER(c.relname), 3) <> 'af_' AND pg_catalog.has_table_privilege(c.oid, 'SELECT') AND pg_catalog.has_schema_privilege(n.oid, 'USAGE') ORDER BY estimated_rows DESC NULLS LAST, n.nspname ASC, c.relname ASC LIMIT 20;",
  },
}
export function buildSourceSQL(driver: DataSourceDriver, preset: SourceSQLPreset, table?: SourceTableInfo): string {
  if (preset !== 'current_table') return statisticsSQL[driver][preset]
  if (!table) throw new Error('请先选择数据表。')
  const quote = (value: string) => driver === 'mysql' ? `\`${value.replaceAll('`', '``')}\`` : `"${value.replaceAll('"', '""')}"`
  return `SELECT * FROM ${quote(table.namespace)}.${quote(table.name)} LIMIT 100;`
}
