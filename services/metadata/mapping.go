package metadata

import (
	"encoding/json"
	"reflect"
	"strings"
	"unicode"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func mappingIdentifier(value string, optional bool) bool {
	return (optional && value == "") || (value != "" && len(value) <= 128 && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0)
}

func mappingLocator(source, namespace, table string) string {
	raw, _ := json.Marshal([]string{source, namespace, table})
	return string(raw)
}

// Canonicalize inherited fields before hashing the draft so subsequent name
// edits and retries keep the same physical identity. Data verifies the live
// catalog and source permissions again before preview/publication/execution.
func canonicalStorageMappings(s *c.Schema) error {
	b := s.StorageBinding
	if b == nil {
		if s.Mode == "db_2_schema" {
			return problem(422, "invalid_mapping", "数据库为准的模型必须绑定主数据源及表")
		}
		for _, p := range s.Properties {
			if p.Mapping != nil {
				return problem(422, "invalid_mapping", "属性映射必须同时声明模型主表绑定")
			}
		}
		return nil
	}
	if !mappingIdentifier(b.DataSourceID, false) || !mappingIdentifier(b.Table, false) || !mappingIdentifier(b.Namespace, true) || !mappingIdentifier(b.TenantColumn, true) {
		return problem(422, "invalid_mapping", "主数据源、表或租户列无效")
	}
	if b.IDColumn == "" {
		b.IDColumn = "id"
	}
	if !mappingIdentifier(b.IDColumn, false) || b.IDColumn == b.TenantColumn {
		return problem(422, "invalid_mapping", "主表稳定标识列无效或与租户列重复")
	}
	primary := mappingLocator(b.DataSourceID, b.Namespace, b.Table)
	locators := map[string][2]string{primary: {b.IDColumn, b.TenantColumn}}
	columns := map[string]bool{}
	propertyLocators := map[string]string{}
	for i := range s.Properties {
		p := &s.Properties[i]
		m := p.Mapping
		if m == nil || !mappingIdentifier(m.DataSourceID, false) || !mappingIdentifier(m.Column, false) {
			return problem(422, "invalid_mapping", "每个对象属性必须指定数据源和物理列")
		}
		if m.DataSourceID == b.DataSourceID {
			if m.Namespace == "" {
				m.Namespace = b.Namespace
			}
			if m.Table == "" {
				m.Table = b.Table
			}
		}
		if !mappingIdentifier(m.Namespace, true) || !mappingIdentifier(m.Table, false) || !mappingIdentifier(m.KeyColumn, true) || !mappingIdentifier(m.TenantColumn, true) {
			return problem(422, "invalid_mapping", "属性来源表、关联键或租户列无效")
		}
		locator := mappingLocator(m.DataSourceID, m.Namespace, m.Table)
		if locator == primary {
			if m.KeyColumn == "" {
				m.KeyColumn = b.IDColumn
			}
			if m.TenantColumn == "" {
				m.TenantColumn = b.TenantColumn
			}
			if m.KeyColumn != b.IDColumn || m.TenantColumn != b.TenantColumn {
				return problem(422, "invalid_mapping", "主表属性的关联键及租户列必须与主表一致")
			}
		} else {
			if m.KeyColumn == "" || !p.Nullable {
				return problem(422, "invalid_mapping", "跨源或跨表属性需要唯一关联键并允许空值，以表示没有关联行")
			}
			if m.KeyColumn == m.TenantColumn {
				return problem(422, "invalid_mapping", "关联键不能与租户列重复")
			}
		}
		identity := [2]string{m.KeyColumn, m.TenantColumn}
		if previous, ok := locators[locator]; ok && previous != identity {
			return problem(422, "invalid_mapping", "同一来源表的属性必须使用一致的关联键和租户列")
		}
		locators[locator] = identity
		column := mappingLocator(locator, "", m.Column)
		if columns[column] {
			return problem(422, "invalid_mapping", "同一来源列不能重复映射为多个属性")
		}
		columns[column] = true
		propertyLocators[p.ID] = locator
	}
	if len(locators) > 8 {
		return problem(422, "budget_exceeded", "一个对象最多绑定8张来源表（含主表）")
	}
	for _, index := range s.Indexes {
		source := ""
		for _, field := range index.FieldIDs {
			if source != "" && propertyLocators[field] != source {
				return problem(422, "invalid_mapping", "一个物理索引不能跨数据源或跨表")
			}
			source = propertyLocators[field]
		}
	}
	return nil
}

// Existing bindings are immutable. Only additive, nullable columns and indexes
// can proceed to Data's live-catalog migration validation; destructive changes
// never become display-only updates by dropping an unknown field.
func compatibleMappedAddition(old, next c.Definition) bool {
	if old.Schema == nil || next.Schema == nil || old.Schema.StorageBinding == nil || next.Schema.StorageBinding == nil {
		return false
	}
	a, b := old.Schema, next.Schema
	if a.Mode != b.Mode || a.StorageLayout != b.StorageLayout || !reflect.DeepEqual(a.StorageBinding, b.StorageBinding) || len(b.Properties) < len(a.Properties) || len(b.Indexes) < len(a.Indexes) {
		return false
	}
	fields := map[string]c.Property{}
	for _, p := range b.Properties {
		fields[p.ID] = p
	}
	for _, p := range a.Properties {
		if !reflect.DeepEqual(p, fields[p.ID]) {
			return false
		}
		delete(fields, p.ID)
	}
	for _, p := range fields {
		if !p.Nullable || p.ReadOnly || len(p.Default) > 0 || p.EnumID != "" {
			return false
		}
	}
	indexes := map[string]c.Index{}
	for _, i := range b.Indexes {
		indexes[i.ID] = i
	}
	for _, i := range a.Indexes {
		if !reflect.DeepEqual(i, indexes[i.ID]) {
			return false
		}
	}
	return len(fields) > 0 || len(b.Indexes) > len(a.Indexes)
}
