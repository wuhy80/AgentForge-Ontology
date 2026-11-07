// The implemented HTTP contract mirrors contracts/model.go. Numeric int64 and
// decimal values travel as strings; JsonValue also supports other JSON values.
export type JsonValue = null | boolean | string | number | JsonValue[] | { [key: string]: JsonValue }
export type Values = Record<string, JsonValue>
export type Kind = 'schema' | 'enum' | 'link' | 'action' | 'function'
export type DataType = 'string' | 'text' | 'longtext' | 'uuid' | 'int64' | 'decimal' | 'boolean' | 'timestamp'

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
  default?: JsonValue
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
}
export interface Callable {
  input?: Property[]
  output?: Property[]
  functionId?: string
  artifactRef?: string
  sideEffects: 'none' | 'dataWrite' | 'externalWrite'
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
export interface Expression { op: string; field?: string; value?: JsonValue; args?: Expression[] }
export interface Source { schemaId?: string; category?: string; schema?: string }
export interface Order { field: string; direction: 'asc' | 'desc' }
export interface QuerySpec {
  from: Source
  where?: Expression
  select: string[]
  orderBy?: Order[]
  page: { limit: number; cursor?: string }
}
export interface Instance { id: string; schemaId: string; values: Values; etag: string }
export interface QueryResult { data: Instance[]; fields: Property[]; nextCursor?: string; hasMore: boolean }
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
export interface SimulationResult { decision: Decision; reason: string }
