package data

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

type schemaCatalog interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func platformColumn(p c.Property, nullable bool) string {
	kind, _ := sqlType(p)
	if p.DataType == "uuid" {
		kind += " CHARACTER SET ascii COLLATE ascii_bin"
	} else if p.DataType == "string" || p.DataType == "text" || p.DataType == "longtext" {
		kind += " CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin"
	}
	if nullable {
		return quote(columnName(p.ID)) + " " + kind + " NULL"
	}
	return quote(columnName(p.ID)) + " " + kind + " NOT NULL"
}

func platformIndex(idx c.Index) string {
	kind := "INDEX "
	if idx.Unique {
		kind = "UNIQUE INDEX "
	}
	fields := []string{"`tenant_id`"}
	for _, id := range idx.FieldIDs {
		fields = append(fields, quote(columnName(id)))
	}
	return kind + quote(physical("ix_", idx.ID)) + " (" + strings.Join(fields, ",") + ")"
}

func platformAdditionPlan(old, next c.Definition) (c.SchemaAddition, []string, error) {
	addition, err := c.ValidateSchemaAddition(old, next)
	if err != nil {
		return addition, nil, problem(422, "unsupported_migration", err.Error())
	}
	if err = validateSchema(next); err != nil {
		return addition, nil, err
	}
	table := quote(tableName(next.ID))
	statements := []string{}
	sets := []string{}
	for _, p := range addition.Properties {
		statements = append(statements, "ALTER TABLE "+table+" ADD COLUMN "+platformColumn(p, true))
		sets = append(sets, quote(columnName(p.ID))+"=?")
	}
	if len(sets) > 0 {
		sets = append(sets, "`etag`=UUID()")
		statements = append(statements, "UPDATE "+table+" SET "+strings.Join(sets, ",")+" WHERE `tenant_id`=?")
	}
	for _, p := range addition.Properties {
		if !p.Nullable {
			statements = append(statements, "ALTER TABLE "+table+" MODIFY COLUMN "+platformColumn(p, false))
		}
	}
	for _, idx := range addition.Indexes {
		statements = append(statements, "ALTER TABLE "+table+" ADD "+platformIndex(idx))
	}
	return addition, statements, nil
}

// New columns have one declared default (or NULL) for every existing row. A
// NULL component cannot conflict under MySQL unique-index semantics; otherwise
// only existing components distinguish groups. No default is interpolated SQL.
func preflightPlatformIndexes(ctx context.Context, db schemaCatalog, old c.Definition, addition c.SchemaAddition) error {
	newFields := map[string]c.Property{}
	for _, p := range addition.Properties {
		newFields[p.ID] = p
	}
	for _, idx := range addition.Indexes {
		if !idx.Unique {
			continue
		}
		groups := []string{"`tenant_id`"}
		conditions := []string{"`tenant_id`=?"}
		canConflict := true
		for _, id := range idx.FieldIDs {
			if p, added := newFields[id]; added {
				if len(p.Default) == 0 || bytes.Equal(bytes.TrimSpace(p.Default), []byte("null")) {
					canConflict = false
				}
				continue
			}
			column := quote(columnName(id))
			groups = append(groups, column)
			conditions = append(conditions, column+" IS NOT NULL")
		}
		if !canConflict {
			continue
		}
		var exists int
		query := "SELECT 1 FROM " + quote(tableName(old.ID)) + " WHERE " + strings.Join(conditions, " AND ") + " GROUP BY " + strings.Join(groups, ",") + " HAVING COUNT(*)>1 LIMIT 1"
		err := db.QueryRowContext(ctx, query, old.TenantID).Scan(&exists)
		if err == nil {
			return problem(422, "migration_unique_conflict", "现有数据与新增唯一索引冲突，请先处理重复值")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

func (h *handler) preparePlatform(ctx context.Context, req c.PrepareRequest) (c.PrepareResult, error) {
	result := c.PrepareResult{OperationID: req.OperationID, Status: "prepared", Statements: []string{}}
	statement, err := ddl(req.Definition)
	if err != nil {
		return result, err
	}
	if err = h.checkBackend(ctx); err != nil {
		return result, err
	}
	initial, ready, migration, resumed := false, false, false, false
	signature := storageSignature(req.Definition)
	err = h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		var oldDigest, oldSignature, oldOperation, status, previous, raw string
		err := tx.QueryRowContext(ctx, "SELECT digest,storage_signature,operation_id,status,previous_digest,definition FROM af_data_gates WHERE tenant_id=? AND definition_id=? FOR UPDATE", req.Definition.TenantID, req.Definition.ID).Scan(&oldDigest, &oldSignature, &oldOperation, &status, &previous, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			if req.PreviousDigest != "" {
				return problem(409, "publication_conflict", "旧映射不存在")
			}
			var exists int
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", tableName(req.Definition.ID)).Scan(&exists); err != nil {
				return err
			}
			if exists != 0 {
				return problem(409, "unmanaged_table", "目标存在未纳管表，不能直接接管")
			}
			body, _ := json.Marshal(req.Definition)
			_, err = tx.ExecContext(ctx, "INSERT INTO af_data_gates (tenant_id,definition_id,digest,previous_digest,operation_id,status,definition,storage_signature) VALUES (?,?,?,?,?,'preparing',?,?)", req.Definition.TenantID, req.Definition.ID, req.Digest, req.PreviousDigest, req.OperationID, string(body), signature)
			initial = true
			return err
		}
		if err != nil {
			return err
		}
		if oldOperation == req.OperationID {
			resumed = true
			if oldDigest != req.Digest || oldSignature != signature || previous != req.PreviousDigest {
				return problem(409, "publication_conflict", "同一发布操作不能改变内容或原定义摘要")
			}
			ready, initial = status == "ready", req.PreviousDigest == ""
			var count int
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM af_data_schema_migrations WHERE tenant_id=? AND operation_id=?", req.Definition.TenantID, req.OperationID).Scan(&count); err != nil {
				return err
			}
			migration = count == 1
			return nil
		}
		if status != "ready" {
			return problem(409, "publication_busy", "其他发布操作尚未恢复")
		}
		if oldDigest != req.PreviousDigest {
			return problem(409, "publication_conflict", "当前映射摘要已改变")
		}
		if oldSignature != signature {
			var old c.Definition
			if json.Unmarshal([]byte(raw), &old) != nil || digest(old) != oldDigest {
				return problem(500, "mapping_corrupt", "无法核对原定义")
			}
			addition, _, err := platformAdditionPlan(old, req.Definition)
			if err != nil {
				return err
			}
			// The admission lock has drained old operations. All deterministic
			// rejection checks finish before the gate is durably closed.
			if err = verifyTable(ctx, tx, old); err != nil {
				return problem(422, "physical_schema_changed", "当前物理结构与原定义不符，不能开始迁移")
			}
			if err = preflightPlatformIndexes(ctx, tx, old, addition); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO af_data_schema_migrations (tenant_id,operation_id,definition_id,previous_definition,target_digest,backfilled,status) VALUES (?,?,?,?,?,0,'preparing')", req.Definition.TenantID, req.OperationID, req.Definition.ID, raw, req.Digest)
			if err != nil {
				return err
			}
			migration = true
		}
		body, _ := json.Marshal(req.Definition)
		_, err = tx.ExecContext(ctx, "UPDATE af_data_gates SET digest=?,previous_digest=?,operation_id=?,status='preparing',definition=?,storage_signature=? WHERE tenant_id=? AND definition_id=?", req.Digest, req.PreviousDigest, req.OperationID, string(body), signature, req.Definition.TenantID, req.Definition.ID)
		return err
	})
	if err != nil {
		if resumed {
			return result, problem(503, "publication_incomplete", "发布恢复尚未完成，请使用原请求重试")
		}
		return result, err
	}
	if ready {
		result.Status = "ready"
		return result, nil
	}
	if migration {
		statements, err := h.applyPlatformAddition(ctx, req)
		if err != nil {
			return result, problem(503, "migration_incomplete", "结构迁移未完成，当前模型保持暂停；请使用原请求恢复")
		}
		result.Statements = statements
		return result, nil
	}
	if initial {
		var exists int
		err = h.cfg.Engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", tableName(req.Definition.ID)).Scan(&exists)
		if err == nil && exists == 0 {
			_, err = h.cfg.Engine.DB().ExecContext(ctx, statement)
		}
		if err != nil {
			return result, problem(503, "publication_incomplete", "建表未完成，请使用原请求恢复")
		}
		result.Statements = []string{statement}
	}
	if err = verifyTable(ctx, h.cfg.Engine.DB(), req.Definition); err != nil {
		return result, problem(503, "publication_incomplete", "物理结构尚未完整验证，请使用原请求恢复")
	}
	_, err = h.cfg.Engine.DB().ExecContext(ctx, "UPDATE af_data_gates SET status='prepared' WHERE tenant_id=? AND definition_id=? AND operation_id=? AND digest=? AND status IN ('preparing','prepared')", req.Definition.TenantID, req.Definition.ID, req.OperationID, req.Digest)
	return result, err
}

func (h *handler) applyPlatformAddition(ctx context.Context, req c.PrepareRequest) ([]string, error) {
	conn, err := h.cfg.Engine.DB().Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var database string
	if err = conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil {
		return nil, err
	}
	lockName := "af_schema_" + digest([]string{database, req.Definition.TenantID, req.Definition.ID})[:48]
	var locked sql.NullInt64
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(?,0)", lockName).Scan(&locked); err != nil || !locked.Valid || locked.Int64 != 1 {
		return nil, fmt.Errorf("schema migration is already running")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var released sql.NullInt64
		if err := conn.QueryRowContext(cleanup, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released); err != nil || !released.Valid || released.Int64 != 1 {
			// An uncertain release must not return a session-level lock to
			// the idle pool. Closing the physical session releases its locks.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	var operation, currentDigest, status string
	if err = conn.QueryRowContext(ctx, "SELECT operation_id,digest,status FROM af_data_gates WHERE tenant_id=? AND definition_id=?", req.Definition.TenantID, req.Definition.ID).Scan(&operation, &currentDigest, &status); err != nil || operation != req.OperationID || currentDigest != req.Digest {
		return nil, fmt.Errorf("schema migration no longer owns admission")
	}
	var raw, target string
	var backfilled bool
	if err = conn.QueryRowContext(ctx, "SELECT previous_definition,target_digest,backfilled FROM af_data_schema_migrations WHERE tenant_id=? AND operation_id=? AND definition_id=?", req.Definition.TenantID, req.OperationID, req.Definition.ID).Scan(&raw, &target, &backfilled); err != nil || target != req.Digest {
		return nil, fmt.Errorf("migration evidence unavailable")
	}
	var old c.Definition
	if json.Unmarshal([]byte(raw), &old) != nil || digest(old) != req.PreviousDigest {
		return nil, fmt.Errorf("migration origin does not match")
	}
	addition, statements, err := platformAdditionPlan(old, req.Definition)
	if err != nil {
		return nil, err
	}
	columns, indexes, err := platformMigrationShape(ctx, conn, old, addition)
	if err != nil {
		return nil, err
	}
	table := quote(tableName(old.ID))
	for _, p := range addition.Properties {
		if _, exists := columns[columnName(p.ID)]; !exists {
			if _, err = conn.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+platformColumn(p, true)); err != nil {
				return nil, err
			}
		}
	}
	if !backfilled {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		sets, args := []string{}, []any{}
		for _, p := range addition.Properties {
			var value any
			if len(p.Default) > 0 {
				value, err = typedValue(p, p.Default)
				if err != nil {
					return nil, err
				}
			}
			sets, args = append(sets, quote(columnName(p.ID))+"=?"), append(args, value)
		}
		if len(sets) > 0 {
			sets = append(sets, "`etag`=UUID()")
			args = append(args, old.TenantID)
			if _, err = tx.ExecContext(ctx, "UPDATE "+table+" SET "+strings.Join(sets, ",")+" WHERE `tenant_id`=?", args...); err != nil {
				return nil, err
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE af_data_schema_migrations SET backfilled=1 WHERE tenant_id=? AND operation_id=?", old.TenantID, req.OperationID); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
	}
	for _, p := range addition.Properties {
		if !p.Nullable && columns[columnName(p.ID)] != "NO" {
			if _, err = conn.ExecContext(ctx, "ALTER TABLE "+table+" MODIFY COLUMN "+platformColumn(p, false)); err != nil {
				return nil, err
			}
		}
	}
	for _, idx := range addition.Indexes {
		if !indexes[physical("ix_", idx.ID)] {
			if _, err = conn.ExecContext(ctx, "ALTER TABLE "+table+" ADD "+platformIndex(idx)); err != nil {
				return nil, err
			}
		}
	}
	if err = verifyTable(ctx, conn, req.Definition); err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE af_data_schema_migrations SET status='prepared' WHERE tenant_id=? AND operation_id=?", old.TenantID, req.OperationID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE af_data_gates SET status='prepared' WHERE tenant_id=? AND definition_id=? AND operation_id=? AND digest=? AND status IN ('preparing','prepared')", old.TenantID, old.ID, req.OperationID, req.Digest); err != nil {
		return nil, err
	}
	return statements, tx.Commit()
}

// Verify any partially applied shape against the immutable original plus only
// the declared additions. Unknown columns/indexes or changed old columns stop
// recovery; existing objects are never silently adopted or overwritten.
func platformMigrationShape(ctx context.Context, db schemaCatalog, old c.Definition, addition c.SchemaAddition) (map[string]string, map[string]bool, error) {
	columns, indexes := map[string]string{}, map[string]bool{}
	rows, err := db.QueryContext(ctx, "SELECT column_name,is_nullable FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=?", tableName(old.ID))
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var name, nullable string
		if err = rows.Scan(&name, &nullable); err != nil {
			rows.Close()
			return nil, nil, err
		}
		columns[name] = nullable
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	rows, err = db.QueryContext(ctx, "SELECT DISTINCT index_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=?", tableName(old.ID))
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return nil, nil, err
		}
		indexes[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	partial := old
	schema := *old.Schema
	partial.Schema = &schema
	schema.Properties = append([]c.Property(nil), old.Schema.Properties...)
	schema.Indexes = append([]c.Index(nil), old.Schema.Indexes...)
	for _, p := range addition.Properties {
		if nullable, exists := columns[columnName(p.ID)]; exists {
			if p.Nullable && nullable != "YES" {
				return nil, nil, fmt.Errorf("unexpected added column nullability")
			}
			p.Nullable = nullable == "YES"
			schema.Properties = append(schema.Properties, p)
		}
	}
	for _, idx := range addition.Indexes {
		if indexes[physical("ix_", idx.ID)] {
			schema.Indexes = append(schema.Indexes, idx)
		}
	}
	if err = verifyTable(ctx, db, partial); err != nil {
		return nil, nil, err
	}
	return columns, indexes, nil
}
