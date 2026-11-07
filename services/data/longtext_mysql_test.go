package data

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

func TestMySQLLongTextRoundTripAtApplicationLimit(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is not set; MySQL longtext round trip not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 2, MaxIdle: 1})
	if err != nil {
		t.Fatal("connect isolated MySQL failed")
	}
	defer engine.Close()
	d := testDefinition()
	d.ID, d.TenantID = c.NewID(), c.NewID()
	property := c.Property{ID: c.NewID(), Name: "正文", DataType: "longtext"}
	d.Schema.Properties = []c.Property{property}
	statement, err := ddl(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.DB().ExecContext(ctx, statement); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := engine.DB().ExecContext(cleanup, "DROP TABLE "+quote(tableName(d.ID))); err != nil {
			t.Error("could not remove isolated longtext test table")
		}
	}()
	if err := verifyTable(ctx, engine.DB(), d); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("知", c.MaxLongTextBytes/3) + "a"
	value, err := typedValue(property, raw(body))
	if err != nil {
		t.Fatal(err)
	}
	id := c.NewID()
	if _, err := engine.DB().ExecContext(ctx, "INSERT INTO "+quote(tableName(d.ID))+" (tenant_id,id,etag,"+quote(columnName(property.ID))+") VALUES (?,?,?,?)", d.TenantID, id, c.NewID(), value); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	var byteLength int
	if err := engine.DB().QueryRowContext(ctx, "SELECT "+quote(columnName(property.ID))+",OCTET_LENGTH("+quote(columnName(property.ID))+") FROM "+quote(tableName(d.ID))+" WHERE tenant_id=? AND id=?", d.TenantID, id).Scan(&stored, &byteLength); err != nil {
		t.Fatal(err)
	}
	encoded, err := rawDatabaseValue(property, stored)
	var restored string
	if err != nil || json.Unmarshal(encoded, &restored) != nil || byteLength != c.MaxLongTextBytes || restored != body {
		t.Fatalf("longtext round trip truncated or changed content: bytes=%d err=%v", byteLength, err)
	}
}
