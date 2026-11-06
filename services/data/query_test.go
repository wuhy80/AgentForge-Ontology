package data

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const testFieldCode = "1b74bd75-135a-4f2f-8f10-7ac08918ef10"
const testFieldPrice = "1b74bd75-135a-4f2f-8f10-7ac08918ef11"
const testFieldDept = "1b74bd75-135a-4f2f-8f10-7ac08918ef12"

func testDefinition() c.Definition {
	return c.Definition{ID: "1b74bd75-135a-4f2f-8f10-7ac08918ef00", TenantID: "tenant-a", Kind: c.KindSchema, Name: "设备", Schema: &c.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []c.Property{{ID: testFieldCode, Name: "设备编码", DataType: "string", Required: true}, {ID: testFieldPrice, Name: "采购价", DataType: "decimal", Precision: 18, Scale: 3, Nullable: true}, {ID: testFieldDept, Name: "所属部门", DataType: "string", Required: true}}}}
}
func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func baseQuery() c.QuerySpec {
	return c.QuerySpec{From: c.Source{SchemaID: testDefinition().ID}, Select: []string{"设备编码"}, Page: c.Page{Limit: 10}}
}
func allowed() c.Decision {
	return c.Decision{Allow: true, ReadFields: []string{testFieldCode, testFieldDept}, WriteFields: []string{"*"}, State: "policy-1"}
}

func TestCompilerBindsValuesAndEscapesLiteralPrefix(t *testing.T) {
	d := testDefinition()
	q := baseQuery()
	q.Where = &c.Expression{Op: "startsWith", Field: "设备编码", Value: raw("P_%' OR 1=1 --")}
	compiled, err := compileQuery(d, "current", c.Principal{TenantID: d.TenantID, Subject: "user"}, allowed(), q, strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compiled.SQL, "OR 1=1") || !strings.Contains(compiled.SQL, "ESCAPE '!'") {
		t.Fatalf("unsafe or incorrect SQL: %s", compiled.SQL)
	}
	if compiled.Args[1] != "P!_!%' OR 1=1 --%" {
		t.Fatalf("literal prefix altered: %#v", compiled.Args)
	}
	if compiled.Args[0] != d.TenantID {
		t.Fatal("tenant not bound")
	}
}

func TestForbiddenFieldsCannotBeProjectedFilteredOrSorted(t *testing.T) {
	for _, use := range []string{"projection", "filter", "sort"} {
		t.Run(use, func(t *testing.T) {
			q := baseQuery()
			switch use {
			case "projection":
				q.Select = []string{"采购价"}
			case "filter":
				q.Where = &c.Expression{Op: "gt", Field: "采购价", Value: raw("1.000")}
			case "sort":
				q.OrderBy = []c.Order{{Field: "采购价", Direction: "asc"}}
			}
			_, err := compileQuery(testDefinition(), "digest", c.Principal{TenantID: "tenant-a", Subject: "user"}, allowed(), q, strings.Repeat("s", 32))
			e, ok := err.(*apiError)
			if !ok || e.status != 403 {
				t.Fatalf("expected field denial, got %v", err)
			}
		})
	}
}

func TestNullAndNotUseSQLThreeValuedLogic(t *testing.T) {
	s := testDefinition().Schema
	b := expressionCompiler{schema: s, allowed: []string{"*"}}
	expr := c.Expression{Op: "not", Args: []c.Expression{{Op: "gt", Field: testFieldPrice, Value: raw("220.000")}}}
	sql, args, err := b.compile(&expr, 0)
	if err != nil {
		t.Fatal(err)
	}
	if sql != "NOT (`"+columnName(testFieldPrice)+"` > CAST(? AS decimal(18,3)))" || len(args) != 1 {
		t.Fatalf("NOT comparison changed semantics: %s %#v", sql, args)
	}
	if _, _, err = b.compile(&c.Expression{Op: "eq", Field: testFieldPrice, Value: raw(nil)}, 0); err == nil {
		t.Fatal("comparison to null must use an explicit null operator")
	}
	if _, _, err = b.compile(&c.Expression{Op: "in", Field: testFieldCode, Value: raw([]string{})}, 0); err == nil {
		t.Fatal("empty IN silently accepted")
	}
}

func TestCursorCannotCrossIdentityPolicyDefinitionOrQuery(t *testing.T) {
	d := testDefinition()
	p := c.Principal{TenantID: d.TenantID, Subject: "u1"}
	dec := allowed()
	q := baseQuery()
	secret := strings.Repeat("s", 32)
	compiled, err := compileQuery(d, "g1", p, dec, q, secret)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signCursor(secret, pageCursor{Fingerprint: compiled.Fingerprint, TenantID: p.TenantID, Subject: p.Subject, SchemaID: d.ID, Digest: "g1", Authorization: digest(dec), LastID: c.NewID(), Expires: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	q.Page.Cursor = token
	if _, err = compileQuery(d, "g1", p, dec, q, secret); err != nil {
		t.Fatalf("valid cursor failed: %v", err)
	}
	for _, change := range []string{"tenant", "subject", "definition", "permission", "query", "tamper"} {
		t.Run(change, func(t *testing.T) {
			p2, d2, q2 := p, dec, q
			etag := "g1"
			switch change {
			case "tenant":
				p2.TenantID = "tenant-b"
			case "subject":
				p2.Subject = "u2"
			case "definition":
				etag = "g2"
			case "permission":
				d2.State = "policy-2"
			case "query":
				q2.Page.Limit = 20
			case "tamper":
				q2.Page.Cursor = "x" + token
			}
			if _, err := compileQuery(d, etag, p2, d2, q2, secret); err == nil {
				t.Fatal("cursor reused across changed context")
			}
		})
	}
}

func TestNullableDescendingCursorRetainsNullTail(t *testing.T) {
	d := testDefinition()
	q := baseQuery()
	q.OrderBy = []c.Order{{Field: testFieldPrice, Direction: "desc"}}
	dec := allowed()
	dec.ReadFields = []string{"*"}
	p := c.Principal{TenantID: d.TenantID, Subject: "u"}
	secret := strings.Repeat("s", 32)
	compiled, err := compileQuery(d, "g", p, dec, q, secret)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signCursor(secret, pageCursor{Fingerprint: compiled.Fingerprint, TenantID: p.TenantID, Subject: p.Subject, SchemaID: d.ID, Digest: "g", Authorization: digest(dec), LastID: c.NewID(), LastValue: raw("2.000"), Expires: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	q.Page.Cursor = token
	compiled, err = compileQuery(d, "g", p, dec, q, secret)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.SQL, " OR `"+columnName(testFieldPrice)+"` IS NULL") {
		t.Fatal("descending continuation lost null values")
	}
}

func TestValueEncodingPreservesPrecisionAndRejectsTruncation(t *testing.T) {
	tests := []struct {
		p         c.Property
		good, bad json.RawMessage
	}{{c.Property{Name: "整数", DataType: "int64"}, raw("9223372036854775807"), raw("9223372036854775808")}, {c.Property{Name: "金额", DataType: "decimal", Precision: 18, Scale: 3}, raw("123456789012345.123"), raw("1.1234")}, {c.Property{Name: "时间", DataType: "timestamp"}, raw("2026-11-06T10:00:00.123456+08:00"), raw("2026-11-06T10:00:00.1234567Z")}}
	for _, tc := range tests {
		if _, err := typedValue(tc.p, tc.good); err != nil {
			t.Fatal(err)
		}
		if _, err := typedValue(tc.p, tc.bad); err == nil {
			t.Fatalf("silent truncation for %s", tc.p.Name)
		}
	}
	if _, err := typedValue(c.Property{DataType: "int64"}, raw(int64(42))); err == nil {
		t.Fatal("int64 numeric JSON could lose precision in a client")
	}
	p := c.Property{DataType: "decimal"}
	out, err := rawDatabaseValue(p, []byte("123456789012345.123"))
	if err != nil || string(out) != `"123456789012345.123"` {
		t.Fatalf("decimal lost precision: %s %v", out, err)
	}
}

func TestPhysicalNamesAreStableAcrossBusinessRename(t *testing.T) {
	d := testDefinition()
	first, err := ddl(d)
	if err != nil {
		t.Fatal(err)
	}
	sig := storageSignature(d)
	d.Name = "设备`; DROP TABLE victims --"
	d.Schema.Properties[0].Name = "编码`中文"
	second, err := ddl(d)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || sig != storageSignature(d) {
		t.Fatal("business rename changed physical structure")
	}
	if strings.Contains(second, "victims") {
		t.Fatal("business text became SQL identifier")
	}
	d.Schema.Properties[0].Required = false
	if sig == storageSignature(d) {
		t.Fatal("constraint change is not detected")
	}
}

func TestBudgetsAndUnsupportedLayoutsFailBeforeSQL(t *testing.T) {
	d := testDefinition()
	d.Schema.StorageLayout = "sharded_tables"
	if _, err := ddl(d); err == nil {
		t.Fatal("unsupported layout accepted")
	}
	d = testDefinition()
	d.Schema.Properties[0].ReadOnly = true
	if _, err := ddl(d); err == nil {
		t.Fatal("uncreatable readonly required field accepted")
	}
	d = testDefinition()
	expr := c.Expression{Op: "eq", Field: testFieldCode, Value: raw("x")}
	for i := 0; i < 12; i++ {
		expr = c.Expression{Op: "not", Args: []c.Expression{expr}}
	}
	q := baseQuery()
	q.Where = &expr
	if _, err := compileQuery(d, "g", c.Principal{TenantID: d.TenantID}, allowed(), q, strings.Repeat("s", 32)); err == nil {
		t.Fatal("expression depth unbounded")
	}
}

func TestUnauthenticatedRoutesDoNotTouchStorage(t *testing.T) {
	h := NewHandler(Config{ServiceToken: strings.Repeat("s", 32)})
	for _, path := range []string{"/data/capabilities", "/data/schemas/a/instances/x", "/internal/publications/preview"} {
		method := http.MethodGet
		if strings.Contains(path, "internal") {
			method = http.MethodPost
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		if rec.Code != 401 {
			t.Fatalf("%s returned %d", path, rec.Code)
		}
	}
}

func TestFieldNamesUseTheSameUnicodeAndCaseNormalizationAsMetadata(t *testing.T) {
	d := testDefinition()
	d.Schema.Properties[0].Name = "État"
	q := baseQuery()
	q.Select = []string{"e\u0301TAT"}
	q.Where = &c.Expression{Op: "eq", Field: "éTAT", Value: raw("active")}
	compiled, err := compileQuery(d, "g", c.Principal{TenantID: d.TenantID, Subject: "u"}, allowed(), q, strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Projection) != 1 || compiled.Projection[0].ID != testFieldCode {
		t.Fatal("Unicode equivalent name did not bind the same stable field")
	}
	values, err := buildValues(d.Schema, map[string]json.RawMessage{"E\u0301TAT": raw("active"), testFieldDept: raw("D1")}, nil, []string{"*"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if string(values[testFieldCode]) != `"active"` {
		t.Fatal("write name normalization differs from query binding")
	}
	d.Schema.Properties[1].Name = "e\u0301tat"
	if err := validateSchema(d); err == nil {
		t.Fatal("Unicode/case-equivalent duplicate names accepted")
	}
}

func TestPropertyNameCannotShadowAnotherStableID(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		d := testDefinition()
		d.Schema.Properties[1].Name = strings.ToUpper(testFieldCode)
		if reverse {
			d.Schema.Properties[0], d.Schema.Properties[1] = d.Schema.Properties[1], d.Schema.Properties[0]
		}
		if err := validateSchema(d); err == nil {
			t.Fatal("name/ID cross-namespace collision accepted")
		}
		if _, ok := findProperty(d.Schema, testFieldCode); ok {
			t.Fatal("ambiguous input silently bound by declaration order")
		}
	}
}
