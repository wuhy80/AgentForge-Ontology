package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func mappedTableOwner(ctx context.Context, table *mappedTable) (string, error) {
	var owner sql.NullString
	var err error
	if table.source.Driver == "mysql" {
		err = table.db.QueryRowContext(ctx, "SELECT table_comment FROM information_schema.tables WHERE table_schema=? AND table_name=?", table.namespace, table.name).Scan(&owner)
	} else {
		err = table.db.QueryRowContext(ctx, "SELECT obj_description(c.oid,'pg_class') FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND c.relkind IN ('r','p')", table.namespace, table.name).Scan(&owner)
	}
	return owner.String, err
}

func mappedKeyProperty(tables []*mappedTable) c.Property {
	primary := tables[0]
	for _, property := range primary.properties {
		if property.Mapping.Column == primary.keyColumn {
			property.Nullable = false
			return property
		}
	}
	if primary.managed {
		return c.Property{DataType: "uuid"}
	}
	if column, ok := primary.column(primary.keyColumn); ok {
		return c.Property{DataType: column.DataType, Precision: column.Precision, Scale: column.Scale}
	}
	return c.Property{DataType: "uuid"}
}

func mappedIndexTable(tables []*mappedTable, index c.Index) (*mappedTable, []string, error) {
	var target *mappedTable
	columns := []string{}
	for _, id := range index.FieldIDs {
		table, property, ok := mappedPropertyTable(tables, id)
		if !ok || target != nil && target != table {
			return nil, nil, problem(422, "cross_source_index", "索引的全部属性必须映射到同一来源表")
		}
		target = table
		columns = append(columns, property.Mapping.Column)
	}
	if target == nil {
		return nil, nil, problem(422, "invalid_index", "索引没有可用映射属性")
	}
	if target.tenantColumn != "" {
		columns = append([]string{target.tenantColumn}, columns...)
	}
	return target, columns, nil
}

func mappedColumnDDL(table *mappedTable, property c.Property, column string) (string, error) {
	typeName, err := mappedType(table.source.Driver, property)
	if err != nil {
		return "", err
	}
	nullable := " NOT NULL"
	if property.Nullable {
		nullable = " NULL"
	}
	return table.quoted(column) + " " + typeName + nullable, nil
}

func mappedManagedColumnMatches(table *mappedTable, property c.Property, actual c.SourceColumn) bool {
	normalize := func(value string) string {
		value = strings.ToLower(value)
		value = strings.ReplaceAll(value, "character varying", "varchar")
		return strings.ReplaceAll(value, "numeric(", "decimal(")
	}
	expected, err := mappedType(table.source.Driver, property)
	if err != nil {
		return false
	}
	if property.DataType == "uuid" && table.source.Driver == "mysql" {
		expected = "char(36)"
	}
	if property.DataType == "boolean" && table.source.Driver == "mysql" && normalize(actual.DatabaseType) == "tinyint" {
		actual.DataType = "boolean"
		actual.DatabaseType = "tinyint(1)"
	}
	return mappedColumnCompatible(property, actual, table.source.Driver) && property.Nullable == actual.Nullable && normalize(expected) == normalize(actual.DatabaseType)
}

// A plan contains only SQL compiled from validated mappings. No SQL supplied by
// the caller is accepted. Reverse discovery always returns an empty DDL plan.
func mappedTablePlan(ctx context.Context, d c.Definition, tables []*mappedTable, table *mappedTable) ([]string, error) {
	if d.Schema.Mode == "db_2_schema" {
		return []string{}, nil
	}
	if table.exists {
		owner, err := mappedTableOwner(ctx, table)
		if err != nil {
			return nil, err
		}
		if owner != mappedOwner(d) {
			return nil, problem(422, "unmanaged_table", "目标存在未纳管表，正向建模不能接管或覆盖；已有表请使用数据库反向建模")
		}
	}
	keyProperty := mappedKeyProperty(tables)
	keyProperty.Nullable = false
	if sourceKeyType(c.SourceColumn{DataType: keyProperty.DataType}) == "" {
		return nil, problem(422, "invalid_source_key", "主表稳定主键只支持整数、短文本或UUID")
	}
	columns := []string{}
	expected := map[string]bool{table.keyColumn: true}
	if table.tenantColumn != "" {
		expected[table.tenantColumn] = true
		columns = append(columns, table.quoted(table.tenantColumn)+" varchar(128) NOT NULL")
	}
	keyDDL, err := mappedColumnDDL(table, keyProperty, table.keyColumn)
	if err != nil {
		return nil, err
	}
	columns = append(columns, keyDDL)
	statements := []string{}
	for _, property := range sortedMappedProperties(table.properties) {
		column := property.Mapping.Column
		if expected[column] {
			if column != table.keyColumn {
				return nil, problem(422, "reserved_column", "属性映射与保留隔离列冲突")
			}
			if sourceKeyType(c.SourceColumn{DataType: property.DataType}) != sourceKeyType(c.SourceColumn{DataType: keyProperty.DataType}) {
				return nil, problem(422, "incompatible_source_key", "来源关联键属性必须与主表稳定主键类型一致")
			}
			continue
		}
		expected[column] = true
		ddl, err := mappedColumnDDL(table, property, column)
		if err != nil {
			return nil, err
		}
		columns = append(columns, ddl)
		if table.exists {
			actual, exists := table.column(column)
			if exists {
				if !mappedManagedColumnMatches(table, property, actual) {
					return nil, problem(422, "source_drift", "已纳管目标列与当前定义不一致，不能自动改类型或约束")
				}
			} else {
				if !property.Nullable {
					return nil, problem(422, "unsafe_migration", "已有目标表仅允许安全追加可空列")
				}
				statements = append(statements, "ALTER TABLE "+table.qualified()+" ADD COLUMN "+ddl)
			}
		}
	}
	keyColumns := []string{table.keyColumn}
	if table.tenantColumn != "" {
		keyColumns = []string{table.tenantColumn, table.keyColumn}
	}
	quotedKeys := []string{}
	for _, column := range keyColumns {
		quotedKeys = append(quotedKeys, table.quoted(column))
	}
	columns = append(columns, "PRIMARY KEY ("+strings.Join(quotedKeys, ",")+")")
	if table.exists {
		for _, actual := range table.catalog.Columns {
			if !expected[actual.Name] {
				return nil, problem(422, "source_drift", "目标表出现未登记列；请核对漂移，平台不会静默删除列")
			}
		}
		key, exists := table.column(table.keyColumn)
		if !exists || key.Nullable || !mappedManagedColumnMatches(table, keyProperty, key) || !mappedUniqueKey(table) {
			return nil, problem(422, "source_drift", "已纳管目标的主键结构不一致")
		}
		if table.tenantColumn != "" {
			tenant, exists := table.column(table.tenantColumn)
			if !exists || tenant.Nullable || tenant.DataType != "string" {
				return nil, problem(422, "source_drift", "已纳管目标的租户隔离列不一致")
			}
		}
	} else {
		create := "CREATE TABLE " + table.qualified() + " (" + strings.Join(columns, ",") + ")"
		if table.source.Driver == "mysql" {
			create += " ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin COMMENT='" + mappedOwner(d) + "'"
		}
		statements = append(statements, create)
		if table.source.Driver == "postgresql" {
			statements = append(statements, "COMMENT ON TABLE "+table.qualified()+" IS '"+mappedOwner(d)+"'")
		}
	}
	expectedIndexes := map[string]bool{}
	for _, index := range d.Schema.Indexes {
		target, fields, err := mappedIndexTable(tables, index)
		if err != nil {
			return nil, err
		}
		if target != table {
			continue
		}
		name := physical("ix_", index.ID)
		expectedIndexes[name] = true
		found := false
		for _, actual := range table.catalog.Indexes {
			if actual.Name == name {
				if actual.Unique != index.Unique || !reflect.DeepEqual(actual.Columns, fields) {
					return nil, problem(422, "source_drift", "已纳管目标索引与当前定义不一致")
				}
				found = true
			}
		}
		if !found {
			quoted := []string{}
			for _, field := range fields {
				quoted = append(quoted, table.quoted(field))
			}
			unique := ""
			if index.Unique {
				unique = "UNIQUE "
			}
			statements = append(statements, "CREATE "+unique+"INDEX "+table.quoted(name)+" ON "+table.qualified()+" ("+strings.Join(quoted, ",")+")")
		}
	}
	if table.exists {
		for _, actual := range table.catalog.Indexes {
			if !actual.Primary && !expectedIndexes[actual.Name] {
				return nil, problem(422, "source_drift", "目标表出现未登记索引，请先核对结构漂移")
			}
		}
	}
	return statements, nil
}

func validateReverseIndexes(d c.Definition, tables []*mappedTable) error {
	for _, index := range d.Schema.Indexes {
		table, fields, err := mappedIndexTable(tables, index)
		if err != nil {
			return err
		}
		matched := false
		for _, actual := range table.catalog.Indexes {
			if actual.Predicate == "" && actual.Unique == index.Unique && reflect.DeepEqual(actual.Columns, fields) {
				matched = true
				break
			}
		}
		if !matched {
			return problem(409, "source_drift", "反向映射索引未在来源表中找到相同字段及唯一约束")
		}
	}
	return nil
}

func (h *handler) previewMapped(ctx context.Context, req c.PrepareRequest) (c.PrepareResult, error) {
	result := c.PrepareResult{OperationID: req.OperationID, Status: "preview", Statements: []string{}}
	var raw, status string
	err := h.cfg.Engine.DB().QueryRowContext(ctx, "SELECT definition,status FROM af_data_gates WHERE tenant_id=? AND definition_id=?", req.Definition.TenantID, req.Definition.ID).Scan(&raw, &status)
	if err == nil {
		if status != "ready" {
			return result, problem(409, "publication_busy", "映射正在生效，请先恢复原操作")
		}
		var old c.Definition
		if json.Unmarshal([]byte(raw), &old) != nil {
			return result, problem(500, "mapping_corrupt", "无法读取当前映射")
		}
		if err := mappedCompatibleUpdate(old, req.Definition); err != nil {
			return result, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	tables, err := h.mappedTables(ctx, req.Definition, req.Definition.Schema.Mode == "schema_2_db")
	if err != nil {
		return result, err
	}
	defer closeMappedTables(tables)
	if req.Definition.Schema.Mode == "db_2_schema" {
		if err = validateMappedCatalog(tables); err != nil {
			return result, err
		}
		if err = validateReverseIndexes(req.Definition, tables); err != nil {
			return result, err
		}
	} else {
		for _, table := range tables {
			plan, err := mappedTablePlan(ctx, req.Definition, tables, table)
			if err != nil {
				return result, err
			}
			result.Statements = append(result.Statements, plan...)
		}
	}
	return result, nil
}

func (h *handler) prepareMapped(ctx context.Context, req c.PrepareRequest) (result c.PrepareResult, failure error) {
	result = c.PrepareResult{OperationID: req.OperationID, Status: "prepared", Statements: []string{}}
	closed := false
	var recordedOperation, recordedStatus string
	err := h.cfg.Engine.DB().QueryRowContext(ctx, "SELECT operation_id,status FROM af_data_gates WHERE tenant_id=? AND definition_id=?", req.Definition.TenantID, req.Definition.ID).Scan(&recordedOperation, &recordedStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	closed = recordedOperation == req.OperationID && recordedStatus != "ready"
	defer func() {
		if failure != nil && closed {
			failure = fmt.Errorf("mapped preparation requires recovery: %s", failure.Error())
		} else if failure != nil {
			var invalid *apiError
			if errors.As(failure, &invalid) && (invalid.status == 403 || invalid.status == 404 || invalid.status == 409 && invalid.code != "publication_conflict" && invalid.code != "publication_busy") {
				// A rejected draft must remain editable. Metadata can release a
				// newly started publication only for a 400/422 preflight rejection.
				failure = problem(422, invalid.code, invalid.message)
			}
		}
	}()
	tables, err := h.mappedTables(ctx, req.Definition, req.Definition.Schema.Mode == "schema_2_db")
	if err != nil {
		return result, err
	}
	defer closeMappedTables(tables)
	if req.Definition.Schema.Mode == "db_2_schema" {
		if err = validateMappedCatalog(tables); err != nil {
			return result, err
		}
		if err = validateReverseIndexes(req.Definition, tables); err != nil {
			return result, err
		}
	} else {
		for _, table := range tables {
			if _, err = mappedTablePlan(ctx, req.Definition, tables, table); err != nil {
				return result, err
			}
		}
	}
	sourceClosed, err := h.blockMappedWriteGate(ctx, req, tables)
	if err != nil {
		return result, err
	}
	closed = closed || sourceClosed
	alreadyReady := false
	err = h.cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		// Source updates acquire the same locks before checking references. Once
		// a gate is recorded they cannot redirect its stable source identities.
		sourceIDs := map[string]string{}
		for _, table := range tables {
			sourceIDs[table.source.ID] = table.source.ETag
		}
		ordered := []string{}
		for id := range sourceIDs {
			ordered = append(ordered, id)
		}
		sort.Strings(ordered)
		for _, id := range ordered {
			var etag string
			if err := tx.QueryRowContext(ctx, "SELECT etag FROM af_data_sources WHERE tenant_id=? AND id=? FOR UPDATE", req.Definition.TenantID, id).Scan(&etag); err != nil {
				return err
			}
			if etag != sourceIDs[id] {
				return problem(409, "source_changed", "数据源配置已改变，请重新预览并发布")
			}
		}
		if req.Definition.Schema.Mode == "schema_2_db" {
			orderedTargets := []string{}
			for _, table := range tables {
				namespace, name := table.namespace, table.name
				if table.source.Driver == "mysql" {
					namespace, name = strings.ToLower(namespace), strings.ToLower(name)
				}
				key := digest([]string{endpointKey(table.source), namespace, name})
				orderedTargets = append(orderedTargets, key)
			}
			sort.Strings(orderedTargets)
			for _, key := range orderedTargets {
				if _, err := tx.ExecContext(ctx, "INSERT INTO af_data_mapped_targets (target_key,tenant_id,definition_id) VALUES (?,?,?) ON DUPLICATE KEY UPDATE target_key=target_key", key, req.Definition.TenantID, req.Definition.ID); err != nil {
					return err
				}
				var tenant, definition string
				if err := tx.QueryRowContext(ctx, "SELECT tenant_id,definition_id FROM af_data_mapped_targets WHERE target_key=? FOR UPDATE", key).Scan(&tenant, &definition); err != nil {
					return err
				}
				if tenant != req.Definition.TenantID || definition != req.Definition.ID {
					return problem(422, "target_owned", "目标物理表已由另一模型纳管或预留，请选择不同表名")
				}
			}
		}
		var operation, currentDigest, raw, status string
		err := tx.QueryRowContext(ctx, "SELECT operation_id,digest,definition,status FROM af_data_gates WHERE tenant_id=? AND definition_id=? FOR UPDATE", req.Definition.TenantID, req.Definition.ID).Scan(&operation, &currentDigest, &raw, &status)
		if errors.Is(err, sql.ErrNoRows) {
			if req.PreviousDigest != "" {
				return problem(409, "publication_conflict", "旧映射不存在")
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO af_data_gates (tenant_id,definition_id,digest,previous_digest,operation_id,status,definition,storage_signature) VALUES (?,?,?,?,?,'preparing',?,?)", req.Definition.TenantID, req.Definition.ID, req.Digest, req.PreviousDigest, req.OperationID, mappedDefinitionJSON(req.Definition), storageSignature(req.Definition))
		} else if err == nil {
			if operation == req.OperationID {
				if currentDigest != req.Digest || raw != mappedDefinitionJSON(req.Definition) {
					return problem(409, "publication_conflict", "相同发布操作不能改变映射内容")
				}
				alreadyReady = status == "ready"
			} else {
				if status != "ready" || currentDigest != req.PreviousDigest {
					return problem(409, "publication_conflict", "当前映射摘要已改变或其他操作尚未恢复")
				}
				var previous c.Definition
				if json.Unmarshal([]byte(raw), &previous) != nil {
					return problem(500, "mapping_corrupt", "当前映射无法核对")
				}
				if err = mappedCompatibleUpdate(previous, req.Definition); err != nil {
					return err
				}
				_, err = tx.ExecContext(ctx, "UPDATE af_data_gates SET digest=?,previous_digest=?,operation_id=?,status='preparing',definition=?,storage_signature=? WHERE tenant_id=? AND definition_id=?", req.Digest, req.PreviousDigest, req.OperationID, mappedDefinitionJSON(req.Definition), storageSignature(req.Definition), req.Definition.TenantID, req.Definition.ID)
			}
		}
		if err != nil {
			return err
		}
		for _, table := range tables {
			if _, err = tx.ExecContext(ctx, "INSERT IGNORE INTO af_data_mapped_steps (tenant_id,operation_id,table_key,definition_id,status) VALUES (?,?,?,?,'pending')", req.Definition.TenantID, req.OperationID, digest(table.locator()), req.Definition.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	closed = true
	if alreadyReady {
		result.Status = "ready"
		return result, nil
	}
	// Every failure after closing admission is recoverable and must remain 5xx,
	// including catalog changes: Metadata must not release the operation guard.
	for _, table := range tables {
		if req.Definition.Schema.Mode == "schema_2_db" {
			catalog, err := readSourceTable(ctx, table.db, table.source, table.namespace, table.name)
			if err == nil {
				table.catalog, table.exists = catalog, true
			} else {
				var failure *apiError
				if !errors.As(err, &failure) || failure.status != 404 {
					return result, fmt.Errorf("resume source table: %w", err)
				}
				table.exists = false
			}
			plan, err := mappedTablePlan(ctx, req.Definition, tables, table)
			if err != nil {
				return result, fmt.Errorf("resume mapped plan: %w", err)
			}
			if table.source.Driver == "postgresql" {
				tx, err := table.db.BeginTx(ctx, nil)
				if err != nil {
					return result, fmt.Errorf("begin source migration: %w", err)
				}
				for _, statement := range plan {
					if _, err = tx.ExecContext(ctx, statement); err != nil {
						_ = tx.Rollback()
						return result, fmt.Errorf("apply source migration: %w", err)
					}
				}
				if err = tx.Commit(); err != nil {
					return result, fmt.Errorf("commit source migration: %w", err)
				}
			} else {
				for _, statement := range plan {
					if _, err = table.db.ExecContext(ctx, statement); err != nil {
						return result, fmt.Errorf("apply source migration: %w", err)
					}
				}
			}
			result.Statements = append(result.Statements, plan...)
		}
		catalog, err := readSourceTable(ctx, table.db, table.source, table.namespace, table.name)
		if err != nil {
			return result, fmt.Errorf("verify source table: %w", err)
		}
		table.catalog, table.exists = catalog, true
		if req.Definition.Schema.Mode == "schema_2_db" {
			remaining, err := mappedTablePlan(ctx, req.Definition, tables, table)
			if err != nil || len(remaining) != 0 {
				return result, sourceStorageError("来源迁移尚未完整验证，请恢复同一发布")
			}
		}
		if _, err = h.cfg.Engine.DB().ExecContext(ctx, "UPDATE af_data_mapped_steps SET status='prepared' WHERE tenant_id=? AND operation_id=? AND table_key=?", req.Definition.TenantID, req.OperationID, digest(table.locator())); err != nil {
			return result, fmt.Errorf("record mapped progress: %w", err)
		}
	}
	if err = validateMappedCatalog(tables); err != nil {
		return result, fmt.Errorf("verify mapped catalogs: %w", err)
	}
	if err = validateReverseIndexes(req.Definition, tables); err != nil && req.Definition.Schema.Mode == "db_2_schema" {
		return result, fmt.Errorf("verify mapped indexes: %w", err)
	}
	_, err = h.cfg.Engine.DB().ExecContext(ctx, "UPDATE af_data_gates SET status='prepared' WHERE tenant_id=? AND definition_id=? AND operation_id=? AND digest=? AND status IN ('preparing','prepared')", req.Definition.TenantID, req.Definition.ID, req.OperationID, req.Digest)
	if err != nil && closed {
		return result, fmt.Errorf("record mapped prepare: %w", err)
	}
	return result, err
}
