package data

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func ontologySQLTestDefinition() c.Definition {
	return c.Definition{ID: "schema", Kind: c.KindSchema, Name: "笔记", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{
		{ID: "title", Name: "标题", DataType: "text", Nullable: true},
		{ID: "count", Name: "计数", DataType: "int64", Nullable: true},
		{ID: "price", Name: "金额", DataType: "decimal", Precision: 30, Scale: 6, Nullable: true},
		{ID: "time", Name: "更新时间", DataType: "timestamp", Nullable: true},
		{ID: "available", Name: "可用", DataType: "boolean"},
		{ID: "code", Name: "编码", DataType: "string"},
	}}}
}

func compileOntologySQLTest(t *testing.T, text string, parameters map[string]json.RawMessage) c.OntologySQLAnalysis {
	t.Helper()
	statement, diags := parseOntologySQL(text)
	if len(diags) > 0 {
		t.Fatalf("parse: %#v", diags)
	}
	return bindOntologySQL(statement, ontologySQLTestDefinition(), parameters)
}

func TestOntologySQLLowersPrecedenceAndStableFieldIdentity(t *testing.T) {
	a := compileOntologySQLTest(t, `select "n"."标题", "n"."计数" from "个人知识库"."笔记" as "n"
WHERE NOT ("n"."标题" IS NULL OR STARTS_WITH("n"."标题", :前缀))
AND "n"."计数" >= 9007199254740993 OR "n"."可用" = FALSE
ORDER BY "n"."计数" DESC LIMIT 12;`, map[string]json.RawMessage{"前缀": raw("_%' OR 1=1 --")})
	if !a.Ready || a.Query == nil || len(a.Diagnostics) > 0 {
		t.Fatalf("analysis %#v", a)
	}
	q := a.Query
	if q.From.SchemaID != "schema" || !reflect.DeepEqual(q.Select, []string{"title", "count"}) || q.Page.Limit != 12 || q.OrderBy[0].Field != "count" || q.OrderBy[0].Direction != "desc" {
		t.Fatalf("wrong binding: %#v", q)
	}
	if q.Where.Op != "or" || q.Where.Args[0].Op != "and" || q.Where.Args[0].Args[0].Op != "not" {
		t.Fatalf("wrong precedence: %#v", q.Where)
	}
	if string(q.Where.Args[0].Args[1].Value) != `"9007199254740993"` {
		t.Fatal("int64 literal lost exact value")
	}
	prefix := q.Where.Args[0].Args[0].Args[0].Args[1]
	if prefix.Op != "startsWith" || string(prefix.Value) != string(raw("_%' OR 1=1 --")) {
		t.Fatalf("prefix changed: %#v", prefix)
	}
	if len(a.Parameters) != 1 || a.Parameters[0].Name != "前缀" || !a.Parameters[0].Provided {
		t.Fatalf("parameter inference: %#v", a.Parameters)
	}
}

func TestOntologySQLPreservesNumbersTimestampsAndLiteralText(t *testing.T) {
	for _, tc := range []struct{ predicate, expected string }{
		{`"计数" = -9223372036854775808`, `"-9223372036854775808"`},
		{`"金额" = 9007199254740993.123456`, `"9007199254740993.123456"`},
		{`"更新时间" = '2026-11-07T15:00:00.123456+08:00'`, `"2026-11-07T15:00:00.123456+08:00"`},
		{`"标题" = '  é; ''你好'' %_  '`, `"  é; '你好' %_  "`},
	} {
		a := compileOntologySQLTest(t, `SELECT "标题" FROM "笔记" WHERE `+tc.predicate, nil)
		if !a.Ready || a.Query == nil {
			t.Fatalf("%s: %#v", tc.predicate, a.Diagnostics)
		}
		if string(a.Query.Where.Value) != tc.expected {
			t.Fatalf("%s: got %s want %s", tc.predicate, a.Query.Where.Value, tc.expected)
		}
	}
}

func TestOntologySQLMissingConflictingAndInvalidParameters(t *testing.T) {
	a := compileOntologySQLTest(t, `SELECT "标题" FROM "笔记" WHERE "计数" > :下限 AND "计数" < :上限`, nil)
	if a.Ready || a.Query != nil || len(a.Diagnostics) != 0 || len(a.Parameters) != 2 || a.Parameters[0].Provided {
		t.Fatalf("missing parameters: %#v", a)
	}
	a = compileOntologySQLTest(t, `SELECT "标题" FROM "笔记" WHERE "计数" > :值 AND "标题" = :值`, nil)
	if len(a.Diagnostics) == 0 || a.Diagnostics[0].Code != "parameter_type_conflict" {
		t.Fatalf("type conflict: %#v", a)
	}
	for _, tc := range []struct {
		text       string
		parameters map[string]json.RawMessage
		code       string
	}{
		{`SELECT "标题" FROM "笔记" WHERE "计数" = :值`, map[string]json.RawMessage{"值": json.RawMessage(`9007199254740993`)}, "invalid_value"},
		{`SELECT "标题" FROM "笔记" WHERE "标题" = :值`, map[string]json.RawMessage{"值": json.RawMessage(`null`)}, "null_comparison"},
		{`SELECT "标题" FROM "笔记" WHERE "计数" IN (:值)`, map[string]json.RawMessage{"值": json.RawMessage(`["1","2"]`)}, "invalid_value"},
		{`SELECT "标题" FROM "笔记"`, map[string]json.RawMessage{"多余": raw("值")}, "unused_parameter"},
	} {
		a = compileOntologySQLTest(t, tc.text, tc.parameters)
		if a.Ready || len(a.Diagnostics) == 0 || a.Diagnostics[0].Code != tc.code {
			t.Fatalf("expected %s: %#v", tc.code, a)
		}
	}
	// A parameter whose name is a keyword must never be consumed as a keyword.
	a = compileOntologySQLTest(t, `SELECT "计数" FROM "笔记" WHERE "计数" IN (:IN, :NOT, :NULL)`, map[string]json.RawMessage{"IN": raw("1"), "NOT": raw("2"), "NULL": raw("3")})
	if !a.Ready {
		t.Fatalf("keyword parameter: %#v", a)
	}
}

func TestOntologySQLNullAndFiniteCollections(t *testing.T) {
	for _, predicate := range []string{`"标题" IS NULL`, `"标题" IS NOT NULL`, `"计数" IN (1, 2, NULL)`, `"计数" NOT IN (1, :值)`} {
		var parameters map[string]json.RawMessage
		if strings.Contains(predicate, ":值") {
			parameters = map[string]json.RawMessage{"值": raw("3")}
		}
		a := compileOntologySQLTest(t, `SELECT "标题" FROM "笔记" WHERE `+predicate, parameters)
		if !a.Ready {
			t.Fatalf("%s: %#v", predicate, a)
		}
	}
	for _, predicate := range []string{`"计数" = 9223372036854775808`, `"计数" = 1.2`, `"金额" = 01.20`, `"金额" = 1.1234567`, `"可用" > TRUE`, `STARTS_WITH("计数", '1')`, `"更新时间" = '2026-11-07T00:00:00.1234567Z'`, `"编码" IN (NULL)`} {
		a := compileOntologySQLTest(t, `SELECT "标题" FROM "笔记" WHERE `+predicate, nil)
		if a.Ready || len(a.Diagnostics) == 0 {
			t.Fatalf("invalid typed predicate accepted: %s", predicate)
		}
	}
}

func TestOntologySQLQuotedNamesNormalizationAndUnavailableFields(t *testing.T) {
	d := ontologySQLTestDefinition()
	d.Name = `点.号"模型`
	d.Schema.Properties[0].Name = `A".é`
	statement, diags := parseOntologySQL(`SELECT "a"".é" FROM "点.号""模型"`)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	a := bindOntologySQL(statement, d, nil)
	if !a.Ready || a.Query.Select[0] != "title" {
		t.Fatalf("quoted/normalized names: %#v", a)
	}
	for _, text := range []string{`SELECT "title" FROM "笔记"`, `SELECT "隐藏字段" FROM "笔记"`, `SELECT "其他"."标题" FROM "笔记" AS "当前"`, `SELECT "标题", " 标题 " FROM "笔记"`, `SELECT "标题" FROM "笔记" ORDER BY "标题"`} {
		a = compileOntologySQLTest(t, text, nil)
		if a.Ready || len(a.Diagnostics) == 0 {
			t.Fatalf("unavailable/unsupported field accepted: %s", text)
		}
	}
	d.Schema.Properties[0].Mapping = &c.PropertyMapping{DataSourceID: "secret-source", Table: "private-table", Column: "private-column"}
	statement, _ = parseOntologySQL(`SELECT "a"".é" FROM "点.号""模型"`)
	a = bindOntologySQL(statement, d, nil)
	if !a.Ready || a.Fields[0].Mapping != nil {
		t.Fatal("analysis leaked physical mapping")
	}
}

func TestOntologySQLRejectsUnsupportedAndUnconsumedSyntax(t *testing.T) {
	for _, text := range []string{
		`DELETE FROM "笔记"`, `SELECT * FROM "笔记"`, `SELECT DISTINCT "标题" FROM "笔记"`, `SELECT "标题" AS "名称" FROM "笔记"`,
		`SELECT "标题" FROM "笔记"; SELECT "计数" FROM "笔记"`, `SELECT "标题" FROM "笔记" JOIN "其他" ON 1 = 1`,
		`SELECT "标题" FROM "笔记" JOIN LINK "关系" AS "其他"`, `SELECT SUM(*) FROM "笔记"`, `SELECT '值' FROM "笔记"`,
		`SELECT "标题" FROM "笔记" WHERE "计数" = "计数"`, `SELECT "标题" FROM "笔记" WHERE "计数" = NULL`,
		`SELECT "标题" FROM "笔记" WHERE "标题" LIKE 'x%'`, `SELECT "标题" FROM "笔记" WHERE "计数" IN ()`,
		`SELECT "标题" FROM "笔记" WHERE "计数" IN (SELECT "计数" FROM "笔记")`, `SELECT "标题" FROM "笔记" WHERE "计数" = 1e3`,
		`SELECT "标题" FROM "笔记" ORDER BY "计数", "编码"`, `SELECT "标题" FROM "笔记" LIMIT 0`, `SELECT "标题" FROM "笔记" LIMIT 101`,
		`SELECT "标题" FROM "笔记" LIMIT :页`, `SELECT "标题" FROM "笔记" LIMIT 1 OFFSET 2`, `SELECT "标题" FROM "笔记" HAVING COUNT(*) > 1`,
		`SELECT 标题 FROM 笔记`, `SELECT "标题" FROM "笔记" /* incomplete`, `SELECT "标题" FROM "笔记" /* nested /* */ */`,
	} {
		t.Run(text, func(t *testing.T) {
			if statement, diags := parseOntologySQL(text); statement != nil || len(diags) == 0 {
				t.Fatalf("unsupported syntax accepted: %#v %#v", statement, diags)
			}
		})
	}
}

func TestOntologySQLUTF16RangesAndComments(t *testing.T) {
	text := "-- 😀 中文\r\nSELECT \"标题\" FROM \"笔记\" WHERE \"标题\" = :值; -- ; SELECT ignored"
	tokens, diags := lexOntologySQL(text)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	if tokens[1].Text != "SELECT" || tokens[1].Range.From != 10 || tokens[1].Range.Line != 2 || tokens[1].Range.Column != 1 {
		t.Fatalf("UTF16/CRLF: %#v", tokens[1])
	}
	a := compileOntologySQLTest(t, text, map[string]json.RawMessage{"值": raw(";--/* 😀")})
	if !a.Ready || len(a.Parameters) != 1 {
		t.Fatalf("comments and semicolon strings: %#v", a)
	}
	parameter := a.Parameters[0].Ranges[0]
	if parameter.To-parameter.From != 2 || parameter.Line != 2 || parameter.Column != 36 {
		t.Fatalf("parameter range: %#v", parameter)
	}
	_, diags = parseOntologySQL("-- 😀\r\nSELECT \"标题\" FROM \"笔记\" WHERE \"标题\" = NULL")
	if len(diags) == 0 || diags[0].Range.Line != 2 || diags[0].Range.To-diags[0].Range.From != 4 {
		t.Fatalf("diagnostic range: %#v", diags)
	}
}

func TestOntologySQLFormattingPreservesBindingAndComments(t *testing.T) {
	text := `select "标题", /* 保留 ; ' \" */ "计数" from "个人知识库"."笔记" as "n" -- 行注释 ;
where STARTS_WITH("标题", '  x'';_%  ') or "计数" in (1,2,null) limit 50;`
	formatted, diags := formatOntologySQL(text)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	if !strings.Contains(formatted, "\nFROM ") || !strings.Contains(formatted, "/* 保留 ; ' \\\" */") || !strings.Contains(formatted, "-- 行注释 ;\n") {
		t.Fatalf("format lost comments: %s", formatted)
	}
	a := compileOntologySQLTest(t, text, nil)
	b := compileOntologySQLTest(t, formatted, nil)
	if !a.Ready || !b.Ready || !reflect.DeepEqual(a.Query, b.Query) {
		t.Fatalf("format changed semantics:\n%s\n%#v\n%#v", formatted, a, b)
	}
	invalid := `SELECT "标题" FROM "笔记"; DELETE FROM "笔记"`
	if actual, diags := formatOntologySQL(invalid); len(diags) == 0 || actual != invalid {
		t.Fatal("formatter discarded invalid tail")
	}
}

func TestOntologySQLBudgets(t *testing.T) {
	for _, text := range []string{
		strings.Repeat(" ", maxOntologySQLBytes+1),
		`SELECT "标题" FROM "笔记" WHERE ` + strings.Repeat("NOT ", 20) + `"标题" IS NULL`,
		`SELECT "标题" FROM "笔记" WHERE ` + strings.Repeat("(", 20) + `"标题" IS NULL` + strings.Repeat(")", 20),
		`SELECT "标题" FROM "笔记" WHERE "计数" IN (` + strings.Repeat("1,", 100) + `1)`,
	} {
		if statement, diags := parseOntologySQL(text); statement != nil || len(diags) == 0 {
			t.Fatal("over-budget SQL accepted")
		}
	}
	parts := make([]string, 65)
	for i := range parts {
		parts[i] = ":p" + strings.Repeat("a", i)
	}
	a := compileOntologySQLTest(t, `SELECT "计数" FROM "笔记" WHERE "计数" IN (`+strings.Join(parts, ",")+")", nil)
	if a.Ready || len(a.Diagnostics) == 0 || a.Diagnostics[0].Code != "budget_exceeded" {
		t.Fatalf("parameter budget: %#v", a)
	}
}

func TestOntologySQLIncompletePrefixesAreBoundedAndNeverPanic(t *testing.T) {
	text := `SELECT "n"."标题", "n"."计数" FROM "个人知识库"."笔记" AS "n" WHERE NOT ("n"."计数" IN (1, :值, NULL) OR STARTS_WITH("n"."标题", 'a'';b')) ORDER BY "n"."计数" DESC LIMIT 20; -- 尾注释`
	runes := []rune(text)
	for i := 0; i <= len(runes); i++ {
		prefix := string(runes[:i])
		statement, diagnostics := parseOntologySQL(prefix)
		if statement == nil && len(diagnostics) == 0 {
			t.Fatalf("incomplete prefix returned neither AST nor diagnostics: %q", prefix)
		}
		if statement != nil {
			bindOntologySQL(statement, ontologySQLTestDefinition(), nil)
		}
		formatted, diagnostics := formatOntologySQL(prefix)
		if len(diagnostics) > 0 && formatted != prefix {
			t.Fatalf("formatter modified incomplete prefix: %q", prefix)
		}
	}
	for _, text := range []string{"", "SELECT", "SELECT WHERE", "SELECT FROM WHERE", "SELECT (", "SELECT \"标题\" FROM \"笔记\" WHERE", "SELECT \"标题\" FROM \"笔记\" WHERE NOT", "SELECT \"标题\" FROM \"笔记\" WHERE " + strings.Repeat("(", 8000), "SELECT \"标题\" FROM \"笔记\" WHERE " + strings.Repeat("NOT ", 8000)} {
		if statement, diagnostics := parseOntologySQL(text); statement != nil || len(diagnostics) == 0 {
			t.Fatalf("malformed query accepted: %q", text[:min(len(text), 100)])
		}
	}
}

func FuzzOntologySQLParser(f *testing.F) {
	for _, text := range []string{
		`SELECT "标题" FROM "笔记"`,
		`SELECT "计数" FROM "分类"."笔记" WHERE "计数" >= 9007199254740993`,
		"-- 😀\r\nSELECT \"标题\" FROM \"笔记\" WHERE \"标题\" = :值",
		`SELECT "标题" FROM "笔记" WHERE NOT ("计数" IN (1, 2, NULL))`,
		`SELECT "标题" FROM "笔记"; DELETE FROM "笔记"`,
		`SELECT "标题" FROM "笔记" WHERE (((((((((((((`,
		`SELECT "标题" FROM "笔记" /* unterminated`,
		`SELECT "标题" FROM "笔记" WHERE "标题" = 'a'';b'`,
	} {
		f.Add(text)
	}
	f.Fuzz(func(t *testing.T, text string) {
		statement, diagnostics := parseOntologySQL(text)
		if statement == nil {
			if len(diagnostics) == 0 {
				t.Fatal("missing parse diagnostic")
			}
			return
		}
		if len(diagnostics) != 0 {
			t.Fatal("parser returned AST with error diagnostics")
		}
		analysis := bindOntologySQL(statement, ontologySQLTestDefinition(), nil)
		if analysis.Ready && analysis.Query == nil {
			t.Fatal("ready query has no binding")
		}
		formatted, diagnostics := formatOntologySQL(text)
		if len(diagnostics) != 0 {
			t.Fatal("parsed statement could not format")
		}
		other, diagnostics := parseOntologySQL(formatted)
		if other == nil || len(diagnostics) != 0 {
			t.Fatalf("formatter produced invalid syntax: %q", formatted)
		}
	})
}
