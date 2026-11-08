package data

import (
	"net/http"
	"strconv"
	"strings"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// Only these complete catalog templates may bypass ordinary-table discovery.
// User SQL is token-matched, then replaced with this trusted SQL. A template
// edit cannot expose a different catalog, predicate, projection or function.
const mysqlSourceStorageSQL = `SELECT TABLE_SCHEMA AS namespace, TABLE_NAME AS table_name, DATA_LENGTH AS data_bytes, INDEX_LENGTH AS index_bytes, DATA_LENGTH + INDEX_LENGTH AS total_bytes FROM information_schema.tables WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE' AND LEFT(LOWER(TABLE_NAME), 3) <> 'af_' ORDER BY total_bytes DESC, TABLE_NAME ASC LIMIT 20`
const mysqlSourceRowsSQL = `SELECT TABLE_SCHEMA AS namespace, TABLE_NAME AS table_name, TABLE_ROWS AS estimated_rows FROM information_schema.tables WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE' AND LEFT(LOWER(TABLE_NAME), 3) <> 'af_' ORDER BY estimated_rows DESC, TABLE_NAME ASC LIMIT 20`
const postgresSourceStorageSQL = `SELECT n.nspname AS namespace, c.relname AS table_name, pg_catalog.pg_table_size(c.oid) AS data_bytes, pg_catalog.pg_indexes_size(c.oid) AS index_bytes, pg_catalog.pg_total_relation_size(c.oid) AS total_bytes FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind = 'r' AND LEFT(LOWER(n.nspname), 3) <> 'pg_' AND n.nspname <> 'information_schema' AND LEFT(LOWER(c.relname), 3) <> 'af_' AND pg_catalog.has_table_privilege(c.oid, 'SELECT') AND pg_catalog.has_schema_privilege(n.oid, 'USAGE') ORDER BY total_bytes DESC, n.nspname ASC, c.relname ASC LIMIT 20`
const postgresSourceRowsSQL = `SELECT n.nspname AS namespace, c.relname AS table_name, CASE WHEN c.reltuples < 0 THEN NULL ELSE CAST(c.reltuples AS bigint) END AS estimated_rows FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind = 'r' AND LEFT(LOWER(n.nspname), 3) <> 'pg_' AND n.nspname <> 'information_schema' AND LEFT(LOWER(c.relname), 3) <> 'af_' AND pg_catalog.has_table_privilege(c.oid, 'SELECT') AND pg_catalog.has_schema_privilege(n.oid, 'USAGE') ORDER BY estimated_rows DESC NULLS LAST, n.nspname ASC, c.relname ASC LIMIT 20`

func sourceCatalogTemplates(driver string) []string {
	if driver == "mysql" {
		return []string{mysqlSourceStorageSQL, mysqlSourceRowsSQL}
	}
	if driver == "postgresql" {
		return []string{postgresSourceStorageSQL, postgresSourceRowsSQL}
	}
	return nil
}

func parseSourceQuery(source c.DataSource, text string, requestLimit int) (*sourceSelect, string, error) {
	if requestLimit == 0 {
		requestLimit = 100
	}
	if requestLimit < 1 || requestLimit > 100 {
		return nil, "", problem(422, "invalid_source_limit", "查询行数上限须为1至100，省略或0使用100")
	}
	tokens, err := lexSourceSQL(text)
	if err != nil {
		return nil, "", err
	}
	if len(tokens) > 0 && tokens[len(tokens)-1] == ";" {
		tokens = tokens[:len(tokens)-1]
	}
	for _, template := range sourceCatalogTemplates(source.Driver) {
		canonical, _ := lexSourceSQL(template)
		if len(tokens) != len(canonical) || len(tokens) < 2 {
			continue
		}
		matched := true
		for i := 0; i < len(tokens)-1; i++ {
			// String literals and quoted identifiers are compared byte-for-byte.
			quoted := strings.HasPrefix(tokens[i], "'") || strings.HasPrefix(tokens[i], "\"") || strings.HasPrefix(tokens[i], "`")
			if quoted && tokens[i] != canonical[i] || !quoted && !strings.EqualFold(tokens[i], canonical[i]) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		if source.Driver == "mysql" {
			if _, err := sourceNamespace(source, ""); err != nil {
				return nil, "", err
			}
		}
		limit, e := strconv.Atoi(tokens[len(tokens)-1])
		if e != nil || limit < 1 || limit > 100 {
			return nil, "", sourceSQLUnsupported()
		}
		if requestLimit < limit {
			limit = requestLimit
		}
		statement := template[:strings.LastIndex(template, " LIMIT ")] + " LIMIT " + strconv.Itoa(limit+1)
		if source.Driver == "mysql" {
			statement = strings.Replace(statement, "SELECT ", "SELECT /*+ MAX_EXECUTION_TIME(10000) */ ", 1)
		}
		return &sourceSelect{source: source, limit: limit, catalogTemplate: true}, statement, nil
	}
	p, statement, err := parseSourceSelect(source, text)
	if err != nil {
		return nil, "", err
	}
	if p.limit > requestLimit {
		statement = strings.TrimSuffix(statement, " LIMIT "+strconv.Itoa(p.limit+1)) + " LIMIT " + strconv.Itoa(requestLimit+1)
		p.limit = requestLimit
	}
	return p, statement, nil
}

// Formatting changes only whitespace. Every token, including unquoted names,
// numbers and the entire quoted literal/identifier, retains its exact spelling.
func formatSourceSQL(text string) (string, error) {
	tokens, err := lexSourceSQL(text)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	previous := ""
	depth := 0
	for i, token := range tokens {
		line := depth == 0 && (strings.EqualFold(token, "FROM") || strings.EqualFold(token, "WHERE") || strings.EqualFold(token, "ORDER") || strings.EqualFold(token, "LIMIT") || strings.EqualFold(token, "JOIN"))
		if i > 0 && token != ";" && token != "," && token != ")" && token != "." && previous != "." && previous != "(" && token != "(" {
			if line {
				out.WriteByte('\n')
			} else {
				out.WriteByte(' ')
			}
		}
		out.WriteString(token)
		if token == "(" {
			depth++
		}
		if token == ")" {
			depth--
		}
		previous = token
	}
	return out.String(), nil
}

func (h *handler) sourceSQLAccess(r *http.Request, p c.Principal) (c.DataSource, error) {
	source, _, err := h.loadSource(r.Context(), p.TenantID, r.PathValue("id"))
	if err != nil {
		return source, err
	}
	if r.Header.Get("If-Match") == "" {
		return source, problem(428, "precondition_required", "SQL操作必须携带数据源 If-Match")
	}
	if r.Header.Get("If-Match") != `"`+source.ETag+`"` {
		return source, problem(412, "precondition_failed", "数据源已改变，请刷新后重试")
	}
	if !source.Enabled {
		return source, problem(409, "source_disabled", "数据源已停用")
	}
	if source.TenantIsolation != "dedicated" {
		return source, problem(422, "source_query_isolation", "共享数据库请通过模型数据探索查询；直接 SQL 仅开放给当前租户专属数据源")
	}
	return source, nil
}

func (h *handler) sourceSQLDelivery(r *http.Request, p c.Principal, source c.DataSource) error {
	if r.Context().Err() != nil {
		return problem(503, "source_query_failed", "请求超时或已取消，结果未交付")
	}
	if err := h.sourceOverviewSession(r, p); err != nil {
		return err
	}
	current, err := h.sourceSQLAccess(r, p)
	if err != nil {
		return err
	}
	if current.ETag != source.ETag {
		return problem(412, "precondition_failed", "SQL操作期间数据源已改变，请刷新后重试")
	}
	return nil
}

func (h *handler) sourceSQLToolHTTP(w http.ResponseWriter, r *http.Request, format bool) error {
	p, err := h.sourceAdmin(r)
	if err != nil {
		return err
	}
	var input c.SourceQuery
	if err = decode(w, r, &input); err != nil {
		return err
	}
	source, err := h.sourceSQLAccess(r, p)
	if err != nil {
		return err
	}
	if err = h.sourceOverviewSession(r, p); err != nil {
		return err
	}
	if _, _, err = parseSourceQuery(source, input.SQL, input.Limit); err != nil {
		return err
	}
	result := c.SourceSQLToolResult{SQL: input.SQL, Valid: true, Dialect: source.Driver, Message: "静态检查通过；仅验证受支持语法，表、字段及数据库读取权限在执行时核验"}
	if format {
		result.SQL, err = formatSourceSQL(input.SQL)
		if err != nil {
			return err
		}
		result.Message = "格式化完成；仅调整空白，未执行SQL，表、字段及数据库读取权限在执行时核验"
	}
	if err = h.sourceSQLDelivery(r, p, source); err != nil {
		return err
	}
	writeJSON(w, 200, result)
	return nil
}
func (h *handler) sourceSQLAnalyzeHTTP(w http.ResponseWriter, r *http.Request) error {
	return h.sourceSQLToolHTTP(w, r, false)
}
func (h *handler) sourceSQLFormatHTTP(w http.ResponseWriter, r *http.Request) error {
	return h.sourceSQLToolHTTP(w, r, true)
}
