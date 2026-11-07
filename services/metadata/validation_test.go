package metadata

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

func validSchema() contracts.Definition {
	return contracts.Definition{ID: "schema-1", TenantID: "tenant-1", CategoryID: "category-1", GroupID: "group-1", Kind: contracts.KindSchema, Name: "设备", Schema: &contracts.Schema{Mode: "schema_2_db", StorageLayout: "single", Properties: []contracts.Property{{ID: "field-1", Name: "设备名称", DataType: "string", Required: true}}}}
}

func TestNamesAreNormalizedIndependentlyOfDatabaseCollation(t *testing.T) {
	for _, pair := range [][2]string{{" 设备 ", "设备"}, {"E\u0301", "é"}, {"Straße", "STRASSE"}, {"设备A", "设备a"}} {
		_, left, err := normalizeName(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		_, right, err := normalizeName(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if left != right {
			t.Fatalf("normalization differs for %q and %q", pair[0], pair[1])
		}
	}
	for _, name := range []string{"", "  ", "设\x00备", strings.Repeat("设", 129)} {
		if _, _, err := normalizeName(name); err == nil {
			t.Errorf("invalid name accepted: %q", name)
		}
	}
}

func TestLongTextDefinitionDefaultsAndIndexes(t *testing.T) {
	for _, tc := range []struct {
		typeName string
		limit    int
	}{{"text", contracts.MaxTextBytes}, {"longtext", contracts.MaxLongTextBytes}} {
		t.Run(tc.typeName, func(t *testing.T) {
			d := validSchema()
			p := &d.Schema.Properties[0]
			p.DataType = tc.typeName
			body := strings.Repeat("知", tc.limit/3) + strings.Repeat("a", tc.limit%3)
			p.Default, _ = json.Marshal(body)
			if err := canonicalDefinition(&d, false); err != nil {
				t.Fatalf("valid text default rejected: %v", err)
			}
			p.Default, _ = json.Marshal(body + "a")
			if err := canonicalDefinition(&d, false); err == nil {
				t.Fatal("oversized text default accepted")
			}
			p.Default = json.RawMessage(`42`)
			if err := canonicalDefinition(&d, false); err == nil {
				t.Fatal("non-string text default accepted")
			}
			p.Default = nil
			d.Schema.Indexes = []contracts.Index{{ID: "index-1", Name: "正文索引", FieldIDs: []string{p.ID}}}
			if err := canonicalDefinition(&d, false); err == nil {
				t.Fatal("unsupported text index accepted")
			}
		})
	}
}

func TestDefinitionRejectsUnsupportedOrConflictingSemantics(t *testing.T) {
	cases := map[string]func(*contracts.Definition){
		"unknown kind":    func(d *contracts.Definition) { d.Kind = "table" },
		"wrong body":      func(d *contracts.Definition) { d.Kind = contracts.KindEnum },
		"multiple bodies": func(d *contracts.Definition) { d.Enum = &contracts.Enum{} },
		"reverse mapping": func(d *contracts.Definition) { d.Schema.Mode = "db_2_schema" },
		"sharding":        func(d *contracts.Definition) { d.Schema.StorageLayout = "sharded_tables" },
		"unknown type":    func(d *contracts.Definition) { d.Schema.Properties[0].DataType = "float128" },
		"duplicate id": func(d *contracts.Definition) {
			d.Schema.Properties = append(d.Schema.Properties, contracts.Property{ID: "field-1", Name: "其他名称", DataType: "string"})
		},
		"duplicate normalized name": func(d *contracts.Definition) {
			d.Schema.Properties = append(d.Schema.Properties, contracts.Property{ID: "field-2", Name: " 设备名称 ", DataType: "string"})
		},
		"null default": func(d *contracts.Definition) { d.Schema.Properties[0].Default = json.RawMessage("null") },
		"foreign index": func(d *contracts.Definition) {
			d.Schema.Indexes = []contracts.Index{{ID: "index-1", Name: "编码索引", FieldIDs: []string{"missing"}}}
		},
		"invalid decimal precision": func(d *contracts.Definition) {
			d.Schema.Properties[0].DataType = "decimal"
			d.Schema.Properties[0].Precision = 39
		},
		"numeric enum": func(d *contracts.Definition) {
			d.Schema.Properties[0].DataType = "int64"
			d.Schema.Properties[0].EnumID = "enum-1"
		},
	}
	for name, modify := range cases {
		t.Run(name, func(t *testing.T) {
			d := validSchema()
			modify(&d)
			if err := canonicalDefinition(&d, true); err == nil {
				t.Fatal("invalid definition was accepted")
			}
		})
	}
}

func TestSchemaAssignsStableFieldIDAndDefaultSingleLayout(t *testing.T) {
	d := validSchema()
	d.Schema.Properties[0].ID = ""
	d.Schema.StorageLayout = ""
	if err := canonicalDefinition(&d, true); err != nil {
		t.Fatal(err)
	}
	id := d.Schema.Properties[0].ID
	if len(id) != 36 || d.Schema.StorageLayout != "single" {
		t.Fatalf("unexpected canonical definition: %+v", d)
	}
	if err := canonicalDefinition(&d, true); err != nil {
		t.Fatal(err)
	}
	if d.Schema.Properties[0].ID != id {
		t.Fatal("existing field ID changed")
	}
}

func TestFieldNamesCannotShadowOtherStableIDs(t *testing.T) {
	for _, tc := range []struct{ name, id, shadow string }{
		{"exact UUID", "12345678-1234-1234-1234-1234567890ab", "12345678-1234-1234-1234-1234567890ab"},
		{"case and whitespace", "12345678-1234-1234-1234-1234567890ab", " 12345678-1234-1234-1234-1234567890AB "},
		{"NFC", "E\u0301", "É"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, reversed := range []bool{false, true} {
				ps := []contracts.Property{{ID: tc.id, Name: "第一属性", DataType: "string"}, {ID: "different-id", Name: tc.shadow, DataType: "string"}}
				if reversed {
					ps[0], ps[1] = ps[1], ps[0]
				}
				if err := validateProperties(ps, false); err == nil {
					t.Fatalf("cross-namespace ambiguity accepted (reversed=%v)", reversed)
				}
			}
		})
	}
	if err := validateProperties([]contracts.Property{{ID: "same-id", Name: "same-id", DataType: "string"}}, false); err != nil {
		t.Fatalf("same field remains unambiguous: %v", err)
	}
}

func TestPublicationCompatibilityDoesNotHideMigration(t *testing.T) {
	old := validSchema()
	raw, _ := json.Marshal(old)
	var renamed contracts.Definition
	_ = json.Unmarshal(raw, &renamed)
	renamed.Name = "生产设备"
	renamed.Description = "新说明"
	if err := compatibleUpdate(old, renamed); err != nil {
		t.Fatal(err)
	}
	renamed.Schema.Properties[0].Name = "对象名称"
	if err := compatibleUpdate(old, renamed); err != nil {
		t.Fatalf("field display rename should preserve its binding: %v", err)
	}
	if old.Schema.Properties[0].Name != "设备名称" {
		t.Fatal("compatibility comparison mutated current definition")
	}
	renamed.Schema.Properties[0].DataType = "text"
	if err := compatibleUpdate(old, renamed); err == nil {
		t.Fatal("structural change accepted without migration")
	}
}

func TestETagIsContentBoundAndStrongConditionalRequired(t *testing.T) {
	d := validSchema()
	first := digestDefinition(d)
	if first != digestDefinition(d) {
		t.Fatal("digest is nondeterministic")
	}
	d.Name = "设备二"
	if first == digestDefinition(d) {
		t.Fatal("digest does not bind name")
	}
	for _, value := range []string{"", "*", "W/\"" + first + "\"", first, "\"wrong\""} {
		r := httptest.NewRequest("PUT", "/", nil)
		r.Header.Set("If-Match", value)
		if err := matchHeader(r, first); err == nil {
			t.Errorf("invalid If-Match accepted: %q", value)
		}
	}
	r := httptest.NewRequest("PUT", "/", nil)
	r.Header.Set("If-Match", etagHeader(first))
	if err := matchHeader(r, first); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeRejectsUnknownFieldsOversizeAndTrailingJSON(t *testing.T) {
	for _, input := range []string{`{"unknown":true}`, `{"name":"设备"} {}`, `{"name":"` + strings.Repeat("x", (1<<20)+1) + `"}`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(input))
		var v struct {
			Name string `json:"name"`
		}
		if err := decode(r, &v); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
}
