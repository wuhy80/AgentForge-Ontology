package data

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// SQL identifiers are checked and quoted here, while values are always bound.
// Physical names can be Unicode; display names are never treated as SQL names.
func sourceQuote(driver, name string) (string, error) {
	limit := 63
	if driver == "mysql" {
		limit = 64
	}
	if name == "" || utf8.RuneCountInString(name) > limit || (driver == "postgresql" && len(name) > 63) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", problem(422, "invalid_mapping", "物理表、列和命名空间必须为有效数据库标识")
	}
	if driver == "mysql" {
		return "`" + strings.ReplaceAll(name, "`", "``") + "`", nil
	}
	if driver == "postgresql" {
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`, nil
	}
	return "", problem(422, "unsupported_driver", "不支持的数据源类型")
}

func sourceNamespace(s c.DataSource, namespace string) (string, error) {
	if namespace == "" {
		if s.Driver == "mysql" {
			namespace = s.Database
		} else {
			namespace = "public"
		}
	}
	if _, err := sourceQuote(s.Driver, namespace); err != nil {
		return "", err
	}
	if s.Driver == "mysql" && namespace != s.Database {
		return "", problem(422, "invalid_mapping", "MySQL映射只能使用数据源配置的数据库")
	}
	lower := strings.ToLower(namespace)
	if lower == "information_schema" || lower == "mysql" || lower == "performance_schema" || lower == "sys" || strings.HasPrefix(lower, "pg_") {
		return "", problem(422, "reserved_namespace", "不能映射数据库系统命名空间")
	}
	return namespace, nil
}

func validSourceTable(s c.DataSource, name string) error {
	if _, err := sourceQuote(s.Driver, name); err != nil {
		return err
	}
	if strings.HasPrefix(strings.ToLower(name), "af_") {
		return problem(422, "reserved_table", "不能映射平台保留表")
	}
	return nil
}

func listSourceTables(ctx context.Context, db *sql.DB, source c.DataSource, namespace string) ([]c.TableInfo, error) {
	namespace, err := sourceNamespace(source, namespace)
	if err != nil {
		return nil, err
	}
	query := "SELECT table_name FROM information_schema.tables WHERE table_schema=? AND table_type='BASE TABLE' ORDER BY table_name LIMIT 1001"
	if source.Driver == "postgresql" {
		query = "SELECT table_name FROM information_schema.tables WHERE table_schema=$1 AND table_type='BASE TABLE' ORDER BY table_name LIMIT 1001"
	}
	rows, err := db.QueryContext(ctx, query, namespace)
	if err != nil {
		return nil, problem(422, "source_catalog_failed", "无法读取数据源表目录，请核对目录权限")
	}
	defer rows.Close()
	out := []c.TableInfo{}
	count := 0
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, problem(422, "source_catalog_failed", "数据源表目录格式无法识别")
		}
		count++
		if count > 1000 {
			return nil, problem(422, "budget_exceeded", "单个命名空间最多发现1000张表，请拆分数据源范围")
		}
		if validSourceTable(source, name) != nil {
			continue
		}
		out = append(out, c.TableInfo{Namespace: namespace, Name: name, Type: "table"})
	}
	if rows.Err() != nil {
		return nil, problem(422, "source_catalog_failed", "数据源表目录读取中断")
	}
	return out, nil
}

func readSourceTable(ctx context.Context, db *sql.DB, source c.DataSource, namespace, table string) (c.SourceTable, error) {
	out := c.SourceTable{Name: table, Columns: []c.SourceColumn{}, Indexes: []c.SourceIndex{}}
	namespace, err := sourceNamespace(source, namespace)
	if err != nil {
		return out, err
	}
	out.Namespace = namespace
	if err = validSourceTable(source, table); err != nil {
		return out, err
	}
	query := "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=? AND table_name=? AND table_type='BASE TABLE'"
	if source.Driver == "postgresql" {
		query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2 AND table_type='BASE TABLE'"
	}
	var exists int
	if err = db.QueryRowContext(ctx, query, namespace, table).Scan(&exists); err != nil {
		return out, problem(422, "source_catalog_failed", "无法读取数据源表结构，请核对目录权限")
	}
	if exists != 1 {
		return out, problem(404, "source_table_not_found", "数据源中不存在可读取的普通表")
	}
	if source.Driver == "postgresql" {
		var inheritedChildren bool
		if err = db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_class t JOIN pg_catalog.pg_namespace n ON n.oid=t.relnamespace JOIN pg_catalog.pg_inherits i ON i.inhparent=t.oid WHERE n.nspname=$1 AND t.relname=$2 AND t.relkind='r')`, namespace, table).Scan(&inheritedChildren); err != nil {
			return out, problem(422, "source_catalog_failed", "无法核对PostgreSQL继承拓扑")
		}
		if inheritedChildren {
			return out, problem(422, "unsupported_source_topology", "普通继承父表的唯一约束不覆盖子表，当前不支持该来源拓扑")
		}
	}
	if source.Driver == "mysql" {
		err = readMySQLColumns(ctx, db, &out)
	} else {
		err = readPostgreSQLColumns(ctx, db, &out)
	}
	if err != nil {
		return out, err
	}
	if len(out.Columns) == 0 {
		return out, problem(422, "source_catalog_failed", "未发现可读取的源列")
	}
	if err = readSourceIndexes(ctx, db, source, &out); err != nil {
		return out, err
	}
	for i := range out.Columns {
		for _, index := range out.Indexes {
			if !index.Primary {
				continue
			}
			for _, name := range index.Columns {
				if name == out.Columns[i].Name {
					out.Columns[i].PrimaryKey = true
				}
			}
		}
	}
	out.Signature = digest(struct {
		Columns []c.SourceColumn
		Indexes []c.SourceIndex
	}{out.Columns, out.Indexes})
	return out, nil
}

func readMySQLColumns(ctx context.Context, db *sql.DB, out *c.SourceTable) error {
	rows, err := db.QueryContext(ctx, `SELECT COLUMN_NAME,COLUMN_TYPE,DATA_TYPE,IS_NULLABLE,EXTRA,COALESCE(NUMERIC_PRECISION,0),COALESCE(NUMERIC_SCALE,0),COALESCE(CHARACTER_MAXIMUM_LENGTH,0),ORDINAL_POSITION FROM information_schema.columns WHERE table_schema=? AND table_name=? ORDER BY ORDINAL_POSITION LIMIT 257`, out.Namespace, out.Name)
	if err != nil {
		return problem(422, "source_catalog_failed", "无法读取MySQL源列目录")
	}
	defer rows.Close()
	for rows.Next() {
		var col c.SourceColumn
		var native, nullable, extra string
		if err = rows.Scan(&col.Name, &col.DatabaseType, &native, &nullable, &extra, &col.Precision, &col.Scale, &col.MaxLength, &col.Position); err != nil {
			return problem(422, "source_catalog_failed", "MySQL源列目录格式无法识别")
		}
		col.Nullable, col.Generated = nullable == "YES", strings.Contains(strings.ToLower(extra), "generated") || strings.Contains(strings.ToLower(extra), "auto_increment")
		col.DataType = sourceLogicalType("mysql", native, col.DatabaseType, col.MaxLength)
		out.Columns = append(out.Columns, col)
		if len(out.Columns) > 256 {
			return problem(422, "budget_exceeded", "源表列数超过256项发现预算")
		}
	}
	if rows.Err() != nil {
		return problem(422, "source_catalog_failed", "MySQL源列目录读取中断")
	}
	return nil
}

func readPostgreSQLColumns(ctx context.Context, db *sql.DB, out *c.SourceTable) error {
	rows, err := db.QueryContext(ctx, `SELECT c.column_name,pg_catalog.format_type(a.atttypid,a.atttypmod),c.udt_name,c.is_nullable,c.is_generated,c.is_identity,COALESCE(c.numeric_precision,0),COALESCE(c.numeric_scale,0),COALESCE(c.character_maximum_length,0),c.ordinal_position FROM information_schema.columns c JOIN pg_catalog.pg_namespace n ON n.nspname=c.table_schema JOIN pg_catalog.pg_class t ON t.relnamespace=n.oid AND t.relname=c.table_name JOIN pg_catalog.pg_attribute a ON a.attrelid=t.oid AND a.attname=c.column_name WHERE c.table_schema=$1 AND c.table_name=$2 ORDER BY c.ordinal_position LIMIT 257`, out.Namespace, out.Name)
	if err != nil {
		return problem(422, "source_catalog_failed", "无法读取PostgreSQL源列目录")
	}
	defer rows.Close()
	for rows.Next() {
		var col c.SourceColumn
		var native, nullable, generated, identity string
		if err = rows.Scan(&col.Name, &col.DatabaseType, &native, &nullable, &generated, &identity, &col.Precision, &col.Scale, &col.MaxLength, &col.Position); err != nil {
			return problem(422, "source_catalog_failed", "PostgreSQL源列目录格式无法识别")
		}
		col.Nullable, col.Generated = nullable == "YES", generated != "NEVER" || identity == "YES"
		col.DataType = sourceLogicalType("postgresql", native, col.DatabaseType, col.MaxLength)
		out.Columns = append(out.Columns, col)
		if len(out.Columns) > 256 {
			return problem(422, "budget_exceeded", "源表列数超过256项发现预算")
		}
	}
	if rows.Err() != nil {
		return problem(422, "source_catalog_failed", "PostgreSQL源列目录读取中断")
	}
	return nil
}

func sourceLogicalType(driver, native, complete string, maxLength int64) string {
	native, complete = strings.ToLower(native), strings.ToLower(complete)
	if driver == "mysql" {
		if strings.Contains(complete, "unsigned") && (native == "bigint" || native == "decimal") {
			return "unsupported"
		}
		switch native {
		case "varchar", "char":
			if maxLength <= 255 {
				return "string"
			}
			return "text"
		case "tinytext", "text":
			return "text"
		case "mediumtext", "longtext":
			return "longtext"
		case "tinyint":
			if complete == "tinyint(1)" {
				return "boolean"
			}
			return "int64"
		case "smallint", "mediumint", "int", "bigint":
			return "int64"
		case "decimal", "numeric":
			return "decimal"
		case "datetime", "timestamp":
			return "timestamp"
		}
	} else {
		switch native {
		case "varchar", "bpchar":
			if maxLength > 0 && maxLength <= 255 {
				return "string"
			}
			return "text"
		case "text":
			return "longtext"
		case "uuid":
			return "uuid"
		case "int2", "int4", "int8":
			return "int64"
		case "numeric":
			return "decimal"
		case "bool":
			return "boolean"
		case "timestamp", "timestamptz":
			return "timestamp"
		}
	}
	return "unsupported"
}

func readSourceIndexes(ctx context.Context, db *sql.DB, source c.DataSource, out *c.SourceTable) error {
	query := `SELECT INDEX_NAME,NON_UNIQUE,SEQ_IN_INDEX,COALESCE(COLUMN_NAME,''),'' FROM information_schema.statistics WHERE table_schema=? AND table_name=? ORDER BY INDEX_NAME,SEQ_IN_INDEX LIMIT 1025`
	if source.Driver == "postgresql" {
		query = `SELECT idx.relname,NOT (ix.indisunique AND ix.indisvalid AND ix.indisready),k.ordinality,COALESCE(a.attname,''),COALESCE(pg_catalog.pg_get_expr(ix.indpred,ix.indrelid),'') FROM pg_catalog.pg_class t JOIN pg_catalog.pg_namespace n ON n.oid=t.relnamespace JOIN pg_catalog.pg_index ix ON ix.indrelid=t.oid JOIN pg_catalog.pg_class idx ON idx.oid=ix.indexrelid JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY k(attnum,ordinality) ON k.ordinality<=ix.indnkeyatts LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid=t.oid AND a.attnum=k.attnum WHERE n.nspname=$1 AND t.relname=$2 ORDER BY idx.relname,k.ordinality LIMIT 1025`
	}
	rows, err := db.QueryContext(ctx, query, out.Namespace, out.Name)
	if err != nil {
		return problem(422, "source_catalog_failed", "无法读取源表索引目录")
	}
	defer rows.Close()
	indexes := map[string]*c.SourceIndex{}
	count := 0
	for rows.Next() {
		var name, column, predicate string
		var nonUnique bool
		var position int
		if err = rows.Scan(&name, &nonUnique, &position, &column, &predicate); err != nil {
			return problem(422, "source_catalog_failed", "源表索引目录格式无法识别")
		}
		count++
		if count > 1024 {
			return problem(422, "budget_exceeded", "源表索引列超过1024项发现预算")
		}
		if indexes[name] == nil {
			indexes[name] = &c.SourceIndex{Name: name, Unique: !nonUnique, Primary: source.Driver == "mysql" && name == "PRIMARY", Columns: []string{}, Predicate: predicate}
		}
		indexes[name].Columns = append(indexes[name].Columns, column)
	}
	if rows.Err() != nil {
		return problem(422, "source_catalog_failed", "源表索引目录读取中断")
	}
	if source.Driver == "postgresql" {
		var primary string
		err = db.QueryRowContext(ctx, `SELECT COALESCE((SELECT idx.relname FROM pg_catalog.pg_class t JOIN pg_catalog.pg_namespace n ON n.oid=t.relnamespace JOIN pg_catalog.pg_index ix ON ix.indrelid=t.oid AND ix.indisprimary AND ix.indisvalid AND ix.indisready JOIN pg_catalog.pg_class idx ON idx.oid=ix.indexrelid WHERE n.nspname=$1 AND t.relname=$2),'')`, out.Namespace, out.Name).Scan(&primary)
		if err != nil {
			return problem(422, "source_catalog_failed", "无法核对源表主键目录")
		}
		if index := indexes[primary]; index != nil {
			index.Primary = true
		}
	}
	names := make([]string, 0, len(indexes))
	for name := range indexes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out.Indexes = append(out.Indexes, *indexes[name])
	}
	return nil
}

func (h *handler) listSourceTablesHTTP(w http.ResponseWriter, r *http.Request) error {
	p, err := h.sourceCatalogPrincipal(r)
	if err != nil {
		return err
	}
	db, source, err := h.openSource(r.Context(), p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	defer db.Close()
	items, err := listSourceTables(r.Context(), db, source, r.URL.Query().Get("namespace"))
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"data": items, "hasMore": false})
	return nil
}

func (h *handler) sourceTableHTTP(w http.ResponseWriter, r *http.Request) error {
	p, err := h.sourceCatalogPrincipal(r)
	if err != nil {
		return err
	}
	db, source, err := h.openSource(r.Context(), p.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	defer db.Close()
	table, err := readSourceTable(r.Context(), db, source, r.URL.Query().Get("namespace"), r.PathValue("table"))
	if err != nil {
		return err
	}
	writeJSON(w, 200, table)
	return nil
}
