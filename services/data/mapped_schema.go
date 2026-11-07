package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const maxMappedTables = 8

var managedIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

func mappedName(value string) bool {
	return value != "" && len(value) <= 128 && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

// Logical validation stays shared with managed platform schemas; physical source
// facts and unique association keys are checked against the authoritative catalog.
func validateMappedDefinition(d c.Definition) error {
	s := d.Schema
	if s == nil || s.StorageBinding == nil || (s.Mode != "schema_2_db" && s.Mode != "db_2_schema") {
		return problem(422, "invalid_mapping", "映射 Schema 必须声明建模方向及主表绑定")
	}
	b := s.StorageBinding
	if !uuidPattern.MatchString(b.DataSourceID) || !mappedName(b.Table) || (b.Namespace != "" && !mappedName(b.Namespace)) || (b.IDColumn != "" && !mappedName(b.IDColumn)) || (b.TenantColumn != "" && !mappedName(b.TenantColumn)) {
		return problem(422, "invalid_mapping", "主表的数据源、表或主键映射不合法")
	}
	plain := *s
	plain.Mode, plain.StorageBinding = "schema_2_db", nil
	plain.Properties = append([]c.Property(nil), s.Properties...)
	for i, p := range s.Properties {
		if s.Mode == "schema_2_db" && len(p.Default) != 0 {
			return problem(422, "unsupported_default", "映射正向建模尚未开放物理默认值编译，不能忽略默认值发布")
		}
		m := p.Mapping
		if m == nil || !uuidPattern.MatchString(m.DataSourceID) || !mappedName(m.Column) || (m.Namespace != "" && !mappedName(m.Namespace)) || (m.Table != "" && !mappedName(m.Table)) || (m.KeyColumn != "" && !mappedName(m.KeyColumn)) || (m.TenantColumn != "" && !mappedName(m.TenantColumn)) {
			return problem(422, "invalid_mapping", "每个对象属性都必须具有有效的数据源与列映射")
		}
		if m.DataSourceID != b.DataSourceID && (m.Table == "" || m.KeyColumn == "") {
			return problem(422, "invalid_mapping", "跨数据源属性必须声明来源表与唯一关联键")
		}
		if m.Table != "" && (m.Table != b.Table || m.DataSourceID != b.DataSourceID || m.Namespace != "" && m.Namespace != b.Namespace) && m.KeyColumn == "" {
			return problem(422, "invalid_mapping", "跨表属性必须声明唯一关联键")
		}
		plain.Properties[i].Mapping = nil
		if s.Mode == "db_2_schema" {
			plain.Properties[i].ReadOnly = false
		}
	}
	d.Schema = &plain
	return validateSchema(d)
}

type mappedTable struct {
	db           *sql.DB
	source       c.DataSource
	namespace    string
	name         string
	keyColumn    string
	tenantColumn string
	primary      bool
	properties   []c.Property
	catalog      c.SourceTable
	exists       bool
	managed      bool
}

func (t mappedTable) locator() string { return t.source.ID + "\x00" + t.namespace + "\x00" + t.name }
func (t mappedTable) qualified() string {
	ns, _ := sourceQuote(t.source.Driver, t.namespace)
	name, _ := sourceQuote(t.source.Driver, t.name)
	return ns + "." + name
}
func (t mappedTable) quoted(name string) string {
	value, _ := sourceQuote(t.source.Driver, name)
	return value
}
func (t mappedTable) column(name string) (c.SourceColumn, bool) {
	for _, column := range t.catalog.Columns {
		if column.Name == name {
			return column, true
		}
	}
	return c.SourceColumn{}, false
}

func closeMappedTables(tables []*mappedTable) {
	closed := map[*sql.DB]bool{}
	for _, table := range tables {
		if table.db != nil && !closed[table.db] {
			_ = table.db.Close()
			closed[table.db] = true
		}
	}
}

func (h *handler) mappedTables(ctx context.Context, d c.Definition, allowMissing bool) ([]*mappedTable, error) {
	if err := validateMappedDefinition(d); err != nil {
		return nil, err
	}
	b := d.Schema.StorageBinding
	tables := []*mappedTable{}
	sources := map[string]*mappedTable{}
	fail := func(err error) ([]*mappedTable, error) { closeMappedTables(tables); return nil, err }
	open := func(sourceID, namespace, name, key, tenant string, primary bool) (*mappedTable, error) {
		connection := sources[sourceID]
		if connection == nil {
			db, source, err := h.openSource(ctx, d.TenantID, sourceID)
			if err != nil {
				return nil, err
			}
			connection = &mappedTable{db: db, source: source}
			sources[sourceID] = connection
			// Retain newly opened connections even if validation below fails.
			tables = append(tables, connection)
		}
		if namespace == "" {
			if connection.source.Driver == "mysql" {
				namespace = connection.source.Database
			} else {
				namespace = "public"
			}
		}
		if key == "" && primary {
			key = "id"
		}
		if connection.source.TenantIsolation == "column" && tenant == "" {
			if d.Schema.Mode == "schema_2_db" {
				tenant = "tenant_id"
			} else {
				return nil, problem(422, "tenant_mapping_required", "共享来源必须显式指定租户列")
			}
		}
		if connection.source.TenantIsolation == "dedicated" && tenant != "" {
			return nil, problem(422, "invalid_mapping", "独占数据源不应混用租户列映射")
		}
		if !mappedName(key) || tenant == key || !mappedName(namespace) || !mappedName(name) {
			return nil, problem(422, "invalid_mapping", "来源表、唯一关联键或租户列不合法")
		}
		for _, table := range tables {
			if table.name == name && table.namespace == namespace && table.source.ID == sourceID {
				if table.keyColumn != key || table.tenantColumn != tenant {
					return nil, problem(422, "inconsistent_mapping", "同一来源表的关联键和租户列必须一致")
				}
				return table, nil
			}
		}
		if d.Schema.Mode == "schema_2_db" {
			for _, identifier := range []string{name, key, tenant} {
				if identifier != "" && !managedIdentifier.MatchString(identifier) {
					return nil, problem(422, "invalid_identifier", "正向建模的物理表列名须为63字节内的字母、数字和下划线")
				}
			}
		}
		table := &mappedTable{db: connection.db, source: connection.source, namespace: namespace, name: name, keyColumn: key, tenantColumn: tenant, primary: primary, managed: d.Schema.Mode == "schema_2_db"}
		if connection.name == "" {
			*connection = *table
			table = connection
		} else {
			tables = append(tables, table)
		}
		if len(tables) > maxMappedTables {
			return nil, problem(422, "budget_exceeded", "每个 Schema 最多映射8张来源表")
		}
		return table, nil
	}
	primary, err := open(b.DataSourceID, b.Namespace, b.Table, b.IDColumn, b.TenantColumn, true)
	if err != nil {
		return fail(err)
	}
	for _, property := range d.Schema.Properties {
		m := property.Mapping
		namespace, name, key, tenant := m.Namespace, m.Table, m.KeyColumn, m.TenantColumn
		if name == "" {
			if m.DataSourceID != b.DataSourceID {
				return fail(problem(422, "invalid_mapping", "其他数据源必须指定来源表"))
			}
			name = b.Table
		}
		if m.DataSourceID == b.DataSourceID && name == b.Table && (namespace == "" || namespace == b.Namespace || namespace == primary.namespace) {
			namespace, key, tenant = primary.namespace, primary.keyColumn, primary.tenantColumn
			if m.KeyColumn != "" && m.KeyColumn != key || m.TenantColumn != "" && m.TenantColumn != tenant {
				return fail(problem(422, "inconsistent_mapping", "主表属性的关联键和租户列必须服从主表绑定"))
			}
		}
		table, err := open(m.DataSourceID, namespace, name, key, tenant, false)
		if err != nil {
			return fail(err)
		}
		if !table.primary && !property.Nullable {
			return fail(problem(422, "nullable_mapping_required", "其他来源可能没有对应行，跨来源属性必须允许空值"))
		}
		if property.Mapping.Column == table.tenantColumn {
			return fail(problem(422, "reserved_column", "租户隔离列不能作为业务属性映射"))
		}
		for _, existing := range table.properties {
			if existing.Mapping.Column == m.Column {
				return fail(problem(422, "duplicate_mapping", "同一来源列不能重复映射为多个属性"))
			}
		}
		if d.Schema.Mode == "schema_2_db" && !managedIdentifier.MatchString(m.Column) {
			return fail(problem(422, "invalid_identifier", "正向属性列名须为63字节内的字母、数字和下划线"))
		}
		table.properties = append(table.properties, property)
	}
	for _, table := range tables {
		if table.source.Driver == "mysql" {
			columnNames := map[string]string{}
			for _, name := range []string{table.keyColumn, table.tenantColumn} {
				if name == "" {
					continue
				}
				folded := strings.ToLower(name)
				if old, ok := columnNames[folded]; ok && old != name {
					return fail(problem(422, "duplicate_mapping", "MySQL物理列名不区分大小写，主键与租户列不能冲突"))
				}
				columnNames[folded] = name
			}
			for _, property := range table.properties {
				name := property.Mapping.Column
				folded := strings.ToLower(name)
				if old, ok := columnNames[folded]; ok && old != name {
					return fail(problem(422, "duplicate_mapping", "MySQL物理列名不区分大小写，请使用唯一的规范列名"))
				}
				columnNames[folded] = name
			}
		}
		catalog, err := readSourceTable(ctx, table.db, table.source, table.namespace, table.name)
		if err != nil {
			var failure *apiError
			if allowMissing && errors.As(err, &failure) && failure.status == 404 {
				continue
			}
			return fail(err)
		}
		table.catalog, table.exists = catalog, true
	}
	physicalTables := map[string]bool{}
	for _, table := range tables {
		namespace, name := table.namespace, table.name
		if table.source.Driver == "mysql" {
			namespace, name = strings.ToLower(namespace), strings.ToLower(name)
		}
		key := digest([]string{endpointKey(table.source), namespace, name})
		if physicalTables[key] {
			return fail(problem(422, "duplicate_mapping", "同一物理表不能通过多个数据源别名重复绑定，请复用同一个来源"))
		}
		physicalTables[key] = true
	}
	return tables, nil
}

func mappedPropertyTable(tables []*mappedTable, id string) (*mappedTable, c.Property, bool) {
	for _, table := range tables {
		for _, property := range table.properties {
			if property.ID == id {
				return table, property, true
			}
		}
	}
	return nil, c.Property{}, false
}

func mappedType(driver string, property c.Property) (string, error) {
	if driver == "mysql" {
		value, err := sqlType(property)
		if property.DataType == "uuid" {
			value = "char(36) CHARACTER SET ascii COLLATE ascii_bin"
		}
		return value, err
	}
	switch property.DataType {
	case "string":
		return "varchar(255)", nil
	case "text", "longtext":
		return "text", nil
	case "uuid":
		return "uuid", nil
	case "int64":
		return "bigint", nil
	case "boolean":
		return "boolean", nil
	case "timestamp":
		return "timestamp(6) with time zone", nil
	case "decimal":
		return sqlType(property)
	}
	return "", problem(422, "unsupported_type", "来源数据库不支持此属性类型")
}

func mappedColumnCompatible(property c.Property, column c.SourceColumn, driver string) bool {
	if column.Generated || column.Nullable != property.Nullable {
		return false
	}
	if property.DataType == "uuid" && column.DataType == "string" {
		return column.MaxLength == 36
	}
	if property.DataType == "longtext" && driver == "postgresql" && column.DataType == "text" {
		return true
	}
	if property.DataType != column.DataType {
		return false
	}
	if property.DataType == "decimal" {
		return property.Precision == column.Precision && property.Scale == column.Scale
	}
	if property.DataType == "string" && (column.MaxLength == 0 || column.MaxLength > 255) {
		return false
	}
	return true
}

func sourceKeyType(column c.SourceColumn) string {
	switch column.DataType {
	case "int64":
		return "int64"
	case "string", "uuid":
		return "string"
	}
	return ""
}

func mappedUniqueKey(table *mappedTable) bool {
	for _, index := range table.catalog.Indexes {
		if (!index.Unique && !index.Primary) || index.Predicate != "" {
			continue
		}
		if len(index.Columns) == 1 && index.Columns[0] == table.keyColumn {
			return true
		}
		if table.tenantColumn != "" && len(index.Columns) == 2 && ((index.Columns[0] == table.tenantColumn && index.Columns[1] == table.keyColumn) || (index.Columns[1] == table.tenantColumn && index.Columns[0] == table.keyColumn)) {
			return true
		}
	}
	return false
}

func validateMappedCatalog(tables []*mappedTable) error {
	var keyType string
	for _, table := range tables {
		if !table.exists {
			return problem(409, "source_drift", "来源表不存在，请重新检查映射")
		}
		key, ok := table.column(table.keyColumn)
		if !ok || key.Nullable || !mappedUniqueKey(table) || sourceKeyType(key) == "" {
			return problem(422, "invalid_source_key", "来源表必须具有非空且唯一的字符串或整数关联键")
		}
		if table.primary {
			keyType = sourceKeyType(key)
		} else if keyType != sourceKeyType(key) {
			return problem(422, "incompatible_source_key", "来源唯一关联键类型必须与主表稳定主键匹配")
		}
		if table.tenantColumn != "" {
			tenant, ok := table.column(table.tenantColumn)
			native := strings.ToLower(tenant.DatabaseType)
			if !ok || tenant.Nullable || tenant.Generated || tenant.DataType != "string" || tenant.MaxLength > 255 || strings.HasPrefix(native, "char(") || strings.HasPrefix(native, "character(") {
				return problem(422, "invalid_tenant_mapping", "租户列必须是非空且可比较的字符串列")
			}
		}
		for _, property := range table.properties {
			column, ok := table.column(property.Mapping.Column)
			if table.managed && property.Mapping.Column != table.keyColumn {
				if !ok || !mappedManagedColumnMatches(table, property, column) {
					return problem(409, "source_drift", "已纳管来源列与Schema定义不一致，请核对物理结构漂移")
				}
				continue
			}
			if !table.primary {
				property.Nullable = column.Nullable
			}
			if property.Mapping.Column == table.keyColumn {
				column.Generated = false
			}
			if !ok || !mappedColumnCompatible(property, column, table.source.Driver) {
				return problem(409, "source_drift", "来源列的类型、可空性或生成规则与当前属性映射不一致，请重新发现源结构")
			}
		}
	}
	return nil
}

func mappedCompatibleUpdate(old, next c.Definition) error {
	if old.Schema == nil || old.Schema.StorageBinding == nil || next.Schema == nil || next.Schema.StorageBinding == nil {
		return problem(422, "unsupported_migration", "已有模型不能直接切换数据源绑定")
	}
	if old.Schema.Mode != next.Schema.Mode || old.Schema.StorageLayout != next.Schema.StorageLayout || !reflect.DeepEqual(old.Schema.StorageBinding, next.Schema.StorageBinding) {
		return problem(422, "unsupported_migration", "已有模型不能直接切换方向或主表绑定")
	}
	nextByID := map[string]c.Property{}
	for _, property := range next.Schema.Properties {
		nextByID[property.ID] = property
	}
	oldByID := map[string]bool{}
	for _, before := range old.Schema.Properties {
		after, ok := nextByID[before.ID]
		before.Name, after.Name = "", ""
		if !ok || !reflect.DeepEqual(before, after) {
			return problem(422, "unsupported_migration", "已有属性不能删除、改变类型、约束或物理映射")
		}
		oldByID[before.ID] = true
	}
	for _, property := range next.Schema.Properties {
		if !oldByID[property.ID] && next.Schema.Mode == "schema_2_db" && !property.Nullable {
			return problem(422, "unsafe_migration", "正向追加属性必须允许空值，非空新增需要独立回填迁移")
		}
	}
	for _, before := range old.Schema.Indexes {
		found := false
		for _, after := range next.Schema.Indexes {
			before.Name, after.Name = "", ""
			if before.ID == after.ID && reflect.DeepEqual(before, after) {
				found = true
				break
			}
		}
		if !found {
			return problem(422, "unsupported_migration", "已有索引不能删除或修改")
		}
	}
	return nil
}

func mappedDefinitionJSON(d c.Definition) string { value, _ := json.Marshal(d); return string(value) }

func mappedOwner(d c.Definition) string {
	return "agentforge-owner:" + digest([]string{d.TenantID, d.ID})
}

func sortedMappedProperties(properties []c.Property) []c.Property {
	result := append([]c.Property(nil), properties...)
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func sourceStorageError(message string) error { return fmt.Errorf("%s", message) }
