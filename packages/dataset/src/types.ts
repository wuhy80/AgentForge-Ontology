export type Value = string | boolean | null;
export type ValueList = readonly Value[];
export type Values = Record<string, Value>;

export interface Property {
  readonly id: string;
  readonly name: string;
  readonly dataType: string;
  readonly nullable?: boolean;
  readonly required?: boolean;
  readonly readOnly?: boolean;
  readonly precision?: number;
  readonly scale?: number;
  readonly enumId?: string;
  readonly enumValues?: readonly { readonly code: string; readonly label: string; readonly deprecated?: boolean }[];
  readonly enumEtag?: string;
}
export interface Source { readonly schemaId?: string; readonly category?: string; readonly schema?: string }
export interface Expression {
  readonly op: string;
  readonly field?: string;
  readonly value?: Value | ValueList;
  readonly args?: readonly Expression[];
}
export type AggregateOperation = 'count' | 'countDistinct' | 'sum' | 'avg' | 'min' | 'max';
export interface AggregateMeasure { readonly id: string; readonly op: AggregateOperation; readonly field?: string; readonly name?: string }
export interface AggregateQuery { readonly groupBy?: readonly string[]; readonly measures: readonly AggregateMeasure[] }
export interface Order { readonly field: string; readonly direction: 'asc' | 'desc' }
export interface QuerySpec {
  readonly from: Source;
  readonly where?: Expression;
  readonly select: readonly string[];
  readonly orderBy?: readonly Order[];
  readonly aggregate?: AggregateQuery;
  readonly page: { readonly limit: number; readonly cursor?: string };
}
export interface QueryBinding { readonly query: QuerySpec; readonly fields: readonly Property[] }
export interface QueryTransport {
  bind(query: QuerySpec, signal?: AbortSignal): Promise<QueryBinding>;
  query(query: QuerySpec, signal?: AbortSignal): Promise<QueryResult>;
}
export interface Instance { readonly id: string; readonly schemaId: string; readonly values: Values; readonly etag: string }
export interface QueryResult {
  readonly data: readonly Instance[];
  readonly fields: readonly Property[];
  readonly aggregateMode?: boolean;
  readonly aggregateRows?: readonly { readonly values: Values }[];
  readonly hasMore: boolean;
  readonly nextCursor?: string;
}
export interface DataSetPage { readonly result: QueryResult; readonly nextPage: (signal?: AbortSignal) => Promise<DataSetPage | undefined> }
