// The implemented HTTP contract mirrors contracts/model.go. Numeric int64 and
// decimal values travel as strings; JsonValue also supports other JSON values.
export type JsonValue = null | boolean | string | number | JsonValue[] | { [key: string]: JsonValue }
export type Values = Record<string, JsonValue>
export type WriteOperation = 'create' | 'update' | 'delete' | 'upsert' | 'bulk_create' | 'bulk_update' | 'bulk_delete' | 'bulk_upsert'
export type ConflictPolicy = 'check' | 'overwrite'
export interface WriteMatch { id?: string; indexId?: string; values?: Values }
export interface UpsertRequest { match: WriteMatch; values: Values; conflictPolicy?: ConflictPolicy; idempotencyKey: string }
export interface BulkWriteItem { clientKey: string; id?: string; ifMatch?: string; match?: WriteMatch; values?: Values }
export interface BulkWriteRequest { atomic: true; items: BulkWriteItem[]; conflictPolicy?: ConflictPolicy; idempotencyKey: string }
export interface WriteResult { schemaId: string; clientKey?: string; outcome: 'created' | 'updated' | 'deleted'; id: string; etag?: string; values?: Values }
export interface BulkWriteResult { schemaId: string; atomic: true; committed: true; replayed?: boolean; results: WriteResult[] }
export interface WriteOperationCapability { name: WriteOperation; allowed: boolean; reason?: string; writeFields: string[] }
export interface WriteCapabilities {
  schemaId: string; operations: WriteOperationCapability[]; fields: Property[]
  matchModes: string[]; uniqueKeys: { id: string; name: string; fieldIds: string[] }[]
  overwriteAllowed: boolean; overwriteWriteFields?: string[]; overwriteUniqueKeyIds?: string[]
  maxBulkItems: number; maxRequestBytes: number; maxResponseBytes: number
}
export type Kind = 'schema' | 'enum' | 'link' | 'action' | 'function'
export type DataType = 'string' | 'text' | 'longtext' | 'uuid' | 'int64' | 'decimal' | 'boolean' | 'timestamp'

export interface StorageBinding {
  dataSourceId: string
  namespace?: string
  table: string
  idColumn?: string
  tenantColumn?: string
}
export interface PropertyMapping {
  dataSourceId: string
  namespace?: string
  table?: string
  column: string
  keyColumn?: string
  tenantColumn?: string
}
export interface Property {
  id: string
  name: string
  dataType: DataType
  required?: boolean
  nullable?: boolean
  readOnly?: boolean
  precision?: number
  scale?: number
  enumId?: string
  enumValues?: EnumValue[]
  enumEtag?: string
  default?: JsonValue
  mapping?: PropertyMapping
}
export interface Index {
  id: string
  name: string
  unique: boolean
  fieldIds: string[]
}
export interface Schema {
  mode: string
  storageLayout: string
  properties: Property[]
  indexes?: Index[]
  storageBinding?: StorageBinding
}

export type DataSourceDriver = 'mysql' | 'postgresql'
export type SourceTLSMode = 'disable' | 'require' | 'verify-full'
export type SourceVersionPolicy = 'auto' | 'pinned'
export interface SourceVersionOption {
  driver: DataSourceDriver
  versionLine: string
  label: string
  status: 'supported' | 'pending'
}
export interface SourceCompatibility {
  connected: boolean
  compatibilityStatus: 'supported' | 'unsupported' | 'mismatch' | 'failed' | 'pending'
  detectedProduct?: string
  serverVersion?: string
  versionLine?: string
  capabilityProfile?: string
  capabilities: string[]
  checkedAt: string
  code?: string
  message?: string
}
export interface DataSource {
  id: string
  tenantId: string
  name: string
  driver: DataSourceDriver
  database: string
  enabled: boolean
  tenantIsolation: 'column' | 'dedicated'
  etag: string
  host?: string
  port?: number
  username?: string
  tlsMode?: SourceTLSMode
  passwordSet?: boolean
  versionPolicy?: SourceVersionPolicy
  expectedVersionLine?: string
  compatibility?: SourceCompatibility
}
export interface DataSourceInput {
  name: string
  driver: DataSourceDriver
  host: string
  port: number
  database: string
  username: string
  password?: string
  tlsMode: SourceTLSMode
  enabled: boolean
  tenantIsolation: 'column' | 'dedicated'
  versionPolicy?: SourceVersionPolicy
  expectedVersionLine?: string
}
export interface SourceDatabaseMetric {
  available: boolean
  value?: string
  unit: 'connections' | 'bytes' | 'queries/s'
  scope: 'instance' | 'database'
  basis: string
  sampleWindowMs?: number
  reason?: string
}
export interface SourceOverview {
  sourceId: string
  etag: string
  checkedAt: string
  serverVersion: string
  database: string
  charset?: string
  metrics: { connections: SourceDatabaseMetric; storageBytes: SourceDatabaseMetric; qps: SourceDatabaseMetric }
}
export interface SourceTableInfo { namespace: string; name: string; type: string; columnCount?: number; primaryKeyCount?: number }
export interface SourceColumn {
  name: string
  databaseType: string
  dataType: DataType | string
  nullable: boolean
  primaryKey: boolean
  generated: boolean
  precision?: number
  scale?: number
  maxLength?: number
  position: number
  defaultValue?: string | null
  comment?: string
}
export interface SourceIndex { name: string; unique: boolean; primary: boolean; columns: string[]; predicate?: string }
export interface SourceTable { namespace: string; name: string; columns: SourceColumn[]; indexes: SourceIndex[]; signature: string }
export interface SourceTestResult { success: boolean; serverVersion: string; compatibility: SourceCompatibility }
export interface SourceQueryResult { columns: string[]; rows: (string | null)[][]; hasMore: boolean; limit: number; elapsedMs: number }
export interface SourceSQLToolResult { sql: string; valid: boolean; dialect: string; message: string }
export interface StorageMappingTable {
  dataSourceId?: string
  dataSourceName: string
  driver: string
  namespace: string
  table: string
  keyColumn: string
  tenantColumn?: string
}
export interface StoragePropertyMapping extends StorageMappingTable {
  propertyId: string
  propertyName: string
  column: string
  databaseType: string
  exists: boolean
}
export interface SchemaStorageMapping {
  schemaId: string
  definitionETag: string
  managed: boolean
  mode: string
  primary: StorageMappingTable
  properties: StoragePropertyMapping[]
}
export interface EnumValue { code: string; label: string; deprecated?: boolean }
export interface Enum { values: EnumValue[] }
export interface Link {
  sourceSchemaId: string
  targetSchemaId: string
  forwardName: string
  reverseName: string
  cardinality: '1:1' | '1:N' | 'N:1' | 'N:M'
  onDelete: string
  storageBinding?: LinkStorageBinding
}
export interface LinkStorageBinding {
  mode: 'managed' | 'junction' | 'foreign_key'
  dataSourceId?: string
  namespace?: string
  table?: string
  sourceColumn?: string
  targetColumn?: string
  tenantColumn?: string
  foreignKeySide?: 'source' | 'target'
  foreignKeyColumn?: string
}
// sourceSchemaId is the navigation start and targetSchemaId its result,
// including for the reverse direction returned by Metadata.
export interface SchemaLink {
  linkRef: { id: string }
  direction: 'out' | 'in'
  name: string
  sourceSchemaId: string
  targetSchemaId: string
  cardinality: Link['cardinality']
}
export interface LinkInstance { id: string; linkId: string; sourceId: string; targetId: string; etag: string }
export interface LinkTraversal { linkId: string; direction: 'out' | 'in'; instanceId: string }
export interface Callable {
  input?: Property[]
  output?: Property[]
  targetSchemaIds?: string[]
  functionId?: string
  artifactRef?: string
  sideEffects: 'none' | 'dataWrite' | 'externalWrite'
  dataWrite?: DataWriteCapability[]
  runtime?: ScriptRuntime
  testCases?: CallableTestCase[]
}
export interface DataWriteCapability { schemaId: string; operations: ('create' | 'update' | 'delete' | 'upsert')[] }
export interface CallableTestCase { id: string; name: string; input: Values }
export interface SourceDiagnostic { code: string; message: string; line: number; column?: number; endLine?: number; endColumn?: number }
export type ScriptLanguage = 'python' | 'javascript' | 'go'
export interface ScriptRuntime { language: ScriptLanguage; source: string; entrypoint: string; timeoutSeconds: number }
export interface SourceToolResult { source: string; diagnostics: SourceDiagnostic[] }
export interface ExecutionCapabilities { invocation: boolean; runtimes: string[]; synchronous: boolean; actionInlineSource?: boolean; draftInvocation?: boolean; dataWriteTransactions?: boolean; dataWriteDraftInvocation?: boolean; maxTransactionItems?: number; sideEffects?: string[]; reason?: string }
export interface Invocation {
  id: string
  definitionId: string
  definitionName: string
  kind: Kind
  status: 'running' | 'committing' | 'succeeded' | 'failed' | 'timed_out' | 'canceled' | 'interrupted'
  transaction?: { status: string; committed: boolean; operationCount: number }
  output?: Values
  error?: { code: string; message: string }
  createdAt: string
  finishedAt?: string
  codeDigest: string
  mode?: 'current' | 'draft'
  draftEtag?: string
  diagnostics?: SourceDiagnostic[]
}
export interface Definition {
  id: string
  tenantId: string
  categoryId: string
  groupId: string
  kind: Kind
  name: string
  description?: string
  schema?: Schema
  enum?: Enum
  link?: Link
  action?: Callable
  function?: Callable
}
export type PropertyInput = Omit<Property, 'id'> & { id?: string }
export type IndexInput = Omit<Index, 'id'> & { id?: string }
export type SchemaInput = Omit<Schema, 'properties' | 'indexes'> & { properties: PropertyInput[]; indexes?: IndexInput[] }
export type CallableInput = Omit<Callable, 'input' | 'output'> & { input?: PropertyInput[]; output?: PropertyInput[] }
export type DefinitionInput = Omit<Definition, 'id' | 'tenantId' | 'schema' | 'action' | 'function'> & {
  id?: string
  tenantId?: string
  schema?: SchemaInput
  action?: CallableInput
  function?: CallableInput
}
export interface DefinitionRecord {
  definition: Definition
  etag: string
  status: string
  // Composed by the UI from independently authorized current and draft views.
  hasCurrent?: boolean
}
export interface Organization { id: string; tenantId: string; categoryId?: string; name: string }
export interface Page<T> { data: T[]; hasMore: boolean; nextCursor?: string }
export interface Publication { id: string; tenantId: string; definitionId: string; status: string; error?: string }
export interface Preview { operationId: string; status: string; statements?: string[] }
export interface PermissionValueRef {
  source: 'user' | 'object'
  attribute?: 'id' | 'tenantId' | 'username' | 'displayName' | 'roleIds' | 'userGroupIds'
  schemaId?: string
  fieldId?: string
  instanceId?: string
  matchFieldId?: string
  matchUserAttribute?: 'id' | 'username'
  mode?: 'scalar' | 'set'
}
export interface Expression { op: string; field?: string; value?: JsonValue; valueRef?: PermissionValueRef; args?: Expression[] }
export interface Source { schemaId?: string; category?: string; schema?: string }
export interface Order { field: string; direction: 'asc' | 'desc' }
export interface QuerySpec {
  from: Source
  via?: LinkTraversal
  rows?: RowQuery
  where?: Expression
  select: string[]
  orderBy?: Order[]
  page: { limit: number; cursor?: string }
}
export interface Instance { id: string; schemaId: string; values: Values; etag: string }
export interface RowQueryNode { id: string; schemaId: string; alias: string }
export interface RowQueryJoin { from: string; to: string; linkId: string; direction: 'out' | 'in' }
export interface RowQuery { nodes: RowQueryNode[]; joins: RowQueryJoin[] }
export interface QueryRowEntity { nodeId: string; schemaId: string; instanceId: string }
export interface QueryRow { id: string; values: Values; entities: QueryRowEntity[] }
export interface QueryResult { data: Instance[]; fields: Property[]; rowMode?: boolean; rows?: QueryRow[]; nextCursor?: string; hasMore: boolean }
export interface SQLRange { from: number; to: number; line: number; column: number }
export interface SQLDiagnostic { code: string; message: string; severity: 'error' | 'warning'; range: SQLRange }
export interface SQLParameter {
  name: string; dataType: DataType; nullable: boolean; precision?: number; scale?: number; ranges: SQLRange[]; provided: boolean
}
export interface OntologySQLRequest {
  text: string; parameters?: Record<string, JsonValue>; defaultCategoryId?: string; requestId?: string; cursorOffset?: number
}
export interface OntologySQLAnalysis {
  requestId: string; textHash: string; diagnostics: SQLDiagnostic[]; parameters: SQLParameter[]; fields: Property[]; query?: QuerySpec; scope?: RowQuery; ready: boolean
}
export interface SQLCompletion { label: string; insertText: string; detail: string; kind: string; range: SQLRange }
export interface OntologySQLCompletions { requestId: string; textHash: string; items: SQLCompletion[] }
export interface OntologySQLFormat { requestId: string; textHash: string; text: string; diagnostics: SQLDiagnostic[] }
export interface OntologySQLResult extends QueryResult { query: QuerySpec; requestId: string; textHash: string }
export interface QueryContextField extends Property { filterable: boolean; sortable: boolean }
export interface QueryContextModel { schemaId: string; categoryId: string; category: string; name: string; fields: QueryContextField[] }
export interface QueryContext { models: QueryContextModel[]; nextCursor?: string }
export interface QueryPathsRequest extends OntologySQLRequest { targetSchemaId: string }
export interface QueryPathStep { linkId: string; name: string; category: string; direction: 'out' | 'in'; fromAlias: string; toAlias: string; target: QueryContextModel }
export interface QueryPathCandidate { id: string; fromAlias: string; steps: QueryPathStep[]; text: string }
export interface QueryPathsResult { requestId: string; textHash: string; candidates: QueryPathCandidate[]; diagnostics: SQLDiagnostic[]; truncated: boolean }
export interface QueryFieldInsertionRequest extends OntologySQLRequest { schemaId: string; fieldId: string; alias?: string; selectionFrom: number; selectionTo: number }
export interface QueryFieldInsertionResult { requestId: string; textHash: string; text: string; from: number; to: number; diagnostics: SQLDiagnostic[] }
export interface MetadataCapabilities {
  definitions: Kind[]
  lifecycle: string[]
  definitionUpdates: string
  schemaModes: string[]
  storageLayouts: string[]
  invocation: boolean
  delete: boolean
  disable: boolean
  schemaInheritance: boolean
  dbToSchema: boolean
  schemaMigrations?: { addFields: boolean; addIndexes: boolean; destructive: boolean }
  enumReferences?: boolean
}
export interface DataCapabilities {
  backend: string
  schemaModes: string[]
  layouts: string[]
  operations: string[]
  dataTypes?: DataType[]
  maxTextBytes?: number
  maxLongTextBytes?: number
  maxRequestBytes?: number
  maxPageSize: number
  maxSortFields: number
  links: boolean
  ontologySQL: boolean
  rowQueries?: boolean
  maxRowJoins?: number
  queryPathPlanning?: boolean
  batch: boolean
  calculatedProperties: boolean
}

export interface User {
  id: string
  tenantId: string
  username: string
  displayName: string
  enabled: boolean
  administrator: boolean
  roleIds: string[]
  etag: string
}
export interface Role { id: string; name: string; description: string; etag: string }
export interface Session { user: User; expiresAt: number; csrfToken: string }
export interface LoginRequest { tenant: string; username: string; password: string }
export interface UserInput {
  username: string
  displayName: string
  password?: string
  enabled: boolean
  administrator: boolean
  roleIds: string[]
}
export interface RoleInput { name: string; description: string }
// Identity groups are independent of the groups used to organize definitions.
export interface UserGroupInput {
  name: string
  description: string
  enabled: boolean
  roleIds: string[]
  userIds: string[]
}
export interface UserGroup extends UserGroupInput { id: string; etag: string }
export interface PermissionRule {
  id: string
  roleId: string
  effect: 'allow' | 'deny'
  operation: string
  resourceId: string
  readFields: string[]
  writeFields: string[]
  rowFilter?: Expression | null
  newRowFilter?: Expression | null
}
export interface PermissionPolicy { rules: PermissionRule[] }
export interface PolicyRecord { current: PermissionPolicy; draft?: PermissionPolicy | null; etag: string; state: string }
export interface AuditRecord { id: string; actorId: string; operation: string; targetId: string; createdAt: string }
export interface Decision {
  allow: boolean
  readFields: string[]
  writeFields: string[]
  rowFilter?: Expression | null
  newRowFilter?: Expression | null
  state: string
}
export interface SimulationRequest { userId: string; request: { operation: string; resourceId: string }; draft: boolean }
export interface SimulationResult { decision: Decision; reason: string; contextProtected?: boolean }
