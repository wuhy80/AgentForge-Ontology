package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// mutationStore owns exactly one local SQL transaction. Authorization, type
// validation and request semantics are shared by all storage implementations.
type mutationMatch struct {
	ID     string
	Index  *c.Index
	Values map[string]json.RawMessage
}

type mutationStore interface {
	Tx() *sql.Tx
	Lookup(context.Context, mutationMatch) (c.Instance, bool, error)
	Insert(context.Context, c.Instance) error
	Update(context.Context, c.Instance, c.Instance, []string) error
	Delete(context.Context, c.Instance) error
	Receipt(context.Context, string, string) (string, string, bool, error)
	SaveReceipt(context.Context, string, string, string, string) error
	Emit(context.Context, string, c.Instance) error
}

type platformMutationStore struct {
	tx      *sql.Tx
	p       c.Principal
	current c.CurrentDefinition
}

func (s *platformMutationStore) Tx() *sql.Tx { return s.tx }

func (h *handler) withMutationStore(ctx context.Context, p c.Principal, current c.CurrentDefinition, fn func(mutationStore) error) error {
	if current.Definition.Schema.StorageBinding != nil {
		return h.withMappedMutationStore(ctx, p, current, fn)
	}
	return h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		if err := h.gate(ctx, tx, p, current); err != nil {
			return err
		}
		return fn(&platformMutationStore{tx: tx, p: p, current: current})
	})
}

func (s *platformMutationStore) Lookup(ctx context.Context, match mutationMatch) (c.Instance, bool, error) {
	id := match.ID
	if match.Index != nil {
		conditions := []string{"`tenant_id`=?"}
		args := []any{s.p.TenantID}
		for _, fieldID := range match.Index.FieldIDs {
			property, _ := findProperty(s.current.Definition.Schema, fieldID)
			value, err := typedValue(property, match.Values[fieldID])
			if err != nil {
				return c.Instance{}, false, err
			}
			conditions = append(conditions, quote(columnName(fieldID))+" = "+parameter(property))
			args = append(args, value)
		}
		// Use the selected real unique key only. A conflict on another unique
		// index must never choose a different target as an upsert side effect.
		err := s.tx.QueryRowContext(ctx, "SELECT id FROM "+quote(tableName(s.current.Definition.ID))+" WHERE "+strings.Join(conditions, " AND ")+" FOR UPDATE", args...).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return c.Instance{}, false, nil
		}
		if err != nil {
			return c.Instance{}, false, err
		}
	}
	out, err := selectInstance(ctx, s.tx, s.current.Definition, s.p.TenantID, id, nil, true)
	var api *apiError
	if errors.As(err, &api) && api.status == 404 {
		return c.Instance{}, false, nil
	}
	return out, err == nil, err
}

func (s *platformMutationStore) Insert(ctx context.Context, out c.Instance) error {
	cols := []string{"`tenant_id`", "`id`", "`etag`"}
	marks := []string{"?", "?", "?"}
	args := []any{s.p.TenantID, out.ID, out.ETag}
	for _, property := range s.current.Definition.Schema.Properties {
		value, err := typedValue(property, out.Values[property.ID])
		if err != nil {
			return err
		}
		cols = append(cols, quote(columnName(property.ID)))
		marks = append(marks, "?")
		args = append(args, value)
	}
	_, err := s.tx.ExecContext(ctx, "INSERT INTO "+quote(tableName(out.SchemaID))+" ("+strings.Join(cols, ",")+") VALUES ("+strings.Join(marks, ",")+")", args...)
	return err
}

func (s *platformMutationStore) Update(ctx context.Context, old, next c.Instance, changed []string) error {
	sets := []string{"`etag`=?"}
	args := []any{next.ETag}
	for _, fieldID := range changed {
		property, _ := findProperty(s.current.Definition.Schema, fieldID)
		value, err := typedValue(property, next.Values[fieldID])
		if err != nil {
			return err
		}
		sets = append(sets, quote(columnName(fieldID))+"=?")
		args = append(args, value)
	}
	args = append(args, s.p.TenantID, old.ID, old.ETag)
	result, err := s.tx.ExecContext(ctx, "UPDATE "+quote(tableName(next.SchemaID))+" SET "+strings.Join(sets, ",")+" WHERE tenant_id=? AND id=? AND etag=?", args...)
	if err != nil {
		return err
	}
	return mutationAffected(result)
}

func mutationAffected(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return problem(412, "precondition_failed", "实例已被修改，请重新读取")
	}
	return nil
}

func (s *platformMutationStore) Delete(ctx context.Context, old c.Instance) error {
	result, err := s.tx.ExecContext(ctx, "DELETE FROM "+quote(tableName(old.SchemaID))+" WHERE tenant_id=? AND id=? AND etag=?", s.p.TenantID, old.ID, old.ETag)
	if err != nil {
		return err
	}
	return mutationAffected(result)
}

func (s *platformMutationStore) Receipt(ctx context.Context, operation, key string) (hash, raw string, found bool, err error) {
	if operation == "create" {
		// Preserve already-issued single-create idempotency keys and their
		// request hashes through the shared engine refactor.
		err = s.tx.QueryRowContext(ctx, "SELECT request_hash,response FROM af_data_idempotency WHERE tenant_id=? AND schema_id=? AND subject_id=? AND request_key=? FOR UPDATE", s.p.TenantID, s.current.Definition.ID, s.p.Subject, key).Scan(&hash, &raw)
		if err == nil {
			var instance c.Instance
			if json.Unmarshal([]byte(raw), &instance) != nil {
				return "", "", false, problem(500, "idempotency_corrupt", "无法核对原创建结果")
			}
			encoded, e := json.Marshal(mutationSaved{Items: []mutationSavedItem{{Instance: instance, Outcome: "created", Operation: "instance.create"}}})
			return hash, string(encoded), true, e
		}
	} else {
		err = s.tx.QueryRowContext(ctx, "SELECT request_hash,response FROM af_data_write_receipts WHERE tenant_id=? AND schema_id=? AND subject_id=? AND operation=? AND request_key=? FOR UPDATE", s.p.TenantID, s.current.Definition.ID, s.p.Subject, operation, key).Scan(&hash, &raw)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return hash, raw, err == nil, err
}

func (s *platformMutationStore) SaveReceipt(ctx context.Context, operation, key, hash, raw string) error {
	if operation == "create" {
		var saved mutationSaved
		if json.Unmarshal([]byte(raw), &saved) != nil || len(saved.Items) != 1 {
			return problem(500, "idempotency_corrupt", "无法保存创建结果")
		}
		instance := saved.Items[0].Instance
		body, err := json.Marshal(instance)
		if err != nil {
			return err
		}
		_, err = s.tx.ExecContext(ctx, "INSERT INTO af_data_idempotency (tenant_id,schema_id,subject_id,request_key,request_hash,entity_id,response) VALUES (?,?,?,?,?,?,?)", s.p.TenantID, s.current.Definition.ID, s.p.Subject, key, hash, instance.ID, string(body))
		return err
	}
	_, err := s.tx.ExecContext(ctx, "INSERT INTO af_data_write_receipts (tenant_id,schema_id,subject_id,operation,request_key,request_hash,response) VALUES (?,?,?,?,?,?,?)", s.p.TenantID, s.current.Definition.ID, s.p.Subject, operation, key, hash, raw)
	return err
}

func (s *platformMutationStore) Emit(ctx context.Context, event string, instance c.Instance) error {
	return emit(ctx, s.tx, s.p.TenantID, event, instance)
}
