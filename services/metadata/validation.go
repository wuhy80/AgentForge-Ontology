package metadata

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

func normalizeName(name string) (string, string, error) {
	name = norm.NFC.String(strings.TrimSpace(name))
	if name == "" || utf8.RuneCountInString(name) > 128 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", "", problem(400, "invalid_name", "名称必须为1到128个字符，且不能包含控制字符")
	}
	return name, cases.Fold().String(name), nil
}

func digestDefinition(d contracts.Definition) string {
	b, _ := json.Marshal(d)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func canonicalDefinition(d *contracts.Definition, assignIDs bool) error {
	var err error
	d.Name, _, err = normalizeName(d.Name)
	if err != nil {
		return err
	}
	if d.CategoryID == "" || d.GroupID == "" {
		return problem(400, "invalid_organization", "分类和分组不可为空")
	}
	count := 0
	for _, present := range []bool{d.Schema != nil, d.Enum != nil, d.Link != nil, d.Action != nil, d.Function != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return problem(400, "invalid_definition", "必须且只能包含与kind一致的一份定义正文")
	}
	switch d.Kind {
	case contracts.KindSchema:
		if d.Schema == nil {
			return invalidBody()
		}
		s := d.Schema
		if s.Mode == "schema_to_db" {
			s.Mode = "schema_2_db"
		}
		if s.Mode == "db_to_schema" {
			s.Mode = "db_2_schema"
		}
		if s.StorageLayout == "" {
			s.StorageLayout = "single"
		}
		if (s.Mode != "schema_2_db" && s.Mode != "db_2_schema") || s.StorageLayout != "single" {
			return problem(422, "unsupported_capability", "支持schema_2_db或db_2_schema与single布局")
		}
		if err := validateProperties(s.Properties, assignIDs); err != nil {
			return err
		}
		if err := canonicalStorageMappings(s); err != nil {
			return err
		}
		if len(s.Properties) == 0 || len(s.Properties) > 128 {
			return problem(400, "invalid_fields", "属性数必须为1到128")
		}
		fields := map[string]contracts.Property{}
		for _, p := range s.Properties {
			fields[p.ID] = p
		}
		names := map[string]bool{}
		ids := map[string]bool{}
		for i := range s.Indexes {
			idx := &s.Indexes[i]
			if idx.ID == "" && assignIDs {
				idx.ID = contracts.NewID()
			}
			var key string
			idx.Name, key, err = normalizeName(idx.Name)
			if err != nil {
				return err
			}
			if idx.ID == "" || ids[idx.ID] || names[key] || len(idx.FieldIDs) == 0 || len(idx.FieldIDs) > 8 {
				return problem(400, "invalid_index", "索引标识、名称或字段集合不合法")
			}
			ids[idx.ID] = true
			names[key] = true
			seen := map[string]bool{}
			for _, id := range idx.FieldIDs {
				field, ok := fields[id]
				if !ok || seen[id] {
					return problem(400, "invalid_index", "索引引用未知或重复属性")
				}
				if field.DataType == "text" || field.DataType == "longtext" {
					return problem(422, "invalid_index", "text / longtext 属性不支持索引")
				}
				seen[id] = true
			}
		}
	case contracts.KindEnum:
		if d.Enum == nil {
			return invalidBody()
		}
		if len(d.Enum.Values) == 0 || len(d.Enum.Values) > 1024 {
			return problem(400, "invalid_enum", "枚举值数量必须为1到1024")
		}
		codes := map[string]bool{}
		for i := range d.Enum.Values {
			v := &d.Enum.Values[i]
			if v.Code == "" || len(v.Code) > 128 || codes[v.Code] || strings.TrimSpace(v.Code) != v.Code {
				return problem(400, "invalid_enum", "枚举code为空、过长或重复")
			}
			codes[v.Code] = true
			v.Label, _, err = normalizeName(v.Label)
			if err != nil {
				return err
			}
		}
	case contracts.KindLink:
		if d.Link == nil {
			return invalidBody()
		}
		l := d.Link
		if l.SourceSchemaID == "" || l.TargetSchemaID == "" {
			return problem(400, "invalid_endpoint", "关系两端必须引用Schema ID")
		}
		if l.ForwardName == "" {
			l.ForwardName = d.Name
		}
		if l.ReverseName == "" {
			l.ReverseName = l.ForwardName + "（反向）"
		}
		l.ForwardName, _, err = normalizeName(l.ForwardName)
		if err != nil {
			return err
		}
		l.ReverseName, _, err = normalizeName(l.ReverseName)
		if err != nil {
			return err
		}
		if l.Cardinality != "1:1" && l.Cardinality != "1:N" && l.Cardinality != "N:1" && l.Cardinality != "N:M" {
			return problem(400, "invalid_cardinality", "关系基数不合法")
		}
		if l.OnDelete != "restrict" {
			return problem(422, "unsupported_capability", "首批仅支持restrict删除策略")
		}
		if err := canonicalLinkMapping(l); err != nil {
			return err
		}
	case contracts.KindAction, contracts.KindFunction:
		c := d.Action
		if d.Kind == contracts.KindFunction {
			c = d.Function
		}
		if c == nil {
			return invalidBody()
		}
		if d.Kind == contracts.KindFunction && len(c.TargetSchemaIDs) != 0 {
			return problem(422, "unsupported_action_target", "关联对象仅支持Action定义，Function不接受targetSchemaIds")
		}
		if len(c.TargetSchemaIDs) > 32 {
			return problem(422, "action_target_budget", "每个动作最多关联32个对象模型")
		}
		targets := map[string]bool{}
		for _, id := range c.TargetSchemaIDs {
			if id == "" || len(id) > 128 || !utf8.ValidString(id) || strings.TrimSpace(id) != id || strings.IndexFunc(id, unicode.IsControl) >= 0 || targets[strings.ToLower(id)] {
				return problem(400, "invalid_action_target", "关联对象标识不能为空、重复、超长或包含首尾空白及控制字符")
			}
			targets[strings.ToLower(id)] = true
		}
		// Targets are a set; equivalent selections must retain the same digest.
		sort.Strings(c.TargetSchemaIDs)
		if err := validateProperties(c.Input, assignIDs); err != nil {
			return err
		}
		if err := validateProperties(c.Output, assignIDs); err != nil {
			return err
		}
		if err := contracts.ValidateCallableTestCases(c); err != nil {
			return problem(422, "invalid_test_cases", err.Error())
		}
		for _, ps := range [][]contracts.Property{c.Input, c.Output} {
			for _, property := range ps {
				if property.Mapping != nil {
					return problem(422, "invalid_mapping", "函数或动作参数不能配置存储列映射")
				}
			}
		}
		if c.SideEffects != "none" && c.SideEffects != "dataWrite" && c.SideEffects != "externalWrite" {
			return problem(400, "invalid_side_effects", "必须显式声明none、dataWrite或externalWrite")
		}
		if err := canonicalDataWrite(c); err != nil {
			return err
		}
		if c.FunctionID != "" && c.ArtifactRef != "" {
			return problem(422, "unsupported_runtime", "实现函数与产物引用不能同时配置")
		}
		if c.Runtime != nil {
			if c.SideEffects == "externalWrite" || c.ArtifactRef != "" || c.FunctionID != "" {
				return problem(422, "unsupported_runtime", "源码支持无副作用计算或已声明范围的事务写入，不能绑定产物、其他函数或修改外部系统")
			}
			if err := contracts.ValidateScriptRuntime(c.Runtime); err != nil {
				return problem(422, "invalid_runtime", err.Error())
			}
			for _, ps := range [][]contracts.Property{c.Input, c.Output} {
				if len(ps) > 256 {
					return problem(422, "parameter_budget", "源码输入与输出分别最多256个参数")
				}
				for _, property := range ps {
					if property.EnumID != "" {
						return problem(422, "unsupported_parameters", "源码运行参数尚不支持Enum引用")
					}
				}
			}
		}
	default:
		return problem(400, "invalid_kind", "未知定义类型")
	}
	return nil
}

func invalidBody() error { return problem(400, "invalid_definition", "定义正文与kind不匹配") }

func validateProperties(ps []contracts.Property, assignIDs bool) error {
	names := map[string]bool{}
	ids := map[string]bool{}
	idKeys := map[string]bool{}
	for i := range ps {
		p := &ps[i]
		if p.ID == "" && assignIDs {
			p.ID = contracts.NewID()
		}
		name, key, err := normalizeName(p.Name)
		if err != nil {
			return err
		}
		p.Name = name
		idKey := cases.Fold().String(norm.NFC.String(strings.TrimSpace(p.ID)))
		if p.ID == "" || len(p.ID) > 128 || ids[p.ID] || idKeys[idKey] || names[key] {
			return problem(400, "invalid_fields", "属性标识或中文名称重复/缺失")
		}
		if names[idKey] || idKeys[key] {
			return problem(400, "ambiguous_field_reference", "属性名称不能与另一属性的稳定ID重叠")
		}
		names[key] = true
		ids[p.ID] = true
		idKeys[idKey] = true
		switch p.DataType {
		case "string", "text", "longtext", "int64", "decimal", "boolean", "timestamp", "uuid":
		default:
			return problem(422, "unsupported_type", fmt.Sprintf("暂不支持属性类型%s", p.DataType))
		}
		if p.DataType == "decimal" && (p.Precision < 1 || p.Precision > 38 || p.Scale < 0 || p.Scale > p.Precision) {
			return problem(400, "invalid_decimal", "decimal精度必须为1到38且0<=scale<=precision")
		}
		if p.DataType != "decimal" && (p.Precision != 0 || p.Scale != 0) {
			return problem(400, "invalid_precision", "只有decimal可声明precision/scale")
		}
		if p.EnumID != "" && p.DataType != "string" {
			return problem(400, "invalid_enum_reference", "首批Enum编码只允许string属性")
		}
		if len(p.Default) > 0 {
			if !json.Valid(p.Default) {
				return problem(400, "invalid_default", "默认值必须为合法JSON")
			}
			if bytes.Equal(bytes.TrimSpace(p.Default), []byte("null")) && !p.Nullable {
				return problem(400, "invalid_default", "不可空属性不能使用null默认值")
			}
			if (p.DataType == "text" || p.DataType == "longtext") && !bytes.Equal(bytes.TrimSpace(p.Default), []byte("null")) {
				var value string
				limit := contracts.MaxTextBytes
				if p.DataType == "longtext" {
					limit = contracts.MaxLongTextBytes
				}
				if json.Unmarshal(p.Default, &value) != nil || len(value) > limit {
					return problem(400, "invalid_default", "文本默认值的类型或 UTF-8 字节数超限")
				}
			}
			if p.ReadOnly {
				return problem(422, "unsupported_capability", "首批不支持只读属性默认生成器")
			}
		}
	}
	return nil
}

func dependencyIDs(d contracts.Definition) []string {
	seen := map[string]bool{}
	addProps := func(ps []contracts.Property) {
		for _, p := range ps {
			if p.EnumID != "" {
				seen[p.EnumID] = true
			}
		}
	}
	if d.Schema != nil {
		addProps(d.Schema.Properties)
	}
	if d.Link != nil {
		seen[d.Link.SourceSchemaID] = true
		seen[d.Link.TargetSchemaID] = true
	}
	for _, c := range []*contracts.Callable{d.Action, d.Function} {
		if c != nil {
			addProps(c.Input)
			addProps(c.Output)
			for _, id := range c.TargetSchemaIDs {
				seen[id] = true
			}
			for _, item := range c.DataWrite {
				seen[item.SchemaID] = true
			}
			if c.FunctionID != "" {
				seen[c.FunctionID] = true
			}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids
}

// compatibleUpdate supports display edits and separately checked safe additions.
// Endpoint, constraint and existing storage changes require migration support.
func compatibleUpdate(old, next contracts.Definition) error {
	if old.ID != next.ID || old.TenantID != next.TenantID || old.Kind != next.Kind || old.CategoryID != next.CategoryID || old.GroupID != next.GroupID {
		return problem(422, "unsupported_change", "首批不支持改变类型或组织归属")
	}
	a, b := withoutDisplayMetadata(old), withoutDisplayMetadata(next)
	// Action targets are discoverable metadata. They may be updated through the
	// same draft/publication flow, which rechecks every live dependency. Callable
	// signatures, implementation modes and side effects remain locked.
	if a.Kind == contracts.KindAction && a.Action != nil && b.Action != nil {
		a.Action.TargetSchemaIDs, b.Action.TargetSchemaIDs = nil, nil
		// An inline Action may replace its code, and an unconfigured supported Action
		// may acquire code. Existing implementations cannot switch modes or drop
		// the executable source of an already active Action.
		if b.Action.Runtime != nil && a.Action.SideEffects == b.Action.SideEffects && (a.Action.SideEffects == "none" || a.Action.SideEffects == "dataWrite") && a.Action.FunctionID == "" && b.Action.FunctionID == "" && a.Action.ArtifactRef == "" && b.Action.ArtifactRef == "" {
			a.Action.Runtime, b.Action.Runtime = nil, nil
		}
	}
	// A supported Function may acquire or replace its inline implementation while
	// preserving its stable signature and write declaration. Started invocations own an
	// immutable snapshot; later invocations resolve the new current definition.
	if a.Kind == contracts.KindFunction && a.Function != nil && b.Function != nil && a.Function.SideEffects == b.Function.SideEffects && (a.Function.SideEffects == "none" || a.Function.SideEffects == "dataWrite") && a.Function.ArtifactRef == "" && b.Function.ArtifactRef == "" {
		a.Function.Runtime, b.Function.Runtime = nil, nil
	}
	if !reflect.DeepEqual(a, b) {
		if compatibleLinkBindingAddition(a, b) {
			return nil
		}
		if compatibleMappedAddition(a, b) {
			return nil
		}
		return problem(422, "migration_required", "首批只开放名称、描述和标签等展示变更；结构变化需要迁移能力")
	}
	return nil
}

func withoutDisplayMetadata(d contracts.Definition) contracts.Definition {
	// Copy the wire object before clearing display fields so the caller's
	// current/draft pointers remain untouched.
	raw, _ := json.Marshal(d)
	var copied contracts.Definition
	_ = json.Unmarshal(raw, &copied)
	copied.Name, copied.Description = "", ""
	clearProperties := func(ps []contracts.Property) {
		for i := range ps {
			ps[i].Name = ""
		}
	}
	if copied.Schema != nil {
		clearProperties(copied.Schema.Properties)
		for i := range copied.Schema.Indexes {
			copied.Schema.Indexes[i].Name = ""
		}
	}
	if copied.Enum != nil {
		for i := range copied.Enum.Values {
			copied.Enum.Values[i].Label = ""
		}
	}
	if copied.Link != nil {
		copied.Link.ForwardName, copied.Link.ReverseName = "", ""
	}
	for _, c := range []*contracts.Callable{copied.Action, copied.Function} {
		if c != nil {
			clearProperties(c.Input)
			clearProperties(c.Output)
			c.TestCases = nil
		}
	}
	return copied
}
