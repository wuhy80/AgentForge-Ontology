import type { QueryResult } from '../../lib/types';
import { propertyValueText } from './query';

export function AggregateResults({ result }: { result: QueryResult }) {
  return <><p className="ontology-row-grain muted">每行对应一个分组的汇总结果。</p><div className="table-wrap"><table className="query-data-table" aria-label="分组汇总结果"><thead><tr>{result.fields.map((field) => <th key={field.id}>{field.name}<small className="muted">{field.dataType}</small></th>)}</tr></thead><tbody>{result.aggregateRows?.map((row, index) => <tr key={index}>{result.fields.map((field) => <td key={field.id}>{propertyValueText(field, row.values[field.id])}</td>)}</tr>)}</tbody></table></div></>;
}
