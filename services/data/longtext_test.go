package data

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestTextBudgetsUseDecodedUTF8BytesAndPreserveLongBodies(t *testing.T) {
	for _, tc := range []struct {
		typeName string
		limit    int
	}{{"text", c.MaxTextBytes}, {"longtext", c.MaxLongTextBytes}} {
		t.Run(tc.typeName, func(t *testing.T) {
			property := c.Property{Name: "正文", DataType: tc.typeName, Nullable: true}
			body := strings.Repeat("知", tc.limit/3) + strings.Repeat("a", tc.limit%3)
			for _, content := range []string{"", body} {
				value, err := typedValue(property, raw(content))
				if err != nil || value != content {
					t.Fatalf("valid %d-byte content was changed or rejected: %v", len(content), err)
				}
				encoded, err := rawDatabaseValue(property, []byte(content))
				var restored string
				if err != nil || json.Unmarshal(encoded, &restored) != nil || restored != content {
					t.Fatalf("database encoding changed %d-byte content: %v", len(content), err)
				}
			}
			if _, err := typedValue(property, raw(body+"a")); err == nil {
				t.Fatal("oversized UTF-8 content accepted")
			}
			if _, err := typedValue(property, raw(42)); err == nil {
				t.Fatal("non-string content accepted")
			}
			if value, err := typedValue(property, raw(nil)); err != nil || value != nil {
				t.Fatal("nullable text lost null")
			}
			property.Nullable = false
			if _, err := typedValue(property, raw(nil)); err == nil {
				t.Fatal("non-null text accepted null")
			}
		})
	}
}

func TestLongTextDDLQueriesAndMigrationBoundary(t *testing.T) {
	d := testDefinition()
	d.Schema.Properties[0].DataType = "longtext"
	statement, err := ddl(d)
	if err != nil || !strings.Contains(statement, quote(columnName(testFieldCode))+" longtext NOT NULL") {
		t.Fatalf("longtext DDL missing: %v", err)
	}
	query := baseQuery()
	query.Where = &c.Expression{Op: "startsWith", Field: testFieldCode, Value: raw("正文\n_%")}
	compiled, err := compileQuery(d, "digest", c.Principal{TenantID: d.TenantID, Subject: "u"}, allowed(), query, strings.Repeat("s", 32))
	if err != nil || compiled.Args[1] != "正文\n!_!%%" {
		t.Fatalf("longtext literal prefix failed: %v", err)
	}
	query.OrderBy = []c.Order{{Field: testFieldCode, Direction: "asc"}}
	if _, err := compileQuery(d, "digest", c.Principal{TenantID: d.TenantID, Subject: "u"}, allowed(), query, strings.Repeat("s", 32)); err == nil {
		t.Fatal("longtext sorting accepted")
	}
	d.Schema.Indexes = []c.Index{{ID: c.NewID(), Name: "正文索引", FieldIDs: []string{testFieldCode}}}
	if _, err := ddl(d); err == nil {
		t.Fatal("longtext index silently accepted")
	}
	d.Schema.Indexes = nil
	longSignature := storageSignature(d)
	d.Schema.Properties[0].DataType = "text"
	if longSignature == storageSignature(d) {
		t.Fatal("text-to-longtext change bypassed migration signature")
	}
}

func TestDataRequestBudgetRejectsOversizeIncludingTrailingWhitespace(t *testing.T) {
	const prefix = `{"values":{"body":"`
	const suffix = `"}}`
	atLimit := prefix + strings.Repeat("x", c.MaxDataRequestBytes-len(prefix)-len(suffix)) + suffix
	for _, tc := range []struct {
		name, input string
		status      int
	}{{"exact limit", atLimit, 0}, {"body exceeds limit", prefix + strings.Repeat("x", c.MaxDataRequestBytes) + suffix, 413}, {"trailing whitespace exceeds limit", atLimit + " ", 413}, {"trailing JSON", `{"values":{}} {}`, 400}, {"unknown field", `{"extra":true}`, 400}} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/data/query", strings.NewReader(tc.input))
			var request c.WriteRequest
			err := decode(httptest.NewRecorder(), r, &request)
			if tc.status == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var api *apiError
			if !errors.As(err, &api) || api.status != tc.status {
				t.Fatalf("expected status %d, got %v", tc.status, err)
			}
		})
	}
}

func TestDataCapabilitiesDeclareLongTextBudgets(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/data/capabilities", nil)
	r = r.WithContext(platform.WithPrincipal(r.Context(), c.Principal{TenantID: "tenant", Subject: "u"}))
	w := httptest.NewRecorder()
	NewHandler(Config{}).ServeHTTP(w, r)
	var result struct {
		DataTypes        []string `json:"dataTypes"`
		MaxTextBytes     int      `json:"maxTextBytes"`
		MaxLongTextBytes int      `json:"maxLongTextBytes"`
		MaxRequestBytes  int      `json:"maxRequestBytes"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.MaxTextBytes != c.MaxTextBytes || result.MaxLongTextBytes != c.MaxLongTextBytes || result.MaxRequestBytes != c.MaxDataRequestBytes {
		t.Fatal("capabilities omitted text budgets")
	}
	for _, typeName := range result.DataTypes {
		if typeName == "longtext" {
			return
		}
	}
	t.Fatal("capabilities omitted longtext")
}
