package data

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func linkTableName(id string) string { return physical("af_l_", id) }

func validateLinkDefinition(d c.Definition) error {
	if d.Kind != c.KindLink || d.Link == nil || !uuidPattern.MatchString(d.ID) || d.TenantID == "" {
		return problem(422, "invalid_link", "需要有效的 Link 定义")
	}
	l := d.Link
	if !uuidPattern.MatchString(l.SourceSchemaID) || !uuidPattern.MatchString(l.TargetSchemaID) || l.OnDelete != "restrict" {
		return problem(422, "invalid_link", "关系端点和删除策略不合法")
	}
	if l.Cardinality != "1:1" && l.Cardinality != "1:N" && l.Cardinality != "N:1" && l.Cardinality != "N:M" {
		return problem(422, "invalid_link", "不支持的关系基数")
	}
	b := l.StorageBinding
	if b == nil {
		return problem(422, "link_mapping_required", "此 Link 尚未配置关系存储映射，不能查询或维护关系事实")
	}
	switch b.Mode {
	case "managed":
		if b.DataSourceID != "" || b.Namespace != "" || b.Table != "" || b.SourceColumn != "" || b.TargetColumn != "" || b.TenantColumn != "" || b.ForeignKeySide != "" || b.ForeignKeyColumn != "" {
			return problem(422, "invalid_link_mapping", "平台关联表不接受外部物理绑定")
		}
	case "junction":
		if !uuidPattern.MatchString(b.DataSourceID) || !mappedName(b.Table) || !mappedName(b.SourceColumn) || !mappedName(b.TargetColumn) || b.SourceColumn == b.TargetColumn || (b.Namespace != "" && !mappedName(b.Namespace)) || (b.TenantColumn != "" && (!mappedName(b.TenantColumn) || b.TenantColumn == b.SourceColumn || b.TenantColumn == b.TargetColumn)) || b.ForeignKeySide != "" || b.ForeignKeyColumn != "" {
			return problem(422, "invalid_link_mapping", "关联表映射须指定数据源、表及不同的两端列")
		}
	case "foreign_key":
		if !uuidPattern.MatchString(b.DataSourceID) || !mappedName(b.ForeignKeyColumn) || (b.ForeignKeySide != "source" && b.ForeignKeySide != "target") || b.Namespace != "" || b.Table != "" || b.SourceColumn != "" || b.TargetColumn != "" || b.TenantColumn != "" {
			return problem(422, "invalid_link_mapping", "外键映射须指定数据源、持有端和物理外键列")
		}
		if (b.ForeignKeySide == "source" && l.Cardinality != "1:1" && l.Cardinality != "N:1") || (b.ForeignKeySide == "target" && l.Cardinality != "1:1" && l.Cardinality != "1:N") {
			return problem(422, "invalid_link_mapping", "外键持有端与关系基数不匹配")
		}
	default:
		return problem(422, "unsupported_link_mapping", "不支持的关系存储方式")
	}
	return nil
}

type linkPlan struct {
	definition                 c.Definition
	source, target             c.CurrentDefinition
	sourceTables, targetTables []*mappedTable
	junction                   *mappedTable
}

func (p *linkPlan) close() {
	closeMappedTables(p.sourceTables)
	closeMappedTables(p.targetTables)
}

func (h *handler) planLink(ctx context.Context, definition c.Definition) (*linkPlan, error) {
	if err := validateLinkDefinition(definition); err != nil {
		return nil, err
	}
	p := &linkPlan{definition: definition}
	var err error
	p.source, err = h.current(ctx, definition.TenantID, definition.Link.SourceSchemaID)
	if err != nil {
		return nil, err
	}
	p.target, err = h.current(ctx, definition.TenantID, definition.Link.TargetSchemaID)
	if err != nil {
		return nil, err
	}
	for _, current := range []c.CurrentDefinition{p.source, p.target} {
		if err = validateSchema(current.Definition); err != nil {
			return nil, err
		}
	}
	if p.source.Definition.ID != definition.Link.SourceSchemaID || p.target.Definition.ID != definition.Link.TargetSchemaID {
		return nil, problem(503, "metadata_unavailable", "关系端点权威响应标识不匹配")
	}
	b := definition.Link.StorageBinding
	if b.Mode == "managed" {
		if p.source.Definition.Schema.StorageBinding != nil || p.target.Definition.Schema.StorageBinding != nil {
			return nil, problem(422, "unsupported_link_mapping", "平台关联表仅支持两个使用平台默认存储的对象模型")
		}
		return p, nil
	}
	sb, tb := p.source.Definition.Schema.StorageBinding, p.target.Definition.Schema.StorageBinding
	if sb == nil || tb == nil || sb.DataSourceID != tb.DataSourceID || sb.DataSourceID != b.DataSourceID {
		return nil, problem(422, "cross_source_link_unsupported", "外部 Link 当前仅支持两端主表和关系表位于同一数据源，跨数据源关系明确拒绝")
	}
	fail := func(err error) (*linkPlan, error) { p.close(); return nil, err }
	p.sourceTables, err = h.mappedTables(ctx, p.source.Definition, false)
	if err != nil {
		return fail(err)
	}
	p.targetTables, err = h.mappedTables(ctx, p.target.Definition, false)
	if err != nil {
		return fail(err)
	}
	if err = validateMappedCatalog(p.sourceTables); err != nil {
		return fail(err)
	}
	if err = validateMappedCatalog(p.targetTables); err != nil {
		return fail(err)
	}
	for _, endpoint := range []struct {
		current c.CurrentDefinition
		tables  []*mappedTable
	}{{p.source, p.sourceTables}, {p.target, p.targetTables}} {
		if endpoint.current.Definition.Schema.Mode == "db_2_schema" {
			if err = validateReverseIndexes(endpoint.current.Definition, endpoint.tables); err != nil {
				return fail(err)
			}
		} else {
			for _, table := range endpoint.tables {
				remaining, e := mappedTablePlan(ctx, endpoint.current.Definition, endpoint.tables, table)
				if e != nil {
					return fail(e)
				}
				if len(remaining) != 0 {
					return fail(problem(409, "source_drift", "关系端点的已发布物理结构缺失"))
				}
			}
		}
	}
	source, target := p.sourceTables[0], p.targetTables[0]
	if b.Mode == "junction" {
		namespace, err := sourceNamespace(source.source, b.Namespace)
		if err != nil {
			return fail(err)
		}
		catalog, err := readSourceTable(ctx, source.db, source.source, namespace, b.Table)
		if err != nil {
			return fail(err)
		}
		p.junction = &mappedTable{db: source.db, source: source.source, namespace: namespace, name: b.Table, tenantColumn: b.TenantColumn, catalog: catalog, exists: true}
		if source.source.TenantIsolation == "column" && b.TenantColumn == "" {
			return fail(problem(422, "tenant_mapping_required", "共享关系表必须指定租户列"))
		}
		if source.source.TenantIsolation == "dedicated" && b.TenantColumn != "" {
			return fail(problem(422, "invalid_tenant_mapping", "专属关系表不能混用租户列"))
		}
		for _, pair := range []struct {
			table  *mappedTable
			column string
		}{{source, b.SourceColumn}, {target, b.TargetColumn}} {
			column, ok := p.junction.column(pair.column)
			key, _ := pair.table.column(pair.table.keyColumn)
			if !ok || column.Nullable || column.Generated || sourceKeyType(column) == "" || sourceKeyType(column) != sourceKeyType(key) {
				return fail(problem(422, "incompatible_link_key", "关系表端点列必须非空且类型与对应对象主键匹配"))
			}
		}
		if p.junction.tenantColumn != "" {
			column, ok := p.junction.column(p.junction.tenantColumn)
			native := strings.ToLower(column.DatabaseType)
			if !ok || column.Nullable || column.Generated || column.DataType != "string" || column.MaxLength > 255 || strings.HasPrefix(native, "char(") || strings.HasPrefix(native, "character(") {
				return fail(problem(422, "invalid_tenant_mapping", "关系表租户列必须为非空字符串列"))
			}
		}
		if !linkUniqueColumns(p.junction, []string{b.SourceColumn, b.TargetColumn}) {
			return fail(problem(422, "link_unique_key_required", "关系表必须以租户范围内两端列建立唯一约束，不能保存重复边"))
		}
		if (definition.Link.Cardinality == "1:1" || definition.Link.Cardinality == "N:1") && !linkUniqueColumns(p.junction, []string{b.SourceColumn}) {
			return fail(problem(422, "link_cardinality_constraint_required", "当前基数要求关系表来源列在租户内唯一"))
		}
		if (definition.Link.Cardinality == "1:1" || definition.Link.Cardinality == "1:N") && !linkUniqueColumns(p.junction, []string{b.TargetColumn}) {
			return fail(problem(422, "link_cardinality_constraint_required", "当前基数要求关系表目标列在租户内唯一"))
		}
	} else {
		holder, referenced := source, target
		if b.ForeignKeySide == "target" {
			holder, referenced = target, source
		}
		if b.ForeignKeyColumn == holder.tenantColumn {
			return fail(problem(422, "invalid_link_mapping", "外键列不能占用租户隔离列"))
		}
		column, ok := holder.column(b.ForeignKeyColumn)
		key, _ := referenced.column(referenced.keyColumn)
		if !ok || column.Generated || sourceKeyType(column) == "" || sourceKeyType(column) != sourceKeyType(key) {
			return fail(problem(422, "incompatible_link_key", "外键列必须具有与另一端对象主键匹配的类型"))
		}
		if definition.Link.Cardinality == "1:1" && !linkUniqueColumns(holder, []string{b.ForeignKeyColumn}) {
			return fail(problem(422, "link_cardinality_constraint_required", "一对一关系要求外键列在租户范围内唯一"))
		}
	}
	return p, nil
}

func linkUniqueColumns(table *mappedTable, columns []string) bool {
	for _, index := range table.catalog.Indexes {
		if (!index.Unique && !index.Primary) || index.Predicate != "" {
			continue
		}
		actual := append([]string(nil), index.Columns...)
		if table.tenantColumn != "" {
			for i, col := range actual {
				if col == table.tenantColumn {
					actual = append(actual[:i], actual[i+1:]...)
					break
				}
			}
		}
		want := append([]string(nil), columns...)
		sort.Strings(actual)
		sort.Strings(want)
		if strings.Join(actual, "\x00") == strings.Join(want, "\x00") {
			return true
		}
	}
	return false
}

func linkStorageSignature(d c.Definition) string {
	l := *d.Link
	l.ForwardName, l.ReverseName = "", ""
	return digest(l)
}

func linkDDL(d c.Definition) (string, error) {
	if err := validateLinkDefinition(d); err != nil {
		return "", err
	}
	if d.Link.StorageBinding.Mode != "managed" {
		return "", problem(422, "unsupported_link_mapping", "外部关系映射不会创建或修改来源表")
	}
	fields := []string{"`tenant_id` varbinary(128) NOT NULL", "`id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL", "`source_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL", "`target_id` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL", "`etag` char(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL", "PRIMARY KEY (`tenant_id`,`id`)", "UNIQUE KEY `edge_pair` (`tenant_id`,`source_id`,`target_id`)", "KEY `edge_reverse` (`tenant_id`,`target_id`,`source_id`)"}
	if d.Link.Cardinality == "1:1" || d.Link.Cardinality == "N:1" {
		fields = append(fields, "UNIQUE KEY `edge_source` (`tenant_id`,`source_id`)")
	}
	if d.Link.Cardinality == "1:1" || d.Link.Cardinality == "1:N" {
		fields = append(fields, "UNIQUE KEY `edge_target` (`tenant_id`,`target_id`)")
	}
	fields = append(fields, "CONSTRAINT "+quote(physical("fk_ls_", d.ID))+" FOREIGN KEY (`tenant_id`,`source_id`) REFERENCES "+quote(tableName(d.Link.SourceSchemaID))+" (`tenant_id`,`id`) ON DELETE RESTRICT ON UPDATE RESTRICT", "CONSTRAINT "+quote(physical("fk_lt_", d.ID))+" FOREIGN KEY (`tenant_id`,`target_id`) REFERENCES "+quote(tableName(d.Link.TargetSchemaID))+" (`tenant_id`,`id`) ON DELETE RESTRICT ON UPDATE RESTRICT")
	return "CREATE TABLE " + quote(linkTableName(d.ID)) + " (" + strings.Join(fields, ",") + ") ENGINE=InnoDB ROW_FORMAT=DYNAMIC", nil
}

func verifyLinkTable(ctx context.Context, db *sql.DB, d c.Definition) error {
	var engine, rowFormat string
	if err := db.QueryRowContext(ctx, "SELECT engine,row_format FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", linkTableName(d.ID)).Scan(&engine, &rowFormat); err != nil {
		return err
	}
	if !strings.EqualFold(engine, "InnoDB") || !strings.EqualFold(rowFormat, "Dynamic") {
		return fmt.Errorf("link table engine differs")
	}
	rows, err := db.QueryContext(ctx, "SELECT column_name,column_type,is_nullable,collation_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=?", linkTableName(d.ID))
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var name, kind, nullable string
		var collation sql.NullString
		if err = rows.Scan(&name, &kind, &nullable, &collation); err != nil {
			rows.Close()
			return err
		}
		if name == "tenant_id" {
			if kind != "varbinary(128)" || collation.Valid || nullable != "NO" {
				rows.Close()
				return fmt.Errorf("link tenant column differs")
			}
		} else if (name != "id" && name != "source_id" && name != "target_id" && name != "etag") || kind != "char(36)" || collation.String != "ascii_bin" || nullable != "NO" {
			rows.Close()
			return fmt.Errorf("link column differs")
		}
		seen[name] = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(seen) != 5 {
		return fmt.Errorf("link columns incomplete")
	}
	indexes, err := db.QueryContext(ctx, "SELECT index_name,non_unique,column_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? ORDER BY index_name,seq_in_index", linkTableName(d.ID))
	if err != nil {
		return err
	}
	actual := map[string][]string{}
	unique := map[string]bool{}
	for indexes.Next() {
		var name, col string
		var nonUnique int
		if err = indexes.Scan(&name, &nonUnique, &col); err != nil {
			indexes.Close()
			return err
		}
		actual[name] = append(actual[name], col)
		unique[name] = nonUnique == 0
	}
	if err = indexes.Err(); err != nil {
		indexes.Close()
		return err
	}
	indexes.Close()
	expected := map[string][]string{"PRIMARY": {"tenant_id", "id"}, "edge_pair": {"tenant_id", "source_id", "target_id"}, "edge_reverse": {"tenant_id", "target_id", "source_id"}}
	if d.Link.Cardinality == "1:1" || d.Link.Cardinality == "N:1" {
		expected["edge_source"] = []string{"tenant_id", "source_id"}
	}
	if d.Link.Cardinality == "1:1" || d.Link.Cardinality == "1:N" {
		expected["edge_target"] = []string{"tenant_id", "target_id"}
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("link indexes differ")
	}
	for name, cols := range expected {
		if strings.Join(actual[name], ",") != strings.Join(cols, ",") || unique[name] != (name != "edge_reverse") {
			return fmt.Errorf("link unique constraints differ")
		}
	}
	refs, err := db.QueryContext(ctx, `SELECT k.constraint_name,k.column_name,k.referenced_table_name,k.referenced_column_name,r.delete_rule,r.update_rule FROM information_schema.key_column_usage k JOIN information_schema.referential_constraints r ON r.constraint_schema=k.constraint_schema AND r.constraint_name=k.constraint_name AND r.table_name=k.table_name WHERE k.table_schema=DATABASE() AND k.table_name=? ORDER BY k.constraint_name,k.ordinal_position`, linkTableName(d.ID))
	if err != nil {
		return err
	}
	defer refs.Close()
	count := 0
	for refs.Next() {
		var name, col, table, ref, del, update string
		if err = refs.Scan(&name, &col, &table, &ref, &del, &update); err != nil {
			return err
		}
		side := "source"
		schemaID := d.Link.SourceSchemaID
		if name == physical("fk_lt_", d.ID) {
			side = "target"
			schemaID = d.Link.TargetSchemaID
		} else if name != physical("fk_ls_", d.ID) {
			return fmt.Errorf("unexpected link foreign key")
		}
		if table != tableName(schemaID) || (col != "tenant_id" && col != side+"_id") || (col == "tenant_id" && ref != "tenant_id") || (col == side+"_id" && ref != "id") || del != "RESTRICT" || update != "RESTRICT" {
			return fmt.Errorf("link endpoint foreign key differs")
		}
		count++
	}
	if err = refs.Err(); err != nil {
		return err
	}
	if count != 4 {
		return fmt.Errorf("link endpoint foreign keys incomplete")
	}
	return nil
}
