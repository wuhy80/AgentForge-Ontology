package data

// Source writes are explicitly enabled per definition. Runtime requests never
// create these control tables, change source credentials, or enable a source.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

var mappedWriteMigrations = []storage.Migration{
	{ID: "001_source_write_identity", SQL: `CREATE TABLE af_source_write_identity (id TINYINT NOT NULL PRIMARY KEY, tenant_id VARBINARY(128) NOT NULL, source_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, secret BINARY(32) NOT NULL, CONSTRAINT ck_source_write_identity CHECK(id=1)) ENGINE=InnoDB`},
	{ID: "002_source_write_gates", SQL: `CREATE TABLE af_source_write_gates (tenant_id VARBINARY(128) NOT NULL, schema_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, definition_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, table_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, catalog_signature CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, status VARCHAR(16) NOT NULL, operation_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, PRIMARY KEY(tenant_id,schema_id), UNIQUE KEY source_write_target(table_key)) ENGINE=InnoDB`},
	{ID: "003_source_write_receipts", SQL: `CREATE TABLE af_source_write_receipts (tenant_id VARBINARY(128) NOT NULL, schema_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, subject_id VARBINARY(255) NOT NULL, operation VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, request_key VARBINARY(128) NOT NULL, request_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, response LONGTEXT NOT NULL, PRIMARY KEY(tenant_id,schema_id,subject_id,operation,request_key)) ENGINE=InnoDB`},
	{ID: "004_source_write_outbox", SQL: `CREATE TABLE af_source_write_outbox (id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY, tenant_id VARBINARY(128) NOT NULL, schema_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, entity_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, event_type VARCHAR(40) NOT NULL, payload LONGTEXT NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)) ENGINE=InnoDB`},
}

type mappedWriteState struct {
	secret []byte
	digest string
	status string
}

func missingSourceControls(err error) bool {
	var my *mysql.MySQLError
	return errors.As(err, &my) && my.Number == 1146
}

func mappedWriteUnsupported(reason string) error {
	return problem(422, reason, "当前来源尚未具备已认证的实例写入能力")
}

// Validate the complete physical scope before allowing a mutation. A source
// account having UPDATE privileges is not itself a write certification.
func validateMappedWriteTable(ctx context.Context, d c.Definition, tables []*mappedTable) error {
	if len(tables) != 1 || !tables[0].primary || tables[0].source.Driver != "mysql" || tables[0].source.TenantIsolation != "dedicated" || tables[0].tenantColumn != "" {
		return mappedWriteUnsupported("mapped_write_scope_unsupported")
	}
	t := tables[0]
	if err := requireMappedWriteCatalogVisibility(ctx, t); err != nil {
		return err
	}
	if err := validateMappedCatalog(tables); err != nil {
		return err
	}
	key, ok := t.column(t.keyColumn)
	if !ok || key.Generated || key.Nullable || key.MaxLength != 36 || sourceKeyType(key) != "string" {
		return mappedWriteUnsupported("mapped_write_key_unsupported")
	}
	collations, err := mappedWriteCollations(ctx, t)
	if err != nil {
		return err
	}
	keyCollation := collations[t.keyColumn]
	if keyCollation != "ascii_bin" && keyCollation != "utf8mb4_0900_bin" {
		return mappedWriteUnsupported("mapped_write_key_unsupported")
	}
	expected := map[string]bool{t.keyColumn: true}
	for _, p := range d.Schema.Properties {
		if p.Mapping.Column == t.keyColumn {
			return mappedWriteUnsupported("mapped_write_key_property_unsupported")
		}
		column, ok := t.column(p.Mapping.Column)
		if !ok || !mappedWriteColumnMatches(t, p, column, collations[p.Mapping.Column]) {
			return mappedWriteUnsupported("mapped_write_columns_unsupported")
		}
		expected[p.Mapping.Column] = true
	}
	if len(expected) != len(t.catalog.Columns) {
		return mappedWriteUnsupported("mapped_write_columns_unsupported")
	}
	for _, col := range t.catalog.Columns {
		if !expected[col.Name] || col.Generated {
			return mappedWriteUnsupported("mapped_write_columns_unsupported")
		}
	}
	var engine, collation string
	if err := t.db.QueryRowContext(ctx, "SELECT engine,table_collation FROM information_schema.tables WHERE table_schema=? AND table_name=?", t.namespace, t.name).Scan(&engine, &collation); err != nil {
		return err
	}
	if !strings.EqualFold(engine, "InnoDB") || collation != "utf8mb4_0900_bin" {
		return mappedWriteUnsupported("mapped_write_storage_unsupported")
	}
	var triggers, unsafeForeignKeys int
	if err := t.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.triggers WHERE event_object_schema=? AND event_object_table=?", t.namespace, t.name).Scan(&triggers); err != nil {
		return err
	}
	if err := t.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.referential_constraints rc JOIN information_schema.key_column_usage kc ON kc.constraint_schema=rc.constraint_schema AND kc.constraint_name=rc.constraint_name AND kc.table_name=rc.table_name WHERE ((kc.table_schema=? AND kc.table_name=?) OR (kc.referenced_table_schema=? AND kc.referenced_table_name=?)) AND (rc.delete_rule NOT IN ('RESTRICT','NO ACTION') OR rc.update_rule NOT IN ('RESTRICT','NO ACTION'))", t.namespace, t.name, t.namespace, t.name).Scan(&unsafeForeignKeys); err != nil {
		return err
	}
	if triggers != 0 || unsafeForeignKeys != 0 {
		return mappedWriteUnsupported("mapped_write_effects_unsupported")
	}
	return validateReverseIndexes(d, tables)
}

func mappedWriteCollations(ctx context.Context, table *mappedTable) (map[string]string, error) {
	rows, err := table.db.QueryContext(ctx, "SELECT column_name,collation_name FROM information_schema.columns WHERE table_schema=? AND table_name=?", table.namespace, table.name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var name string
		var collation sql.NullString
		if err := rows.Scan(&name, &collation); err != nil {
			return nil, err
		}
		result[name] = collation.String
	}
	return result, rows.Err()
}

func mappedWriteColumnMatches(table *mappedTable, property c.Property, column c.SourceColumn, collation string) bool {
	if !mappedManagedColumnMatches(table, property, column) {
		return false
	}
	switch property.DataType {
	case "string", "text", "longtext":
		return collation == "utf8mb4_0900_bin"
	case "uuid":
		return collation == "ascii_bin" || collation == "utf8mb4_0900_bin"
	}
	return collation == ""
}

// SHOW GRANTS exposes the account's direct grants. Require schema-wide SELECT
// and TRIGGER plus global REFERENCES (or SELECT) visibility so a missing catalog
// row proves absence, including inbound foreign keys from other source schemas.
// Role grants and wildcard database patterns are deliberately not certified.
func requireMappedWriteCatalogVisibility(ctx context.Context, table *mappedTable) error {
	rows, err := table.db.QueryContext(ctx, "SHOW GRANTS")
	if err != nil {
		return err
	}
	defer rows.Close()
	grants := []string{}
	for rows.Next() {
		var grant string
		if err := rows.Scan(&grant); err != nil {
			return err
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !mappedWriteCatalogGrants(grants, table.namespace) {
		return mappedWriteUnsupported("mapped_write_metadata_visibility_unsupported")
	}
	return nil
}

func mappedWriteCatalogGrants(grants []string, namespace string) bool {
	visible := map[string]bool{}
	globalCatalog := false
	for _, grant := range grants {
		if strings.HasPrefix(grant, "REVOKE ") {
			return false
		}
		privileges, rest, ok := strings.Cut(grant, " ON ")
		if !ok || !strings.HasPrefix(privileges, "GRANT ") {
			continue
		}
		scope, _, ok := strings.Cut(rest, " TO ")
		if !ok || (scope != "*.*" && scope != quote(namespace)+".*") {
			continue
		}
		for _, privilege := range strings.Split(strings.TrimPrefix(privileges, "GRANT "), ",") {
			privilege = strings.TrimSpace(privilege)
			if scope == "*.*" && (privilege == "REFERENCES" || privilege == "SELECT" || privilege == "ALL PRIVILEGES") {
				globalCatalog = true
			}
			if privilege == "ALL PRIVILEGES" {
				visible["SELECT"], visible["TRIGGER"] = true, true
			}
			visible[privilege] = true
		}
	}
	return visible["SELECT"] && visible["TRIGGER"] && globalCatalog
}

func readMappedWriteState(ctx context.Context, table *mappedTable, current c.CurrentDefinition, lock *sql.Tx) (mappedWriteState, bool, error) {
	var state mappedWriteState
	var tenant, source string
	query := "SELECT tenant_id,source_id,secret FROM af_source_write_identity WHERE id=1"
	var err error
	if lock == nil {
		err = table.db.QueryRowContext(ctx, query).Scan(&tenant, &source, &state.secret)
	} else {
		err = lock.QueryRowContext(ctx, query+" FOR SHARE").Scan(&tenant, &source, &state.secret)
	}
	if missingSourceControls(err) || errors.Is(err, sql.ErrNoRows) {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	if tenant != current.Definition.TenantID || source != table.source.ID || len(state.secret) != 32 {
		return state, false, mappedWriteUnsupported("mapped_write_owner_mismatch")
	}
	var key, signature string
	query = "SELECT definition_digest,status,table_key,catalog_signature FROM af_source_write_gates WHERE tenant_id=? AND schema_id=?"
	args := []any{tenant, current.Definition.ID}
	if lock == nil {
		err = table.db.QueryRowContext(ctx, query, args...).Scan(&state.digest, &state.status, &key, &signature)
	} else {
		err = lock.QueryRowContext(ctx, query+" FOR UPDATE", args...).Scan(&state.digest, &state.status, &key, &signature)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	if state.status != "ready" || state.digest != current.ETag || key != digest(table.locator()) || signature != table.catalog.Signature {
		return state, false, nil
	}
	return state, true, nil
}

func (h *handler) mappedMutationReadiness(ctx context.Context, current c.CurrentDefinition) (bool, string, error) {
	if h.cfg.Engine == nil {
		return false, "storage_unavailable", nil
	}
	tables, err := h.mappedTables(ctx, current.Definition, false)
	if err != nil {
		return false, "mapped_write_unavailable", err
	}
	defer closeMappedTables(tables)
	if err = validateMappedWriteTable(ctx, current.Definition, tables); err != nil {
		var api *apiError
		if errors.As(err, &api) {
			return false, api.code, nil
		}
		return false, "mapped_write_unavailable", err
	}
	_, enabled, err := readMappedWriteState(ctx, tables[0], current, nil)
	if enabled {
		return true, "", err
	}
	return enabled, "mapped_write_not_initialized", err
}

// InitializeMappedWrites is an explicit maintenance command. It verifies the
// current definition, migrates source-owned control tables, and enables only
// that exact definition. It does not modify any business row or source setting.
func InitializeMappedWrites(ctx context.Context, cfg Config, tenantID, schemaID string) error {
	if cfg.Engine == nil || tenantID == "" || !uuidPattern.MatchString(schemaID) {
		return fmt.Errorf("来源写入初始化需要明确租户、Schema及Data存储")
	}
	h := &handler{cfg: cfg, client: cfg.HTTPClient}
	if h.client == nil {
		h.client = &http.Client{Timeout: 10 * time.Second}
	}
	current, err := h.current(ctx, tenantID, schemaID)
	if err != nil {
		return err
	}
	tables, err := h.mappedTables(ctx, current.Definition, false)
	if err != nil {
		return err
	}
	defer closeMappedTables(tables)
	if err := validateMappedWriteTable(ctx, current.Definition, tables); err != nil {
		return err
	}
	table := tables[0]
	if err := storage.Migrate(ctx, table.db, "source_write", mappedWriteMigrations); err != nil {
		return err
	}
	// Hold the platform publication lock during explicit enablement; source
	// mutations themselves use their source gate as the atomic admission barrier.
	return cfg.Engine.WithTx(ctx, func(platformTx *sql.Tx) error {
		p := c.Principal{TenantID: tenantID}
		if err := h.gate(ctx, platformTx, p, current); err != nil {
			return err
		}
		tx, err := table.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO af_source_write_identity(id,tenant_id,source_id,secret) VALUES(1,?,?,?) ON DUPLICATE KEY UPDATE id=id", tenantID, table.source.ID, secret); err != nil {
			return err
		}
		var ownerTenant, ownerSource string
		if err := tx.QueryRowContext(ctx, "SELECT tenant_id,source_id FROM af_source_write_identity WHERE id=1 FOR UPDATE").Scan(&ownerTenant, &ownerSource); err != nil {
			return err
		}
		if ownerTenant != tenantID || ownerSource != table.source.ID {
			return mappedWriteUnsupported("mapped_write_owner_mismatch")
		}
		var oldDigest, oldStatus string
		err = tx.QueryRowContext(ctx, "SELECT definition_digest,status FROM af_source_write_gates WHERE tenant_id=? AND schema_id=? FOR UPDATE", tenantID, schemaID).Scan(&oldDigest, &oldStatus)
		if err == nil && (oldDigest != current.ETag || oldStatus != "ready") {
			return problem(409, "mapped_write_recovery_required", "来源写入正在切换，请恢复对应模型发布")
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO af_source_write_gates(tenant_id,schema_id,definition_digest,table_key,catalog_signature,status,operation_id) VALUES(?,?,?,?,?,'ready','initialize') ON DUPLICATE KEY UPDATE schema_id=schema_id", tenantID, schemaID, current.ETag, digest(table.locator()), table.catalog.Signature); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func mappedWriteETag(secret []byte, tenant, schema, definition string, instance c.Instance) string {
	instance.ETag = ""
	raw, _ := json.Marshal([]any{"mapped-write-state", tenant, schema, definition, instance})
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

func prepareMappedWriteProjection(ctx context.Context, tables []*mappedTable, current c.CurrentDefinition) error {
	if len(tables) != 1 || tables[0].source.Driver != "mysql" || tables[0].source.TenantIsolation != "dedicated" {
		return nil
	}
	state, enabled, err := readMappedWriteState(ctx, tables[0], current, nil)
	if err != nil {
		return err
	}
	if enabled {
		tables[0].writeSecret, tables[0].writeCurrent = state.secret, current
	}
	return nil
}

func stampMappedInstance(secret []byte, current c.CurrentDefinition, item c.Instance) (c.Instance, error) {
	if !uuidPattern.MatchString(item.ID) || item.ID != strings.ToLower(item.ID) {
		return item, problem(409, "source_drift", "来源主键不满足规范UUID写入身份约定")
	}
	values := make(map[string]json.RawMessage, len(item.Values))
	for _, p := range current.Definition.Schema.Properties {
		value, err := typedValue(p, item.Values[p.ID])
		if err != nil {
			return item, err
		}
		if p.DataType == "decimal" && value != nil {
			text := value.(string)
			parts := strings.SplitN(text, ".", 2)
			fraction := ""
			if len(parts) == 2 {
				fraction = parts[1]
			}
			fraction += strings.Repeat("0", p.Scale-len(fraction))
			whole := parts[0]
			if strings.Trim(whole, "-0") == "" && strings.Trim(fraction, "0") == "" {
				whole = "0"
			}
			value = whole
			if p.Scale > 0 {
				value = whole + "." + fraction
			}
		}
		raw, err := rawDatabaseValue(p, value)
		if err != nil {
			return item, err
		}
		values[p.ID] = raw
	}
	item.Values = values
	item.ETag = mappedWriteETag(secret, current.Definition.TenantID, current.Definition.ID, current.ETag, item)
	return item, nil
}
