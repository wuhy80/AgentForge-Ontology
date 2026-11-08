package data

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestMappedWriteETagBindsCompleteCanonicalState(t *testing.T) {
	d := c.Definition{ID: c.NewID(), TenantID: "test-tenant", Kind: c.KindSchema, Schema: &c.Schema{Properties: []c.Property{
		{ID: "amount", DataType: "decimal", Precision: 38, Scale: 4},
		{ID: "at", DataType: "timestamp"},
		{ID: "private", DataType: "text"},
	}}}
	current := c.CurrentDefinition{Definition: d, ETag: "current-definition"}
	item := c.Instance{ID: c.NewID(), SchemaID: d.ID, Values: map[string]json.RawMessage{"amount": json.RawMessage(`"9007199254740993.1"`), "at": json.RawMessage(`"2026-11-08T08:30:00+08:00"`), "private": json.RawMessage(`"SYNTHETIC_PRIVATE"`)}}
	secret := []byte(strings.Repeat("a", 32))
	first, err := stampMappedInstance(secret, current, item)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Values["amount"]) != `"9007199254740993.1000"` || string(first.Values["at"]) != `"2026-11-08T00:30:00Z"` {
		t.Fatal("source row did not use exact database canonical values")
	}
	second, err := stampMappedInstance(secret, current, first)
	if err != nil || first.ETag != second.ETag {
		t.Fatal("the ETag depends on incoming textual encoding")
	}
	for name, change := range map[string]func(*c.CurrentDefinition, *c.Instance){
		"hidden state": func(_ *c.CurrentDefinition, i *c.Instance) {
			i.Values["private"] = json.RawMessage(`"SYNTHETIC_CHANGED"`)
		},
		"tenant":     func(c *c.CurrentDefinition, _ *c.Instance) { c.Definition.TenantID += "-other" },
		"schema":     func(c *c.CurrentDefinition, _ *c.Instance) { c.Definition.ID = "other-schema" },
		"definition": func(c *c.CurrentDefinition, _ *c.Instance) { c.ETag += "-changed" },
		"instance":   func(_ *c.CurrentDefinition, i *c.Instance) { i.ID = c.NewID() },
	} {
		t.Run(name, func(t *testing.T) {
			copyCurrent, copyItem := current, item
			copyItem.Values = map[string]json.RawMessage{}
			for id, raw := range item.Values {
				copyItem.Values[id] = raw
			}
			change(&copyCurrent, &copyItem)
			result, err := stampMappedInstance(secret, copyCurrent, copyItem)
			if err != nil || result.ETag == first.ETag {
				t.Fatal("write credential was reusable across a different complete row identity or state")
			}
		})
	}
	otherSecret, _ := stampMappedInstance([]byte(strings.Repeat("b", 32)), current, item)
	if otherSecret.ETag == first.ETag || first.ETag == digest(first.Values) {
		t.Fatal("hidden row values were authenticated without the source secret")
	}
}

func TestMappedWriteScopeRejectsBeforeOpeningUncertifiedTargets(t *testing.T) {
	current := c.CurrentDefinition{Definition: c.Definition{ID: c.NewID()}}
	if ready, _, err := (&handler{}).mappedMutationReadiness(context.Background(), current); err != nil || ready {
		t.Fatal("missing storage advertised writable mapped objects")
	}
	for _, tables := range [][]*mappedTable{
		nil,
		{{primary: true, source: c.DataSource{Driver: "postgresql", TenantIsolation: "dedicated"}}},
		{{primary: true, source: c.DataSource{Driver: "mysql", TenantIsolation: "column"}}},
		{{primary: true, source: c.DataSource{Driver: "mysql", TenantIsolation: "dedicated"}}, {source: c.DataSource{Driver: "mysql", TenantIsolation: "dedicated"}}},
	} {
		if validateMappedWriteTable(context.Background(), current.Definition, tables) == nil {
			t.Fatal("unverified source scope was writable")
		}
	}
}

func TestMappedWriteCatalogGrantsRequireCompleteDirectVisibility(t *testing.T) {
	for name, test := range map[string]struct {
		grants []string
		want   bool
	}{
		"schema all hides cross-schema references": {[]string{"GRANT ALL PRIVILEGES ON `source_db`.* TO `writer`@`%`"}, false},
		"global all":                             {[]string{"GRANT ALL PRIVILEGES ON *.* TO `writer`@`%`"}, true},
		"partial revoke hides metadata":          {[]string{"GRANT ALL PRIVILEGES ON *.* TO `writer`@`%`", "REVOKE SELECT, REFERENCES ON `another`.* FROM `writer`@`%`"}, false},
		"combined direct grants":                 {[]string{"GRANT SELECT ON `source_db`.* TO `writer`@`%`", "GRANT TRIGGER, REFERENCES ON *.* TO `writer`@`%`"}, true},
		"metadata without global business reads": {[]string{"GRANT SELECT, TRIGGER ON `source_db`.* TO `writer`@`%`", "GRANT REFERENCES ON *.* TO `writer`@`%`"}, true},
		"DML hides triggers":                     {[]string{"GRANT SELECT, INSERT, UPDATE, DELETE ON `source_db`.* TO `writer`@`%`"}, false},
		"table hides inbound references":         {[]string{"GRANT SELECT, TRIGGER ON `source_db`.`items` TO `writer`@`%`"}, false},
		"role unverified":                        {[]string{"GRANT `source_admin`@`%` TO `writer`@`%`"}, false},
		"other schema":                           {[]string{"GRANT ALL PRIVILEGES ON `another`.* TO `writer`@`%`"}, false},
		"wildcard schema unverified":             {[]string{"GRANT ALL PRIVILEGES ON `source%`.* TO `writer`@`%`"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := mappedWriteCatalogGrants(test.grants, "source_db"); got != test.want {
				t.Fatalf("complete direct metadata visibility=%v want=%v", got, test.want)
			}
		})
	}
}

func TestMappedWriteColumnsRequireLosslessPhysicalRepresentation(t *testing.T) {
	table := &mappedTable{source: c.DataSource{Driver: "mysql"}}
	for name, test := range map[string]struct {
		property  c.Property
		column    c.SourceColumn
		collation string
		want      bool
	}{
		"varchar preserves all strings":                {c.Property{DataType: "string"}, c.SourceColumn{DataType: "string", DatabaseType: "varchar(255)", MaxLength: 255}, "utf8mb4_0900_bin", true},
		"char trims trailing spaces":                   {c.Property{DataType: "string"}, c.SourceColumn{DataType: "string", DatabaseType: "char(255)", MaxLength: 255}, "utf8mb4_0900_bin", false},
		"short varchar cannot encode all values":       {c.Property{DataType: "string"}, c.SourceColumn{DataType: "string", DatabaseType: "varchar(32)", MaxLength: 32}, "utf8mb4_0900_bin", false},
		"other charset loses characters":               {c.Property{DataType: "string"}, c.SourceColumn{DataType: "string", DatabaseType: "varchar(255)", MaxLength: 255}, "latin1_bin", false},
		"case insensitive unique keys change identity": {c.Property{DataType: "string"}, c.SourceColumn{DataType: "string", DatabaseType: "varchar(255)", MaxLength: 255}, "utf8mb4_general_ci", false},
		"microsecond timestamp":                        {c.Property{DataType: "timestamp"}, c.SourceColumn{DataType: "timestamp", DatabaseType: "datetime(6)"}, "", true},
		"timestamp truncates precision":                {c.Property{DataType: "timestamp"}, c.SourceColumn{DataType: "timestamp", DatabaseType: "datetime"}, "", false},
		"timestamp type applies source timezone":       {c.Property{DataType: "timestamp"}, c.SourceColumn{DataType: "timestamp", DatabaseType: "timestamp(6)"}, "", false},
		"exact decimal":                                {c.Property{DataType: "decimal", Precision: 38, Scale: 4}, c.SourceColumn{DataType: "decimal", DatabaseType: "decimal(38,4)", Precision: 38, Scale: 4}, "", true},
		"decimal rounds input":                         {c.Property{DataType: "decimal", Precision: 38, Scale: 4}, c.SourceColumn{DataType: "decimal", DatabaseType: "decimal(38,2)", Precision: 38, Scale: 2}, "", false},
		"UUID canonical ascii":                         {c.Property{DataType: "uuid"}, c.SourceColumn{DataType: "string", DatabaseType: "char(36)", MaxLength: 36}, "ascii_bin", true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := mappedWriteColumnMatches(table, test.property, test.column, test.collation); got != test.want {
				t.Fatalf("lossless physical representation=%v want=%v", got, test.want)
			}
		})
	}
}
