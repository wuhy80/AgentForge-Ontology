package contracts

import (
	"bytes"
	"fmt"
	"reflect"
)

// SchemaAddition is the first, deliberately additive platform migration. It
// preserves every existing field and index identity and storage constraint.
type SchemaAddition struct {
	Properties []Property
	Indexes    []Index
}

// ValidateSchemaAddition is shared by Metadata admission and Data's physical
// planner. Live-catalog, uniqueness and backend checks remain Data's work.
func ValidateSchemaAddition(old, next Definition) (SchemaAddition, error) {
	var out SchemaAddition
	bad := func() (SchemaAddition, error) {
		return out, fmt.Errorf("仅支持默认单表追加属性或索引；已有属性、约束、索引和组织归属不能改变")
	}
	if old.ID != next.ID || old.TenantID != next.TenantID || old.Kind != KindSchema || next.Kind != KindSchema || old.CategoryID != next.CategoryID || old.GroupID != next.GroupID || old.Schema == nil || next.Schema == nil {
		return bad()
	}
	a, b := old.Schema, next.Schema
	if a.StorageBinding != nil || b.StorageBinding != nil || a.Mode != "schema_2_db" || b.Mode != a.Mode || a.StorageLayout != "single" || b.StorageLayout != a.StorageLayout {
		return bad()
	}
	fields := map[string]Property{}
	for _, p := range b.Properties {
		if p.ID == "" {
			return bad()
		}
		if _, exists := fields[p.ID]; exists {
			return bad()
		}
		fields[p.ID] = p
	}
	for _, p := range a.Properties {
		n, ok := fields[p.ID]
		if !ok || !reflect.DeepEqual(migrationProperty(p), migrationProperty(n)) {
			return bad()
		}
		delete(fields, p.ID)
	}
	for _, p := range b.Properties {
		if _, added := fields[p.ID]; !added {
			continue
		}
		if p.Mapping != nil || !p.Nullable && (len(p.Default) == 0 || bytes.Equal(bytes.TrimSpace(p.Default), []byte("null"))) || len(p.Default) > 0 && !validCallableSampleValue(p, p.Default) {
			return out, fmt.Errorf("新增属性须允许空值或声明合法的非空默认值，且不能绑定外部列")
		}
		out.Properties = append(out.Properties, p)
	}
	indexes := map[string]Index{}
	for _, idx := range b.Indexes {
		if idx.ID == "" {
			return bad()
		}
		if _, exists := indexes[idx.ID]; exists {
			return bad()
		}
		indexes[idx.ID] = idx
	}
	for _, idx := range a.Indexes {
		n, ok := indexes[idx.ID]
		idx.Name, n.Name = "", ""
		if !ok || !reflect.DeepEqual(idx, n) {
			return bad()
		}
		delete(indexes, idx.ID)
	}
	for _, idx := range b.Indexes {
		if _, added := indexes[idx.ID]; added {
			out.Indexes = append(out.Indexes, idx)
		}
	}
	if len(out.Properties) == 0 && len(out.Indexes) == 0 {
		return out, fmt.Errorf("没有可追加的属性或索引")
	}
	return out, nil
}

func migrationProperty(p Property) Property {
	p.Name = ""
	// Enum options are derived by readers and never alter stored field identity.
	p.EnumValues, p.EnumETag = nil, ""
	return p
}
