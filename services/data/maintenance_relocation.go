package data

// This file provides an explicit offline maintenance operation. None of these
// functions is registered as an HTTP endpoint or called during server startup.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const relocationMaxRows = 10000
const relocationMaxBytes = 64 << 20

type relocationRow struct {
	ID     string                     `json:"id"`
	Values map[string]json.RawMessage `json:"values"`
	native []any
}

type relocationPlan struct {
	item       c.StorageRelocation
	manifest   string
	table      *mappedTable
	rows       []relocationRow
	statements []string
	resumed    bool
	ready      bool
	gateStatus string
}

type relocationQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// PlanDefaultStorageRelocation validates the complete fixed manifest and reads
// bounded source snapshots. It does not close gates, claim targets or run DDL.
func PlanDefaultStorageRelocation(ctx context.Context, cfg Config, items []c.StorageRelocation) ([]c.StorageRelocationResult, error) {
	plans, err := planDefaultRelocations(ctx, cfg, items)
	defer closeRelocationPlans(plans)
	if err != nil {
		return nil, err
	}
	return relocationResults(plans), nil
}

// PrepareDefaultStorageRelocation closes the entire batch before changing any
// target. Source tables are never altered. A failure after closure leaves all
// gates closed and the same manifest can be resumed without replacing rows.
func PrepareDefaultStorageRelocation(ctx context.Context, cfg Config, items []c.StorageRelocation) ([]c.StorageRelocationResult, error) {
	plans, err := planDefaultRelocations(ctx, cfg, items)
	defer closeRelocationPlans(plans)
	if err != nil {
		return nil, err
	}
	ready := 0
	for _, plan := range plans {
		if plan.ready {
			ready++
		}
	}
	if ready == len(plans) {
		return relocationResults(plans), nil
	}
	if ready != 0 {
		return nil, fmt.Errorf("relocation batch has mixed activated and inactive gates")
	}
	if err = closeRelocationGates(ctx, cfg, plans); err != nil {
		return nil, err
	}
	// A default writer admitted before gate closure may have committed after the
	// initial plan. Read again only after all admitted transactions have drained.
	for _, plan := range plans {
		plan.rows, err = readDefaultRelocationRows(ctx, cfg.Engine.DB(), plan.item.Previous.Definition)
		if err != nil {
			return nil, fmt.Errorf("relocation remains paused: %w", err)
		}
		if err := checkRelocationBatchBudget(plans); err != nil {
			return nil, fmt.Errorf("relocation remains paused: %w", err)
		}
	}
	for _, plan := range plans {
		if err = prepareRelocationTarget(ctx, cfg, plan); err != nil {
			return nil, fmt.Errorf("relocation remains paused: %w", err)
		}
	}
	err = cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		for _, plan := range orderedRelocationPlans(plans) {
			if err := checkRelocationGate(ctx, tx, plan.item, true); err != nil {
				return err
			}
		}
		for _, plan := range plans {
			_, err := tx.ExecContext(ctx, "UPDATE af_data_gates SET status='prepared' WHERE tenant_id=? AND definition_id=? AND operation_id=? AND digest=? AND status IN ('preparing','prepared')", plan.item.Definition.TenantID, plan.item.Definition.ID, plan.item.OperationID, plan.item.Digest)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("relocation remains paused: %w", err)
	}
	return relocationResults(plans), nil
}

// ActivateDefaultStorageRelocation requires the complete prepared batch. The
// coordinator must first commit and verify Metadata's matching current entries.
// All physical data is checked once more before the gates become ready together.
func ActivateDefaultStorageRelocation(ctx context.Context, cfg Config, items []c.StorageRelocation) error {
	plans, err := planDefaultRelocations(ctx, cfg, items)
	defer closeRelocationPlans(plans)
	if err != nil {
		return err
	}
	for _, plan := range plans {
		if !plan.resumed || len(plan.statements) != 0 || !plan.table.exists {
			return fmt.Errorf("relocation target is not completely prepared")
		}
	}
	return cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		for _, plan := range orderedRelocationPlans(plans) {
			if err := checkRelocationGate(ctx, tx, plan.item, true); err != nil {
				return err
			}
			var status string
			if err := tx.QueryRowContext(ctx, "SELECT status FROM af_data_gates WHERE tenant_id=? AND definition_id=?", plan.item.Definition.TenantID, plan.item.Definition.ID).Scan(&status); err != nil {
				return err
			}
			if status != "prepared" && status != "ready" {
				return fmt.Errorf("relocation gate is not prepared")
			}
		}
		for _, plan := range plans {
			if _, err := tx.ExecContext(ctx, "UPDATE af_data_gates SET status='ready' WHERE tenant_id=? AND definition_id=? AND operation_id=? AND digest=?", plan.item.Definition.TenantID, plan.item.Definition.ID, plan.item.OperationID, plan.item.Digest); err != nil {
				return err
			}
		}
		return nil
	})
}

func validateRelocationManifest(items []c.StorageRelocation) error {
	if len(items) == 0 || len(items) > 16 {
		return fmt.Errorf("relocation requires 1 to 16 models")
	}
	ids, operations, targets := map[string]bool{}, map[string]bool{}, map[string]bool{}
	tenant, source := "", ""
	for _, item := range items {
		before, after := item.Previous.Definition, item.Definition
		if !uuidPattern.MatchString(item.OperationID) || item.SourceETag == "" || operations[item.OperationID] || ids[after.ID] || item.Previous.Status != "active" || item.Previous.ETag != digest(before) || item.Digest != digest(after) {
			return fmt.Errorf("relocation identity, operation or digest is invalid")
		}
		if before.Schema == nil || before.Schema.StorageBinding != nil || validateSchema(before) != nil || after.Schema == nil || after.Schema.Mode != "schema_2_db" || after.Schema.StorageLayout != "single" || validateMappedDefinition(after) != nil {
			return fmt.Errorf("relocation only supports default single MySQL schemas to explicit schema_2_db mappings")
		}
		binding := after.Schema.StorageBinding
		if binding.IDColumn != "id" || binding.TenantColumn != "" {
			return fmt.Errorf("relocation requires a dedicated target with the stable id primary key")
		}
		if tenant == "" {
			tenant, source = after.TenantID, binding.DataSourceID
		}
		if tenant != after.TenantID || source != binding.DataSourceID {
			return fmt.Errorf("one relocation batch must use one tenant and one data source")
		}
		raw, _ := json.Marshal(after)
		var logical c.Definition
		_ = json.Unmarshal(raw, &logical)
		logical.Schema.StorageBinding = nil
		for i, property := range logical.Schema.Properties {
			mapping := property.Mapping
			if mapping.DataSourceID != source || mapping.Table != "" && mapping.Table != binding.Table || mapping.Namespace != "" && mapping.Namespace != binding.Namespace || mapping.KeyColumn != "" && mapping.KeyColumn != "id" || mapping.TenantColumn != "" || mapping.Column == "id" {
				return fmt.Errorf("relocation properties must map only to their dedicated primary table")
			}
			logical.Schema.Properties[i].Mapping = nil
		}
		if !reflect.DeepEqual(before, logical) {
			return fmt.Errorf("relocation cannot change logical definitions, identifiers or indexes")
		}
		target := strings.ToLower(binding.Namespace + "\x00" + binding.Table)
		if targets[target] {
			return fmt.Errorf("relocation targets must be distinct")
		}
		ids[after.ID], operations[item.OperationID], targets[target] = true, true, true
	}
	return nil
}

func planDefaultRelocations(ctx context.Context, cfg Config, items []c.StorageRelocation) (plans []*relocationPlan, failure error) {
	if cfg.Engine == nil {
		return nil, fmt.Errorf("relocation requires a configured Data engine")
	}
	if err := validateRelocationManifest(items); err != nil {
		return nil, err
	}
	h := &handler{cfg: cfg, client: cfg.HTTPClient}
	if h.client == nil {
		h.client = http.DefaultClient
	}
	if err := h.checkBackend(ctx); err != nil {
		return nil, err
	}
	manifestItems := append([]c.StorageRelocation(nil), items...)
	sort.Slice(manifestItems, func(i, j int) bool { return manifestItems[i].Definition.ID < manifestItems[j].Definition.ID })
	manifest := digest(manifestItems)
	for _, item := range items {
		plan := &relocationPlan{item: item, manifest: manifest}
		plans = append(plans, plan)
		if err := checkRelocationGate(ctx, cfg.Engine.DB(), item, false); err != nil {
			return plans, err
		}
		var operation, status string
		if err := cfg.Engine.DB().QueryRowContext(ctx, "SELECT operation_id,status FROM af_data_gates WHERE tenant_id=? AND definition_id=?", item.Definition.TenantID, item.Definition.ID).Scan(&operation, &status); err != nil {
			return plans, err
		}
		plan.resumed, plan.ready = operation == item.OperationID, operation == item.OperationID && status == "ready"
		plan.gateStatus = status
		if err := verifyTable(ctx, cfg.Engine.DB(), item.Previous.Definition); err != nil {
			return plans, err
		}
		rows, err := readDefaultRelocationRows(ctx, cfg.Engine.DB(), item.Previous.Definition)
		if err != nil {
			return plans, err
		}
		plan.rows = rows
		if err := checkRelocationBatchBudget(plans); err != nil {
			return plans, err
		}
		tables, err := h.mappedTables(ctx, item.Definition, true)
		if err != nil {
			return plans, err
		}
		if len(tables) != 1 || tables[0].source.Driver != "mysql" || tables[0].source.TenantIsolation != "dedicated" {
			closeMappedTables(tables)
			return plans, fmt.Errorf("relocation target must be one dedicated MySQL table")
		}
		plan.table = tables[0]
		if plan.table.source.ETag != item.SourceETag {
			return plans, fmt.Errorf("relocation data source changed after the manifest was created")
		}
		if plan.resumed {
			if err := checkRelocationTargetClaim(ctx, cfg.Engine.DB(), plan); err != nil {
				return plans, fmt.Errorf("relocation manifest or its persisted target claim changed")
			}
		} else if plan.table.exists {
			return plans, fmt.Errorf("relocation refuses to adopt an existing target table")
		}
		plan.statements, err = mappedTablePlan(ctx, item.Definition, tables, plan.table)
		if err != nil {
			return plans, err
		}
		if plan.table.exists {
			targetRows, err := readTargetRelocationRows(ctx, plan.table.db, plan, false)
			if err != nil {
				return plans, err
			}
			if len(targetRows) != 0 && digest(targetRows) != digest(plan.rows) {
				return plans, fmt.Errorf("relocation target contents differ; existing rows will not be overwritten")
			}
			if (status == "prepared" || plan.ready) && digest(targetRows) != digest(plan.rows) {
				return plans, fmt.Errorf("prepared relocation target no longer matches its original source")
			}
		}
	}
	if err := rejectRelocationManagedLinks(ctx, cfg.Engine.DB(), plans); err != nil {
		return plans, err
	}
	return plans, nil
}

type relocationGateReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func checkRelocationGate(ctx context.Context, reader relocationGateReader, item c.StorageRelocation, lock bool) error {
	var actual, previous, operation, status, raw, signature string
	statement := "SELECT digest,previous_digest,operation_id,status,definition,storage_signature FROM af_data_gates WHERE tenant_id=? AND definition_id=?"
	if lock {
		statement += " FOR UPDATE"
	}
	if err := reader.QueryRowContext(ctx, statement, item.Definition.TenantID, item.Definition.ID).Scan(&actual, &previous, &operation, &status, &raw, &signature); err != nil {
		return err
	}
	if operation == item.OperationID {
		if actual != item.Digest || previous != item.Previous.ETag || raw != mappedDefinitionJSON(item.Definition) || signature != storageSignature(item.Definition) || status != "preparing" && status != "prepared" && status != "ready" {
			return fmt.Errorf("relocation manifest does not match its persisted operation")
		}
		return nil
	}
	if actual != item.Previous.ETag || raw != mappedDefinitionJSON(item.Previous.Definition) || signature != storageSignature(item.Previous.Definition) || status != "ready" {
		return fmt.Errorf("relocation source gate changed or another operation is incomplete")
	}
	return nil
}

func closeRelocationGates(ctx context.Context, cfg Config, plans []*relocationPlan) error {
	return cfg.Engine.WithTx(ctx, func(tx *sql.Tx) error {
		var sourceETag string
		first := plans[0]
		if err := tx.QueryRowContext(ctx, "SELECT etag FROM af_data_sources WHERE tenant_id=? AND id=? FOR UPDATE", first.item.Definition.TenantID, first.table.source.ID).Scan(&sourceETag); err != nil {
			return err
		}
		for _, plan := range orderedRelocationPlans(plans) {
			if sourceETag != plan.table.source.ETag {
				return fmt.Errorf("relocation data source changed after planning")
			}
			if err := checkRelocationGate(ctx, tx, plan.item, true); err != nil {
				return err
			}
			key := relocationTargetKey(plan.table)
			if _, err := tx.ExecContext(ctx, "INSERT INTO af_data_mapped_targets (target_key,tenant_id,definition_id) VALUES (?,?,?) ON DUPLICATE KEY UPDATE target_key=target_key", key, plan.item.Definition.TenantID, plan.item.Definition.ID); err != nil {
				return err
			}
			var ownerTenant, ownerDefinition string
			if err := tx.QueryRowContext(ctx, "SELECT tenant_id,definition_id FROM af_data_mapped_targets WHERE target_key=? FOR UPDATE", key).Scan(&ownerTenant, &ownerDefinition); err != nil {
				return err
			}
			if ownerTenant != plan.item.Definition.TenantID || ownerDefinition != plan.item.Definition.ID {
				return fmt.Errorf("relocation target is already owned by another model")
			}
			if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO af_data_mapped_steps (tenant_id,operation_id,table_key,definition_id,status) VALUES (?,?,?,?,'pending')", plan.item.Definition.TenantID, plan.item.OperationID, relocationStepKey(plan), plan.item.Definition.ID); err != nil {
				return err
			}
		}
		if err := rejectRelocationManagedLinks(ctx, tx, plans); err != nil {
			return err
		}
		for _, plan := range plans {
			if _, err := tx.ExecContext(ctx, "UPDATE af_data_gates SET digest=?,previous_digest=?,operation_id=?,status='preparing',definition=?,storage_signature=? WHERE tenant_id=? AND definition_id=?", plan.item.Digest, plan.item.Previous.ETag, plan.item.OperationID, mappedDefinitionJSON(plan.item.Definition), storageSignature(plan.item.Definition), plan.item.Definition.TenantID, plan.item.Definition.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

func rejectRelocationManagedLinks(ctx context.Context, reader relocationQueryer, plans []*relocationPlan) error {
	ids := map[string]bool{}
	for _, plan := range plans {
		ids[plan.item.Definition.ID] = true
	}
	rows, err := reader.QueryContext(ctx, "SELECT definition FROM af_data_gates WHERE tenant_id=? AND JSON_EXTRACT(definition,'$.link') IS NOT NULL", plans[0].item.Definition.TenantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var definition c.Definition
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		if json.Unmarshal([]byte(raw), &definition) != nil || definition.Link == nil {
			return fmt.Errorf("cannot verify existing Link mappings")
		}
		link := definition.Link
		if link.StorageBinding != nil && link.StorageBinding.Mode == "managed" && (ids[link.SourceSchemaID] || ids[link.TargetSchemaID]) {
			return fmt.Errorf("default managed Link must be migrated before moving either endpoint")
		}
	}
	return rows.Err()
}

func relocationTargetKey(table *mappedTable) string {
	return digest([]string{endpointKey(table.source), strings.ToLower(table.namespace), strings.ToLower(table.name)})
}

// Bind every durable step to the complete batch, including SourceETag. Retrying
// with only a subset must not open part of a batch that was closed together.
func relocationStepKey(plan *relocationPlan) string {
	return digest([]string{"storage-relocation", plan.manifest, plan.table.locator()})
}

func checkRelocationTargetClaim(ctx context.Context, reader relocationGateReader, plan *relocationPlan) error {
	var tenant, definition, step string
	if err := reader.QueryRowContext(ctx, "SELECT tenant_id,definition_id FROM af_data_mapped_targets WHERE target_key=?", relocationTargetKey(plan.table)).Scan(&tenant, &definition); err != nil {
		return err
	}
	if tenant != plan.item.Definition.TenantID || definition != plan.item.Definition.ID {
		return fmt.Errorf("relocation target ownership changed")
	}
	if err := reader.QueryRowContext(ctx, "SELECT status FROM af_data_mapped_steps WHERE tenant_id=? AND operation_id=? AND table_key=? AND definition_id=?", tenant, plan.item.OperationID, relocationStepKey(plan), definition).Scan(&step); err != nil {
		return err
	}
	if step != "pending" && step != "prepared" {
		return fmt.Errorf("relocation target has no matching preparation step")
	}
	if plan.resumed && (plan.ready || plan.gateStatus == "prepared") && step != "prepared" {
		return fmt.Errorf("prepared relocation has an incomplete target step")
	}
	return nil
}

func prepareRelocationTarget(ctx context.Context, cfg Config, plan *relocationPlan) error {
	if err := checkRelocationTargetClaim(ctx, cfg.Engine.DB(), plan); err != nil {
		return err
	}
	// Refresh the catalog before applying compiled DDL. An unexpectedly present
	// table must still have this model's owner marker and matching manifest claim.
	catalog, err := readSourceTable(ctx, plan.table.db, plan.table.source, plan.table.namespace, plan.table.name)
	if err == nil {
		plan.table.catalog, plan.table.exists = catalog, true
	} else {
		var failure *apiError
		if !errors.As(err, &failure) || failure.status != 404 {
			return err
		}
		plan.table.exists = false
	}
	statements, err := mappedTablePlan(ctx, plan.item.Definition, []*mappedTable{plan.table}, plan.table)
	if err != nil {
		return err
	}
	for _, statement := range statements {
		if _, err = plan.table.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("cannot apply compiled relocation DDL")
		}
	}
	catalog, err = readSourceTable(ctx, plan.table.db, plan.table.source, plan.table.namespace, plan.table.name)
	if err != nil {
		return err
	}
	plan.table.catalog, plan.table.exists = catalog, true
	remaining, err := mappedTablePlan(ctx, plan.item.Definition, []*mappedTable{plan.table}, plan.table)
	if err != nil || len(remaining) != 0 {
		return fmt.Errorf("relocation target catalog is not fully prepared")
	}
	if err = validateMappedCatalog([]*mappedTable{plan.table}); err != nil {
		return err
	}
	tx, err := plan.table.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actual, err := readTargetRelocationRows(ctx, tx, plan, true)
	if err != nil {
		return err
	}
	if len(actual) == 0 && len(plan.rows) != 0 {
		columns := []string{plan.table.quoted(plan.table.keyColumn)}
		for _, property := range plan.item.Definition.Schema.Properties {
			columns = append(columns, plan.table.quoted(property.Mapping.Column))
		}
		statement := "INSERT INTO " + plan.table.qualified() + " (" + strings.Join(columns, ",") + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ")"
		for _, row := range plan.rows {
			args := append([]any{row.ID}, row.native...)
			if _, err = tx.ExecContext(ctx, statement, args...); err != nil {
				return fmt.Errorf("cannot copy source rows into the relocation target")
			}
		}
		actual, err = readTargetRelocationRows(ctx, tx, plan, true)
		if err != nil {
			return err
		}
	}
	if digest(actual) != digest(plan.rows) {
		return fmt.Errorf("relocation target differs from source; existing data will not be overwritten")
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_, err = cfg.Engine.DB().ExecContext(ctx, "UPDATE af_data_mapped_steps SET status='prepared' WHERE tenant_id=? AND operation_id=? AND table_key=? AND definition_id=?", plan.item.Definition.TenantID, plan.item.OperationID, relocationStepKey(plan), plan.item.Definition.ID)
	return err
}

func readDefaultRelocationRows(ctx context.Context, reader relocationQueryer, definition c.Definition) ([]relocationRow, error) {
	columns := []string{"`id`"}
	for _, property := range definition.Schema.Properties {
		columns = append(columns, quote(columnName(property.ID)))
	}
	statement := "SELECT " + strings.Join(columns, ",") + " FROM " + quote(tableName(definition.ID)) + " WHERE tenant_id=? ORDER BY id LIMIT ?"
	return readRelocationRows(ctx, reader, statement, []any{definition.TenantID, relocationMaxRows + 1}, definition.Schema.Properties)
}

func readTargetRelocationRows(ctx context.Context, reader relocationQueryer, plan *relocationPlan, lock bool) ([]relocationRow, error) {
	columns := []string{plan.table.quoted(plan.table.keyColumn)}
	for _, property := range plan.item.Definition.Schema.Properties {
		columns = append(columns, plan.table.quoted(property.Mapping.Column))
	}
	statement := "SELECT " + strings.Join(columns, ",") + " FROM " + plan.table.qualified() + " ORDER BY " + plan.table.quoted(plan.table.keyColumn) + " LIMIT ?"
	if lock {
		statement += " FOR UPDATE"
	}
	return readRelocationRows(ctx, reader, statement, []any{relocationMaxRows + 1}, plan.item.Definition.Schema.Properties)
}

func readRelocationRows(ctx context.Context, reader relocationQueryer, statement string, args []any, properties []c.Property) ([]relocationRow, error) {
	rows, err := reader.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []relocationRow{}
	bytes := 0
	for rows.Next() {
		if len(result) == relocationMaxRows {
			return nil, fmt.Errorf("relocation exceeds the 10000-row per-table budget")
		}
		values, destinations := make([]any, len(properties)+1), make([]any, len(properties)+1)
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		row := relocationRow{ID: dbString(values[0]), Values: map[string]json.RawMessage{}, native: make([]any, len(properties))}
		if !uuidPattern.MatchString(row.ID) {
			return nil, fmt.Errorf("relocation source contains an invalid stable instance id")
		}
		for i, property := range properties {
			value := values[i+1]
			if value == nil && !property.Nullable {
				return nil, fmt.Errorf("relocation source violates a required property")
			}
			raw, err := mappedRawValue(property, value)
			if err != nil {
				return nil, err
			}
			row.Values[property.ID] = raw
			bytes += len(raw)
			if bytes > relocationMaxBytes {
				return nil, fmt.Errorf("relocation exceeds the 64-MiB per-table budget")
			}
			if valueBytes, ok := value.([]byte); ok {
				// Retain empty strings as empty strings: copying an empty byte
				// slice onto nil would bind SQL NULL through database/sql.
				value = string(valueBytes)
			}
			row.native[i] = value
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func relocationResults(plans []*relocationPlan) []c.StorageRelocationResult {
	results := make([]c.StorageRelocationResult, 0, len(plans))
	for _, plan := range plans {
		results = append(results, c.StorageRelocationResult{DefinitionID: plan.item.Definition.ID, Rows: len(plan.rows), ContentDigest: digest(plan.rows), Statements: plan.statements})
	}
	return results
}

func checkRelocationBatchBudget(plans []*relocationPlan) error {
	bytes := 0
	for _, plan := range plans {
		for _, row := range plan.rows {
			for _, value := range row.Values {
				bytes += len(value)
				if bytes > 2*relocationMaxBytes {
					return fmt.Errorf("relocation exceeds the 128-MiB batch budget")
				}
			}
		}
	}
	return nil
}

func closeRelocationPlans(plans []*relocationPlan) {
	for _, plan := range plans {
		if plan.table != nil {
			_ = plan.table.db.Close()
		}
	}
}

func orderedRelocationPlans(plans []*relocationPlan) []*relocationPlan {
	ordered := append([]*relocationPlan(nil), plans...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].item.Definition.ID < ordered[j].item.Definition.ID })
	return ordered
}
