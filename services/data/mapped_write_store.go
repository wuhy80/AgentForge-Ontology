package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type mappedMutationStore struct {
	tx               *sql.Tx
	table            *mappedTable
	p                c.Principal
	current          c.CurrentDefinition
	state            mappedWriteState
	h                *handler
	platformTx       *sql.Tx
	recheckRelations []func(context.Context) error
}

func (s *mappedMutationStore) Tx() *sql.Tx { return s.tx }

func (s *mappedMutationStore) Stamp(item c.Instance) (c.Instance, error) {
	return stampMappedInstance(s.state.secret, s.current, item)
}

func (h *handler) withMappedMutationStore(ctx context.Context, p c.Principal, current c.CurrentDefinition, fn func(mutationStore) error) error {
	tables, err := h.mappedTables(ctx, current.Definition, false)
	if err != nil {
		return err
	}
	defer closeMappedTables(tables)
	if err := validateMappedWriteTable(ctx, current.Definition, tables); err != nil {
		return err
	}
	var status, actual string
	err = h.cfg.Engine.DB().QueryRowContext(ctx, "SELECT status,digest FROM af_data_gates WHERE tenant_id=? AND definition_id=?", p.TenantID, current.Definition.ID).Scan(&status, &actual)
	if err != nil {
		return err
	}
	if status != "ready" || actual != current.ETag {
		return problem(409, "mapping_unavailable", "Schema 正在生效或映射已改变，请重试")
	}
	return h.cfg.Engine.WithTx(ctx, func(platformTx *sql.Tx) error {
		if err := h.gate(ctx, platformTx, p, current); err != nil {
			return err
		}
		tx, err := tables[0].db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		state, enabled, err := readMappedWriteState(ctx, tables[0], current, tx)
		if err != nil {
			return err
		}
		if !enabled {
			return mappedWriteUnsupported("mapped_write_not_initialized")
		}
		store := &mappedMutationStore{tx: tx, table: tables[0], p: p, current: current, state: state, h: h, platformTx: platformTx}
		if err := fn(store); err != nil {
			return err
		}
		for _, check := range store.recheckRelations {
			if err := check(ctx); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

func (s *mappedMutationStore) Lookup(ctx context.Context, match mutationMatch) (c.Instance, bool, error) {
	item := c.Instance{SchemaID: s.current.Definition.ID, Values: map[string]json.RawMessage{}}
	columns := []string{s.table.quoted(s.table.keyColumn)}
	properties := sortedMappedProperties(s.current.Definition.Schema.Properties)
	for _, property := range properties {
		columns = append(columns, s.table.quoted(property.Mapping.Column))
	}
	conditions, args := []string{}, []any{}
	if match.ID != "" {
		if !uuidPattern.MatchString(match.ID) || match.ID != strings.ToLower(match.ID) {
			return item, false, problem(422, "invalid_instance_id", "写入目标必须使用规范UUID")
		}
		conditions = append(conditions, s.table.quoted(s.table.keyColumn)+"=?")
		args = append(args, match.ID)
	} else {
		if match.Index == nil || !match.Index.Unique {
			return item, false, problem(422, "invalid_match", "upsert需要明确的实例ID或唯一键")
		}
		for _, fieldID := range match.Index.FieldIDs {
			property, ok := findProperty(s.current.Definition.Schema, fieldID)
			if !ok {
				return item, false, problem(422, "invalid_match", "upsert匹配字段无效")
			}
			value, err := typedValue(property, match.Values[fieldID])
			if err != nil {
				return item, false, err
			}
			conditions = append(conditions, s.table.quoted(property.Mapping.Column)+"="+parameter(property))
			args = append(args, value)
		}
	}
	values, dest := make([]any, len(columns)), make([]any, len(columns))
	for i := range values {
		dest[i] = &values[i]
	}
	err := s.tx.QueryRowContext(ctx, "SELECT "+strings.Join(columns, ",")+" FROM "+s.table.qualified()+" WHERE "+strings.Join(conditions, " AND ")+" FOR UPDATE", args...).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return item, false, nil
	}
	if err != nil {
		return item, false, err
	}
	budget := jsonBudget{remaining: c.MaxDataResponseBytes - 1024}
	if err := budget.checkDatabaseRow(values); err != nil {
		return item, false, err
	}
	item.ID, err = mappedDatabaseID(s.table, values[0])
	if err != nil || !uuidPattern.MatchString(item.ID) {
		return item, false, problem(409, "source_drift", "来源主键不满足受控写入身份约定")
	}
	for i, property := range properties {
		raw, err := mappedRawValue(property, values[i+1])
		if err != nil {
			return item, false, err
		}
		item.Values[property.ID] = raw
	}
	item, err = s.Stamp(item)
	return item, true, err
}

func (s *mappedMutationStore) Insert(ctx context.Context, item c.Instance) error {
	if err := s.checkCreateRelations(ctx, item); err != nil {
		return err
	}
	columns, marks, args := []string{s.table.quoted(s.table.keyColumn)}, []string{"?"}, []any{item.ID}
	for _, p := range sortedMappedProperties(s.current.Definition.Schema.Properties) {
		value, err := typedValue(p, item.Values[p.ID])
		if err != nil {
			return err
		}
		columns, marks, args = append(columns, s.table.quoted(p.Mapping.Column)), append(marks, "?"), append(args, value)
	}
	_, err := s.tx.ExecContext(ctx, "INSERT INTO "+s.table.qualified()+" ("+strings.Join(columns, ",")+") VALUES ("+strings.Join(marks, ",")+")", args...)
	return err
}

func (s *mappedMutationStore) Update(ctx context.Context, old c.Instance, next c.Instance, changed []string) error {
	if err := s.checkRelationChanges(ctx, changed); err != nil {
		return err
	}
	if len(changed) == 0 {
		return nil
	}
	sets, args := []string{}, []any{}
	for _, id := range changed {
		p, ok := findProperty(s.current.Definition.Schema, id)
		if !ok {
			return problem(422, "unknown_field", "修改包含未知属性")
		}
		value, err := typedValue(p, next.Values[p.ID])
		if err != nil {
			return err
		}
		sets, args = append(sets, s.table.quoted(p.Mapping.Column)+"=?"), append(args, value)
	}
	args = append(args, old.ID)
	_, err := s.tx.ExecContext(ctx, "UPDATE "+s.table.qualified()+" SET "+strings.Join(sets, ",")+" WHERE "+s.table.quoted(s.table.keyColumn)+"=?", args...)
	return err
}

func (s *mappedMutationStore) Delete(ctx context.Context, item c.Instance) error {
	if err := s.checkDeleteRelations(ctx); err != nil {
		return err
	}
	_, err := s.tx.ExecContext(ctx, "DELETE FROM "+s.table.qualified()+" WHERE "+s.table.quoted(s.table.keyColumn)+"=?", item.ID)
	return err
}

func (s *mappedMutationStore) Receipt(ctx context.Context, operation, key string) (hash, raw string, found bool, err error) {
	err = s.tx.QueryRowContext(ctx, "SELECT request_hash,response FROM af_source_write_receipts WHERE tenant_id=? AND schema_id=? AND subject_id=? AND operation=? AND request_key=? FOR UPDATE", s.p.TenantID, s.current.Definition.ID, s.p.Subject, operation, key).Scan(&hash, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return hash, raw, err == nil, err
}

func (s *mappedMutationStore) SaveReceipt(ctx context.Context, operation, key, hash, raw string) error {
	_, err := s.tx.ExecContext(ctx, "INSERT INTO af_source_write_receipts(tenant_id,schema_id,subject_id,operation,request_key,request_hash,response) VALUES(?,?,?,?,?,?,?)", s.p.TenantID, s.current.Definition.ID, s.p.Subject, operation, key, hash, raw)
	return err
}

func (s *mappedMutationStore) Emit(ctx context.Context, event string, item c.Instance) error {
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = s.tx.ExecContext(ctx, "INSERT INTO af_source_write_outbox(id,tenant_id,schema_id,entity_id,event_type,payload) VALUES(?,?,?,?,?,?)", c.NewID(), s.p.TenantID, s.current.Definition.ID, item.ID, event, string(raw))
	return err
}
