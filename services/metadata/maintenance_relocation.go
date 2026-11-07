package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"sort"
	"strings"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

var relocationOperationID = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)
var relocationDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ValidateDefaultStorageRelocation checks a fixed offline relocation manifest
// against Metadata without changing rows. Data owns source ownership, driver,
// catalog, copied-row verification and gate admission. The caller must stop the
// public services before coordinating those checks and committing this batch.
func ValidateDefaultStorageRelocation(ctx context.Context, engine *storage.Engine, items []c.StorageRelocation) error {
	return readStorageRelocation(ctx, engine, items, false)
}

// VerifyDefaultStorageRelocation requires every current definition and its
// completed publication/outbox evidence to match this exact manifest.
func VerifyDefaultStorageRelocation(ctx context.Context, engine *storage.Engine, items []c.StorageRelocation) error {
	return readStorageRelocation(ctx, engine, items, true)
}

func readStorageRelocation(ctx context.Context, engine *storage.Engine, items []c.StorageRelocation, requireCommitted bool) error {
	if ctx == nil {
		return problem(422, "invalid_relocation", "离线迁移需要有效的调用上下文")
	}
	ordered, err := prepareStorageRelocation(engine, items)
	if err != nil {
		return err
	}
	tx, err := engine.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	committed, err := inspectStorageRelocation(ctx, tx, ordered, false)
	if err != nil {
		return err
	}
	if requireCommitted && !committed {
		return problem(409, "relocation_not_committed", "迁移清单尚未全部提交")
	}
	return tx.Commit()
}

// CommitDefaultStorageRelocation changes only Metadata's authoritative current
// definitions and publication evidence in one transaction. It deliberately does
// not relax ordinary compatibleUpdate or expose an HTTP rebinding endpoint.
// All-new state is idempotent only when the same operation evidence matches.
func CommitDefaultStorageRelocation(ctx context.Context, engine *storage.Engine, items []c.StorageRelocation) error {
	ordered, err := prepareStorageRelocation(engine, items)
	if err != nil {
		return err
	}
	return engine.WithTx(ctx, func(tx *sql.Tx) error {
		committed, err := inspectStorageRelocation(ctx, tx, ordered, true)
		if err != nil || committed {
			return err
		}
		for _, item := range ordered {
			d := item.Definition
			raw, _ := json.Marshal(d)
			result, err := tx.ExecContext(ctx, "UPDATE af_meta_definitions SET current_json=?,current_digest=? WHERE tenant_id=? AND id=? AND current_digest=? AND draft_json IS NULL AND draft_digest='' AND status='active'", string(raw), item.Digest, d.TenantID, d.ID, item.Previous.ETag)
			if err != nil {
				return err
			}
			if count, err := result.RowsAffected(); err != nil || count != 1 {
				return problem(409, "relocation_conflict", "提交时模型当前定义已改变")
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO af_meta_publications (id,tenant_id,definition_id,candidate_json,candidate_digest,previous_digest,status,error_text) VALUES (?,?,?,?,?,?,'succeeded','')", item.OperationID, d.TenantID, d.ID, string(raw), item.Digest, item.Previous.ETag); err != nil {
				return err
			}
			manifest, _ := json.Marshal(item)
			if _, err := tx.ExecContext(ctx, "INSERT INTO af_meta_outbox (id,tenant_id,event_type,object_id,payload) VALUES (?,?,'definition.storage_relocated',?,?)", item.OperationID, d.TenantID, d.ID, string(manifest)); err != nil {
				return err
			}
		}
		return nil
	})
}

func prepareStorageRelocation(engine *storage.Engine, items []c.StorageRelocation) ([]c.StorageRelocation, error) {
	if engine == nil || engine.Capabilities().Driver != "mysql" {
		return nil, problem(422, "unsupported_relocation", "离线迁移仅支持现有MySQL元数据存储")
	}
	return canonicalStorageRelocation(items)
}

func canonicalStorageRelocation(items []c.StorageRelocation) ([]c.StorageRelocation, error) {
	if len(items) == 0 || len(items) > 16 {
		return nil, problem(422, "invalid_relocation", "迁移清单须包含1至16个对象模型")
	}
	// Clone before canonicalization: the reviewed manifest must not be mutated.
	raw, err := json.Marshal(items)
	if err != nil {
		return nil, problem(422, "invalid_relocation", "迁移清单不是有效JSON定义")
	}
	var ordered []c.StorageRelocation
	if err = json.Unmarshal(raw, &ordered); err != nil {
		return nil, err
	}
	tenant, category, source, sourceETag := "", "", "", ""
	ids, operations, targets := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i := range ordered {
		item := &ordered[i]
		old, next := &item.Previous.Definition, &item.Definition
		if !relocationOperationID.MatchString(item.OperationID) || !relocationDigest.MatchString(item.SourceETag) || ids[next.ID] || operations[item.OperationID] || next.ID == "" {
			return nil, problem(422, "invalid_relocation", "迁移操作标识及对象稳定ID必须有效且不重复，并包含来源配置摘要")
		}
		ids[next.ID], operations[item.OperationID] = true, true
		if item.Previous.Status != "active" || old.Kind != c.KindSchema || next.Kind != c.KindSchema || old.Schema == nil || next.Schema == nil || old.Schema.StorageBinding != nil || old.Schema.Mode != "schema_2_db" || old.Schema.StorageLayout != "single" || next.Schema.Mode != "schema_2_db" || next.Schema.StorageLayout != "single" || next.Schema.StorageBinding == nil {
			return nil, problem(422, "unsupported_relocation", "只允许已生效默认存储的单表正向模型迁移到显式主表")
		}
		if err := canonicalDefinition(old, false); err != nil {
			return nil, err
		}
		if err := canonicalDefinition(next, false); err != nil {
			return nil, err
		}
		if item.Previous.ETag != digestDefinition(*old) || item.Digest != digestDefinition(*next) {
			return nil, problem(422, "invalid_relocation_digest", "迁移清单的旧定义或候选摘要不匹配规范定义")
		}
		binding := next.Schema.StorageBinding
		if !relocationOperationID.MatchString(binding.DataSourceID) || binding.Namespace == "" || binding.IDColumn != "id" || binding.TenantColumn != "" {
			return nil, problem(422, "unsupported_relocation", "迁移目标必须是显式专用数据源命名空间及id单主键，不配置共享租户列")
		}
		if tenant == "" {
			tenant, category, source, sourceETag = next.TenantID, next.CategoryID, binding.DataSourceID, item.SourceETag
		}
		if tenant == "" || len(tenant) > 128 || next.TenantID != tenant || next.CategoryID != category || binding.DataSourceID != source || item.SourceETag != sourceETag {
			return nil, problem(422, "invalid_relocation_scope", "一次迁移仅覆盖同租户同分类及同一个显式数据源")
		}
		target := mappingLocator(binding.DataSourceID, binding.Namespace, binding.Table)
		if targets[target] {
			return nil, problem(422, "invalid_relocation", "不同模型不能迁移到同一个主表")
		}
		targets[target] = true
		for _, property := range next.Schema.Properties {
			m := property.Mapping
			if m == nil || m.DataSourceID != source || m.Namespace != binding.Namespace || m.Table != binding.Table || m.KeyColumn != binding.IDColumn || m.TenantColumn != "" {
				return nil, problem(422, "unsupported_relocation", "离线迁移仅支持全部属性映射到模型自身主表")
			}
		}
		logical := *next.Schema
		logical.StorageBinding = nil
		logical.Properties = append([]c.Property(nil), logical.Properties...)
		for n := range logical.Properties {
			logical.Properties[n].Mapping = nil
		}
		comparison := *next
		comparison.Schema = &logical
		if digestDefinition(comparison) != digestDefinition(*old) {
			return nil, problem(422, "unsupported_relocation", "存储迁移不能修改对象、属性、索引、类型、默认值、组织或稳定ID")
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Definition.ID < ordered[j].Definition.ID })
	return ordered, nil
}

func inspectStorageRelocation(ctx context.Context, tx *sql.Tx, items []c.StorageRelocation, lock bool) (bool, error) {
	tenant := items[0].Definition.TenantID
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var active string
	if err := tx.QueryRowContext(ctx, "SELECT publication_id FROM af_meta_tenant_guards WHERE tenant_id=?"+suffix, tenant).Scan(&active); err != nil {
		return false, err
	}
	if active != "" {
		return false, problem(409, "publication_in_progress", "租户存在未完成发布，不能切换存储")
	}
	var pending int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM af_meta_publications WHERE tenant_id=? AND status NOT IN ('succeeded','rejected')", tenant).Scan(&pending); err != nil {
		return false, err
	}
	if pending != 0 {
		return false, problem(409, "publication_in_progress", "租户发布记录尚未完成，不能切换存储")
	}
	newCount := 0
	for _, item := range items {
		d := item.Definition
		row, err := scanDefinition(tx.QueryRowContext(ctx, "SELECT "+definitionColumns+" FROM af_meta_definitions WHERE tenant_id=? AND id=?"+suffix, tenant, d.ID))
		if err != nil {
			return false, err
		}
		if row.status != "active" || row.draft.Valid || row.draftDigest != "" {
			return false, problem(409, "relocation_conflict", "待迁移模型必须已生效且没有草稿")
		}
		current, err := parseDefinition(row.current)
		if err != nil {
			return false, err
		}
		if current.ID != row.d.ID || current.TenantID != row.d.TenantID || current.Kind != row.d.Kind || current.Name != row.d.Name || current.CategoryID != row.d.CategoryID || current.GroupID != row.d.GroupID || digestDefinition(current) != row.currentDigest {
			return false, problem(409, "relocation_conflict", "模型当前定义与登记记录不一致")
		}
		isNew := row.currentDigest == item.Digest && reflect.DeepEqual(current, item.Definition)
		if !isNew && (row.currentDigest != item.Previous.ETag || !reflect.DeepEqual(current, item.Previous.Definition)) {
			return false, problem(409, "relocation_conflict", "模型权威当前定义不符合原迁移清单")
		}
		if err := validateOrg(ctx, tx, d); err != nil {
			return false, err
		}
		if err := validateReferences(ctx, tx, d); err != nil {
			return false, err
		}
		if err := relocationPublicationEvidence(ctx, tx, item, isNew); err != nil {
			return false, err
		}
		if isNew {
			newCount++
		}
	}
	if newCount != 0 && newCount != len(items) {
		return false, problem(409, "relocation_partial_commit", "模型迁移出现新旧混合，拒绝继续自动切换")
	}
	if newCount == 0 {
		if err := validateRelocationLinks(ctx, tx, items); err != nil {
			return false, err
		}
	}
	return newCount == len(items), nil
}

func relocationPublicationEvidence(ctx context.Context, tx *sql.Tx, item c.StorageRelocation, committed bool) error {
	pub, err := scanPublication(tx.QueryRowContext(ctx, "SELECT "+publicationColumns+" FROM af_meta_publications WHERE id=?", item.OperationID))
	if !committed {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			return problem(409, "relocation_conflict", "迁移操作标识已经被其他发布记录占用")
		}
	} else if err != nil || pub.p.TenantID != item.Definition.TenantID || pub.p.DefinitionID != item.Definition.ID || pub.p.Status != "succeeded" || pub.p.Error != "" || pub.digest != item.Digest || pub.previous != item.Previous.ETag || !reflect.DeepEqual(pub.candidate, item.Definition) {
		return problem(409, "relocation_conflict", "迁移后的发布记录与原清单不一致")
	}
	var tenant, event, object, payload string
	err = tx.QueryRowContext(ctx, "SELECT tenant_id,event_type,object_id,payload FROM af_meta_outbox WHERE id=?", item.OperationID).Scan(&tenant, &event, &object, &payload)
	if !committed {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return problem(409, "relocation_conflict", "迁移操作标识已经被其他事件占用")
	}
	manifest, _ := json.Marshal(item)
	if err != nil || tenant != item.Definition.TenantID || event != "definition.storage_relocated" || object != item.Definition.ID || payload != string(manifest) {
		return problem(409, "relocation_conflict", "迁移后的定义事件与原清单不一致")
	}
	return nil
}

func validateRelocationLinks(ctx context.Context, tx *sql.Tx, items []c.StorageRelocation) error {
	marks, ids := make([]string, len(items)), make([]any, len(items))
	for i, item := range items {
		marks[i], ids[i] = "?", item.Definition.ID
	}
	conditions, args := []string{}, []any{items[0].Definition.TenantID}
	for _, path := range []string{"current_json,'$.link.sourceSchemaId'", "current_json,'$.link.targetSchemaId'", "draft_json,'$.link.sourceSchemaId'", "draft_json,'$.link.targetSchemaId'"} {
		conditions = append(conditions, "JSON_UNQUOTE(JSON_EXTRACT("+path+")) IN ("+strings.Join(marks, ",")+")")
		args = append(args, ids...)
	}
	rows, err := tx.QueryContext(ctx, "SELECT current_json,draft_json FROM af_meta_definitions WHERE tenant_id=? AND kind='link' AND ("+strings.Join(conditions, " OR ")+") LIMIT 1001", args...)
	if err != nil {
		return err
	}
	links := []c.Definition{}
	for rows.Next() {
		var current, draft sql.NullString
		if err := rows.Scan(&current, &draft); err != nil {
			rows.Close()
			return err
		}
		if draft.Valid {
			rows.Close()
			return problem(409, "relocation_dependency", "相关Link存在草稿，先处理草稿再迁移")
		}
		if current.Valid {
			d, err := parseDefinition(current)
			if err != nil {
				rows.Close()
				return err
			}
			if d.Link == nil || d.Link.StorageBinding != nil {
				rows.Close()
				return problem(422, "relocation_dependency", "相关Link已经绑定关系存储，须另行协调关系迁移")
			}
			links = append(links, d)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(links) > 1000 {
		return problem(422, "relocation_dependency_budget", "相关Link目录超过本次离线检查预算")
	}
	for _, d := range links {
		if d.TenantID != items[0].Definition.TenantID {
			return problem(422, "relocation_dependency", "相关Link的租户身份不一致")
		}
		if err := validateReferences(ctx, tx, d); err != nil {
			return err
		}
	}
	return nil
}
