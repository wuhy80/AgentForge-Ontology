package data

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func nameKey(name string) string {
	return cases.Fold().String(norm.NFC.String(strings.TrimSpace(name)))
}

func digest(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func physical(prefix, id string) string { return prefix + digest(id)[:32] }
func tableName(id string) string        { return physical("af_s_", id) }
func columnName(id string) string       { return physical("f_", id) }
func quote(s string) string             { return "`" + s + "`" } // Only called on generated identifiers.
func bareETag(s string) string          { return strings.Trim(s, `"`) }

func validateSchema(d c.Definition) error {
	if d.Kind != c.KindSchema || d.Schema == nil {
		return problem(422, "unsupported", "该定义不是可存储 Schema")
	}
	if d.Schema.StorageBinding != nil {
		return validateMappedDefinition(d)
	}
	s := d.Schema
	if s.Mode != "schema_2_db" || s.StorageLayout != "single" {
		return problem(422, "unsupported", "首批仅支持 schema_2_db / single")
	}
	if !uuidPattern.MatchString(d.ID) || d.TenantID == "" {
		return problem(422, "invalid_schema", "Schema 必须具有 UUID 和租户")
	}
	if len(s.Properties) == 0 || len(s.Properties) > 100 {
		return problem(422, "invalid_schema", "属性数必须为 1 至 100")
	}
	if len(s.Indexes) > 32 {
		return problem(422, "budget_exceeded", "首批每 Schema 最多 32 个索引")
	}
	rowBytes := 256 // Conservative allowance for system columns and row metadata.
	inlineBytes := 256
	ids, names := map[string]bool{}, map[string]bool{}
	for _, p := range s.Properties {
		if !uuidPattern.MatchString(p.ID) || ids[p.ID] {
			return problem(422, "invalid_schema", "属性 ID 必须唯一且有效")
		}
		ids[p.ID] = true
	}
	nameIDs := map[string]string{}
	for _, p := range s.Properties {
		nameIDs[nameKey(p.ID)] = p.ID
	}
	for _, p := range s.Properties {
		key := nameKey(p.Name)
		if key == "" || names[key] || utf8.RuneCountInString(p.Name) > 128 || strings.IndexFunc(p.Name, unicode.IsControl) >= 0 {
			return problem(422, "invalid_schema", "属性 ID 和名称必须唯一且有效")
		}
		if id, ok := nameIDs[key]; ok && id != p.ID {
			return problem(422, "ambiguous_field", "属性名称不能与另一属性 ID 冲突")
		}
		names[key] = true
		if p.EnumID != "" {
			return problem(422, "unsupported", "首批尚未开放 Enum 属性的跨定义协调")
		}
		if p.ReadOnly && len(p.Default) == 0 && (p.Required || !p.Nullable) {
			return problem(422, "invalid_schema", "只读必填或非空属性需要显式默认值")
		}
		if _, err := sqlType(p); err != nil {
			return err
		}
		switch p.DataType {
		case "string":
			rowBytes += 1022
			inlineBytes += 42 // DYNAMIC: at most 40 inline bytes or a 20-byte external pointer, plus length.
		case "uuid":
			rowBytes += 36 // UUID values are canonical ASCII, not utf8mb4 CHAR.
			inlineBytes += 40
		case "text", "longtext":
			rowBytes += 12
			inlineBytes += 42
		default:
			rowBytes += 24
			inlineBytes += 24
		}
		if len(p.Default) > 0 {
			if _, err := typedValue(p, p.Default); err != nil {
				return err
			}
		}
	}
	if rowBytes > 60000 {
		return problem(422, "budget_exceeded", "属性组合超过首批安全行宽预算")
	}
	if inlineBytes > 7000 {
		return problem(422, "budget_exceeded", "属性组合超过首批安全页内预算")
	}
	indexIDs := map[string]bool{}
	for _, idx := range s.Indexes {
		if !uuidPattern.MatchString(idx.ID) || indexIDs[idx.ID] || len(idx.FieldIDs) == 0 || len(idx.FieldIDs) > 2 {
			return problem(422, "invalid_index", "索引 ID 必须唯一，首批每索引支持 1 至 2 个字段")
		}
		indexIDs[idx.ID] = true
		seen := map[string]bool{}
		for _, id := range idx.FieldIDs {
			p, ok := findProperty(s, id)
			if !ok || seen[id] || p.DataType == "text" || p.DataType == "longtext" {
				return problem(422, "invalid_index", "索引字段不存在、重复或不支持索引")
			}
			seen[id] = true
		}
	}
	return nil
}

func findProperty(s *c.Schema, key string) (c.Property, bool) {
	var found c.Property
	matched := false
	for _, p := range s.Properties {
		if p.ID == key || nameKey(p.Name) == nameKey(key) {
			if matched && found.ID != p.ID {
				return c.Property{}, false
			}
			found, matched = p, true
		}
	}
	return found, matched
}
func sqlType(p c.Property) (string, error) {
	switch p.DataType {
	case "string":
		return "varchar(255)", nil
	case "text":
		return "text", nil
	case "longtext":
		return "longtext", nil
	case "uuid":
		return "char(36)", nil
	case "int64":
		return "bigint", nil
	case "boolean":
		return "tinyint(1)", nil
	case "timestamp":
		return "datetime(6)", nil
	case "decimal":
		if p.Precision < 1 || p.Precision > 38 || p.Scale < 0 || p.Scale > p.Precision {
			return "", problem(422, "invalid_type", "decimal 需声明 1..38 精度及合法小数位")
		}
		return fmt.Sprintf("decimal(%d,%d)", p.Precision, p.Scale), nil
	default:
		return "", problem(422, "unsupported", "不支持该属性类型: "+p.DataType)
	}
}

func ddl(d c.Definition) (string, error) {
	if err := validateSchema(d); err != nil {
		return "", err
	}
	cols := []string{"`tenant_id` varbinary(128) NOT NULL", "`id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL", "`etag` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL"}
	for _, p := range d.Schema.Properties {
		t, _ := sqlType(p)
		if p.DataType == "uuid" {
			t += " CHARACTER SET ascii COLLATE ascii_bin"
		}
		nullable := " NOT NULL"
		if p.Nullable {
			nullable = " NULL"
		}
		cols = append(cols, quote(columnName(p.ID))+" "+t+nullable)
	}
	cols = append(cols, "PRIMARY KEY (`tenant_id`,`id`)")
	for _, idx := range d.Schema.Indexes {
		fields := []string{"`tenant_id`"}
		for _, id := range idx.FieldIDs {
			fields = append(fields, quote(columnName(id)))
		}
		kind := "KEY "
		if idx.Unique {
			kind = "UNIQUE KEY "
		}
		cols = append(cols, kind+quote(physical("ix_", idx.ID))+" ("+strings.Join(fields, ",")+")")
	}
	return "CREATE TABLE " + quote(tableName(d.ID)) + " (" + strings.Join(cols, ",") + ") ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin", nil
}

// Storage signature deliberately excludes presentation names. No automatic ALTER
// is attempted in this increment: unsupported changes cannot pause a live model.
func storageSignature(d c.Definition) string {
	s := *d.Schema
	s.Properties = append([]c.Property(nil), s.Properties...)
	s.Indexes = append([]c.Index(nil), s.Indexes...)
	for i := range s.Properties {
		s.Properties[i].Name = ""
	}
	for i := range s.Indexes {
		s.Indexes[i].Name = ""
	}
	sort.Slice(s.Properties, func(i, j int) bool { return s.Properties[i].ID < s.Properties[j].ID })
	sort.Slice(s.Indexes, func(i, j int) bool { return s.Indexes[i].ID < s.Indexes[j].ID })
	return digest(s)
}

func typedValue(p c.Property, raw json.RawMessage) (any, error) {
	if string(raw) == "null" {
		if !p.Nullable {
			return nil, problem(422, "invalid_value", p.Name+" 不允许 null")
		}
		return nil, nil
	}
	bad := func() (any, error) { return nil, problem(422, "invalid_value", p.Name+" 类型或范围不合法") }
	var text string
	switch p.DataType {
	case "string", "text", "longtext", "uuid", "timestamp", "decimal", "int64":
		if json.Unmarshal(raw, &text) != nil {
			return bad()
		}
	case "boolean":
		var b bool
		if json.Unmarshal(raw, &b) != nil {
			return bad()
		}
		return b, nil
	default:
		return bad()
	}
	switch p.DataType {
	case "string":
		if len([]rune(text)) > 255 {
			return bad()
		}
		return text, nil
	case "text":
		if len(text) > c.MaxTextBytes {
			return bad()
		}
		return text, nil
	case "longtext":
		if len(text) > c.MaxLongTextBytes {
			return bad()
		}
		return text, nil
	case "uuid":
		if !uuidPattern.MatchString(text) {
			return bad()
		}
		return strings.ToLower(text), nil
	case "int64":
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil || strconv.FormatInt(n, 10) != text {
			return bad()
		}
		return n, nil
	case "timestamp":
		t, err := time.Parse(time.RFC3339Nano, text)
		if err != nil || t.Nanosecond()%1000 != 0 || t.UTC().Year() < 1000 || t.UTC().Year() > 9999 {
			return bad()
		}
		return t.UTC().Format("2006-01-02 15:04:05.000000"), nil
	case "decimal":
		if !decimalPattern.MatchString(text) {
			return bad()
		}
		parts := strings.Split(strings.TrimPrefix(text, "-"), ".")
		whole := strings.TrimLeft(parts[0], "0")
		fraction := 0
		if len(parts) == 2 {
			fraction = len(parts[1])
		}
		if len(whole) > p.Precision-p.Scale || fraction > p.Scale {
			return bad()
		}
		return text, nil
	}
	return bad()
}

func rawDatabaseValue(p c.Property, v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage("null"), nil
	}
	var s string
	switch x := v.(type) {
	case []byte:
		s = string(x)
	case string:
		s = x
	case int64:
		s = strconv.FormatInt(x, 10)
	case bool:
		if x {
			s = "1"
		} else {
			s = "0"
		}
	case time.Time:
		s = x.UTC().Format(time.RFC3339Nano)
	default:
		return nil, fmt.Errorf("unsupported database value %T", v)
	}
	if p.DataType == "boolean" {
		if s == "1" {
			return json.RawMessage("true"), nil
		}
		if s == "0" {
			return json.RawMessage("false"), nil
		}
		return nil, fmt.Errorf("invalid boolean in database")
	}
	if p.DataType == "timestamp" {
		if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
			t, e := time.ParseInLocation("2006-01-02 15:04:05.999999", s, time.UTC)
			if e != nil {
				return nil, e
			}
			s = t.Format(time.RFC3339Nano)
		}
	}
	b, err := json.Marshal(s)
	return b, err
}
