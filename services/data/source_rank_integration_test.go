package data

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

func TestPostgresRankRequiresTableAndSchemaPrivileges(t *testing.T) {
	dsn := sourceIntegrationDSN("postgresql", "POSTGRES_SOURCE_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_SOURCE_TEST_DSN not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx := context.Background()
	suffix := strings.ReplaceAll(c.NewID(), "-", "")[:12]
	role, visible, blocked := "rank_role_"+suffix, "rank_visible_"+suffix, "rank_blocked_"+suffix
	defer func() {
		for _, statement := range []string{"RESET ROLE", "DROP SCHEMA IF EXISTS " + visible + " CASCADE", "DROP SCHEMA IF EXISTS " + blocked + " CASCADE", "DROP ROLE IF EXISTS " + role} {
			if _, err = db.ExecContext(ctx, statement); err != nil {
				t.Error(err)
			}
		}
	}()
	for _, statement := range []string{"CREATE ROLE " + role + " NOLOGIN", "CREATE SCHEMA " + visible, "CREATE SCHEMA " + blocked, "CREATE TABLE " + visible + ".allowed_table(id int)", "CREATE TABLE " + visible + ".denied_table(id int)", "CREATE TABLE " + visible + ".af_private(id int)", "CREATE TABLE " + blocked + ".schema_blocked(id int)", "GRANT USAGE ON SCHEMA " + visible + " TO " + role, "GRANT SELECT ON " + visible + ".allowed_table, " + visible + ".af_private, " + blocked + ".schema_blocked TO " + role} {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.ExecContext(ctx, "SET ROLE "+role); err != nil {
		t.Fatal(err)
	}
	for _, template := range sourceCatalogTemplates("postgresql") {
		result, err := querySourceWithLimit(ctx, db, c.DataSource{Driver: "postgresql", TenantIsolation: "dedicated"}, template, 100)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range result.Rows {
			if *row[0] == blocked || *row[1] == "denied_table" || strings.HasPrefix(*row[1], "af_") {
				t.Fatalf("rank disclosed inaccessible/reserved table: %+v", row)
			}
			if *row[0] == visible && *row[1] == "allowed_table" {
				found = true
				if strings.Contains(template, "estimated_rows") && row[2] != nil {
					t.Fatal("unknown relation estimate invented a number")
				}
			}
		}
		if !found {
			t.Fatal("rank omitted role-visible table")
		}
	}
}
