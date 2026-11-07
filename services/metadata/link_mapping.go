package metadata

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// Link mappings are explicit. Old definitions without a binding remain valid
// metadata; they must not silently gain runtime storage by being republished.
func canonicalLinkMapping(l *c.Link) error {
	b := l.StorageBinding
	if b == nil {
		return nil
	}
	bad := func() error {
		return problem(422, "invalid_link_mapping", "关系存储方式与映射字段不匹配")
	}
	switch b.Mode {
	case "managed":
		if *b != (c.LinkStorageBinding{Mode: "managed"}) {
			return bad()
		}
	case "junction":
		if !mappingIdentifier(b.DataSourceID, false) || !mappingIdentifier(b.Namespace, true) || !mappingIdentifier(b.Table, false) || !mappingIdentifier(b.SourceColumn, false) || !mappingIdentifier(b.TargetColumn, false) || !mappingIdentifier(b.TenantColumn, true) || b.SourceColumn == b.TargetColumn || b.SourceColumn == b.TenantColumn || b.TargetColumn == b.TenantColumn || b.ForeignKeySide != "" || b.ForeignKeyColumn != "" {
			return bad()
		}
	case "foreign_key":
		if !mappingIdentifier(b.DataSourceID, false) || !mappingIdentifier(b.ForeignKeyColumn, false) || b.Namespace != "" || b.Table != "" || b.SourceColumn != "" || b.TargetColumn != "" || b.TenantColumn != "" {
			return bad()
		}
		if b.ForeignKeySide == "source" {
			if l.Cardinality != "1:1" && l.Cardinality != "N:1" {
				return problem(422, "invalid_link_mapping", "来源表持有外键时仅支持一对一或多对一关系")
			}
		} else if b.ForeignKeySide == "target" {
			if l.Cardinality != "1:1" && l.Cardinality != "1:N" {
				return problem(422, "invalid_link_mapping", "目标表持有外键时仅支持一对一或一对多关系")
			}
		} else {
			return bad()
		}
	default:
		return problem(422, "unsupported_link_mapping", "关系仅支持平台关联表、外部关联表或外键映射")
	}
	return nil
}

func validateLinkEndpoints(l *c.Link, source, target c.Definition, tenant string) error {
	if source.ID != l.SourceSchemaID || target.ID != l.TargetSchemaID || source.TenantID != tenant || target.TenantID != tenant || source.Kind != c.KindSchema || target.Kind != c.KindSchema || source.Schema == nil || target.Schema == nil {
		return problem(422, "invalid_endpoint", "关系两端必须引用同租户已生效的对象模型")
	}
	b := l.StorageBinding
	if b == nil {
		return nil
	}
	sb, tb := source.Schema.StorageBinding, target.Schema.StorageBinding
	if b.Mode == "managed" {
		if sb != nil || tb != nil {
			return problem(422, "unsupported_link_mapping", "平台关联表当前仅支持两个使用平台默认存储的对象模型")
		}
		return nil
	}
	if sb == nil || tb == nil || sb.DataSourceID != b.DataSourceID || tb.DataSourceID != b.DataSourceID {
		return problem(422, "unsupported_link_mapping", "外部关系映射要求两端主表与关系使用同一数据源，暂不支持跨源关系查询")
	}
	if b.Mode == "foreign_key" {
		owner := sb
		if b.ForeignKeySide == "target" {
			owner = tb
		}
		if b.ForeignKeyColumn == owner.TenantColumn {
			return problem(422, "invalid_link_mapping", "外键列不能使用租户隔离列")
		}
	}
	return nil
}

// Validate both saved drafts and publication candidates against current
// endpoints. Drafts never expose a relationship to an unavailable tenant.
func validateLinkReferences(ctx context.Context, tx *sql.Tx, d c.Definition) error {
	if d.Link == nil {
		return nil
	}
	refs := make([]c.Definition, 2)
	for i, id := range []string{d.Link.SourceSchemaID, d.Link.TargetSchemaID} {
		var raw sql.NullString
		err := tx.QueryRowContext(ctx, "SELECT current_json FROM af_meta_definitions WHERE tenant_id=? AND id=? AND kind='schema' AND status='active' AND current_json IS NOT NULL", d.TenantID, id).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return problem(422, "invalid_endpoint", "关系两端必须引用同租户已生效的对象模型")
		}
		if err != nil {
			return err
		}
		refs[i], err = parseDefinition(raw)
		if err != nil {
			return err
		}
	}
	return validateLinkEndpoints(d.Link, refs[0], refs[1], d.TenantID)
}

// A definition-only Link has no relation facts to migrate. It may be explicitly
// bound once; changing an existing binding, endpoints or cardinality remains a
// migration and is rejected. Both arguments have display metadata removed.
func compatibleLinkBindingAddition(old, next c.Definition) bool {
	if old.Link == nil || next.Link == nil || old.Link.StorageBinding != nil || next.Link.StorageBinding == nil {
		return false
	}
	a, b := *old.Link, *next.Link
	a.StorageBinding, b.StorageBinding = nil, nil
	return reflect.DeepEqual(a, b)
}
