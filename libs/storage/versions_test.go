package storage

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

func mysqlVersionFixture(version, product string) expectation {
	return expectation{
		op: "query", contains: "SELECT VERSION(),@@version_comment",
		columns: []string{"version", "product", "engine", "charset", "collation", "mode", "zone", "binary_collation"},
		rows:    [][]driver.Value{{version, product, "InnoDB", "utf8mb4", "utf8mb4_general_ci", "STRICT_TRANS_TABLES", "SYSTEM", true}},
	}
}

func postgresVersionFixture(version, numeric, banner string) expectation {
	return expectation{
		op: "query", contains: "SELECT version(),current_setting('server_version')",
		columns: []string{"banner", "version", "number", "server_encoding", "client_encoding", "standard_strings", "zone"},
		rows:    [][]driver.Value{{banner, version, numeric, "UTF8", "UTF8", "on", "UTC"}},
	}
}

func requireVersionError(t *testing.T, err error, code string) {
	t.Helper()
	var versionErr *VersionError
	if !errors.As(err, &versionErr) || versionErr.Code != code {
		t.Fatalf("wanted %s, got %v", code, err)
	}
}

func TestDatabaseVersionPoliciesAndRegistryDoNotTrustArbitrarySeries(t *testing.T) {
	for _, item := range []struct{ driver, policy, expected string }{
		{"mysql", "", ""}, {"mysql", "auto", ""}, {"mysql", "pinned", "26.7"},
		{"postgresql", "pinned", "17"},
	} {
		if err := ValidateDatabaseVersionPolicy(item.driver, item.policy, item.expected); err != nil {
			t.Fatalf("valid policy rejected: %+v: %v", item, err)
		}
	}
	for _, item := range []struct{ driver, policy, expected, code string }{
		{"mysql", "auto", "26.7", "invalid_version_policy"},
		{"mysql", "pinned", "", "invalid_version_policy"},
		{"mysql", "legacy", "", "invalid_version_policy"},
		{"mysql", "pinned", "99.0", "unsupported_database_version"},
		{"postgresql", "pinned", "26.7", "unsupported_database_version"},
		{"sqlite", "auto", "", "unsupported_driver"},
	} {
		t.Run(item.driver+"/"+item.policy+"/"+item.expected, func(t *testing.T) {
			requireVersionError(t, ValidateDatabaseVersionPolicy(item.driver, item.policy, item.expected), item.code)
			_, err := InspectDatabaseVersion(context.Background(), nil, item.driver, item.policy, item.expected)
			requireVersionError(t, err, item.code)
		})
	}
	options := DatabaseVersionOptions()
	options[0].Status = "changed"
	if DatabaseVersionOptions()[0].Status != "supported" {
		t.Fatal("caller mutated shared version registrations")
	}
}

func TestMySQLVersionInspectionBindsIdentitySettingsAndProfile(t *testing.T) {
	one := mysqlVersionFixture("26.7.0", "MySQL Community Server - GPL")
	two := mysqlVersionFixture("26.7.0", "MySQL Community Server - GPL")
	two.rows[0][5] = "STRICT_TRANS_TABLES,NO_BACKSLASH_ESCAPES"
	db, _ := newFixtureDB(t, one, one, two)
	first, err := InspectDatabaseVersion(context.Background(), db, "mysql", "pinned", "26.7")
	if err != nil {
		t.Fatal(err)
	}
	if first.Product != "mysql" || first.ServerVersion != "26.7.0" || first.VersionLine != "26.7" || first.CapabilityProfile == "" || len(first.Signature) != 64 || len(first.Capabilities) == 0 {
		t.Fatalf("incomplete capability identity: %+v", first)
	}
	repeated, err := InspectDatabaseVersion(context.Background(), db, "mysql", "", "")
	if err != nil || repeated.Signature != first.Signature {
		t.Fatal("unchanged server did not produce stable capability signature")
	}
	changed, err := InspectDatabaseVersion(context.Background(), db, "mysql", "auto", "")
	if err != nil || changed.Signature == first.Signature {
		t.Fatal("session configuration change did not invalidate capability signature")
	}
}

func TestMySQLVersionInspectionRejectsOtherProductsVersionsAndConfigurations(t *testing.T) {
	for _, item := range []struct {
		name, version, product, code string
		change                       func(*expectation)
	}{
		{"mariadb", "5.5.5-10.11.8-MariaDB", "mariadb.org binary distribution", "unsupported_database_product", nil},
		{"tidb", "8.0.11-TiDB-v8.0.0", "MySQL-compatible TiDB", "unsupported_database_product", nil},
		{"percona", "26.7.0", "Percona Server", "unsupported_database_product", nil},
		{"unknown vendor", "26.7.0", "compatible database", "unsupported_database_product", nil},
		{"future major", "27.0.0", "MySQL Community Server - GPL", "unsupported_database_version", nil},
		{"unregistered patch", "26.7.1", "MySQL Community Server - GPL", "unsupported_database_version", nil},
		{"legacy mysql", "5.7.44", "MySQL Community Server - GPL", "unsupported_database_version", nil},
		{"malformed", "26.7", "MySQL Community Server - GPL", "unsupported_database_version", nil},
		{"prerelease", "26.7.0-rc1", "MySQL Community Server - GPL", "unsupported_database_version", nil},
		{"unavailable collation", "26.7.0", "MySQL Community Server - GPL", "unsupported_database_configuration", func(e *expectation) { e.rows[0][7] = false }},
		{"transactional engine", "26.7.0", "MySQL Community Server - GPL", "unsupported_database_configuration", func(e *expectation) { e.rows[0][2] = "MyISAM" }},
		{"connection encoding", "26.7.0", "MySQL Community Server - GPL", "unsupported_database_configuration", func(e *expectation) { e.rows[0][3] = "latin1" }},
	} {
		t.Run(item.name, func(t *testing.T) {
			query := mysqlVersionFixture(item.version, item.product)
			if item.change != nil {
				item.change(&query)
			}
			db, _ := newFixtureDB(t, query)
			result, err := InspectDatabaseVersion(context.Background(), db, "mysql", "auto", "")
			requireVersionError(t, err, item.code)
			if result.ServerVersion != item.version || result.CapabilityProfile != "" || result.Signature != "" || len(result.Capabilities) != 0 {
				t.Fatalf("unsupported identity hidden or capabilities enabled: %+v", result)
			}
		})
	}
}

func TestPostgreSQLVersionInspectionValidatesNumericVersionAndKnownProduct(t *testing.T) {
	for _, item := range []struct {
		name, version, numeric, banner, code string
		change                               func(*expectation)
	}{
		{"official", "17.6", "170006", "PostgreSQL 17.6 on x86_64-pc-linux-gnu", "", nil},
		{"distribution", "17.6 (Debian 17.6-1.pgdg13+1)", "170006", "PostgreSQL 17.6 (Debian 17.6-1.pgdg13+1)", "", nil},
		{"future version", "19.1", "190001", "PostgreSQL 19.1", "unsupported_database_version", nil},
		{"numeric mismatch", "17.6", "170007", "PostgreSQL 17.6", "unsupported_database_version", nil},
		{"prerelease", "17beta1", "170000", "PostgreSQL 17beta1", "unsupported_database_version", nil},
		{"unknown vendor", "17.6", "170006", "compatible postgres database", "unsupported_database_product", nil},
		{"cockroach", "17.6", "170006", "PostgreSQL 17.6 compatible CockroachDB", "unsupported_database_product", nil},
		{"yugabyte", "17.6", "170006", "PostgreSQL 17.6-YB YugabyteDB", "unsupported_database_product", nil},
		{"server encoding", "17.6", "170006", "PostgreSQL 17.6", "unsupported_database_configuration", func(e *expectation) { e.rows[0][3] = "LATIN1" }},
		{"client encoding", "17.6", "170006", "PostgreSQL 17.6", "unsupported_database_configuration", func(e *expectation) { e.rows[0][4] = "LATIN1" }},
		{"string semantics", "17.6", "170006", "PostgreSQL 17.6", "unsupported_database_configuration", func(e *expectation) { e.rows[0][5] = "off" }},
	} {
		t.Run(item.name, func(t *testing.T) {
			query := postgresVersionFixture(item.version, item.numeric, item.banner)
			if item.change != nil {
				item.change(&query)
			}
			db, _ := newFixtureDB(t, query)
			result, err := InspectDatabaseVersion(context.Background(), db, "postgresql", "auto", "")
			if item.code != "" {
				requireVersionError(t, err, item.code)
				if result.ServerVersion != item.version || len(result.Capabilities) != 0 {
					t.Fatal("diagnostic identity lost or unsupported capability enabled")
				}
				return
			}
			if err != nil || result.Product != "postgresql" || result.VersionLine != "17" || result.Signature == "" {
				t.Fatalf("supported PostgreSQL rejected: %+v %v", result, err)
			}
		})
	}
}

func TestVersionMismatchReturnsActualDetectedIdentity(t *testing.T) {
	db, _ := newFixtureDB(t, postgresVersionFixture("18.1", "180001", "PostgreSQL 18.1"))
	actual, err := InspectDatabaseVersion(context.Background(), db, "postgresql", "pinned", "17")
	requireVersionError(t, err, "database_version_mismatch")
	if actual.VersionLine != "18" || actual.ServerVersion != "18.1" {
		t.Fatal("mismatch diagnostics did not include the actual server version")
	}
}

func TestVersionInspectionDoesNotLeakDriverFailure(t *testing.T) {
	db, _ := newFixtureDB(t, expectation{op: "query", contains: "SELECT VERSION()", err: errors.New("password=secret fixture DSN")})
	_, err := InspectDatabaseVersion(context.Background(), db, "mysql", "auto", "")
	requireVersionError(t, err, "database_version_unavailable")
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "DSN") {
		t.Fatal("version diagnostic leaked driver details")
	}
}

func TestPlatformCertificationHasIndependentServerRanges(t *testing.T) {
	db, _ := newFixtureDB(t, mysqlVersionFixture("26.7.0", "MySQL Community Server - GPL"))
	actual, err := InspectDatabaseVersion(context.Background(), db, "mysql", "auto", "")
	if err != nil || validatePlatformDatabaseVersion(actual) != nil {
		t.Fatal("current certified platform server was rejected", err)
	}
	for _, changed := range []DatabaseVersion{
		{Product: "mysql", ServerVersion: "26.7.1", VersionLine: "26.7", CapabilityProfile: actual.CapabilityProfile},
		{Product: "mysql", ServerVersion: "27.0.0", VersionLine: "27.0", CapabilityProfile: actual.CapabilityProfile},
		{Product: "mysql", ServerVersion: "26.7.0", VersionLine: "26.7", CapabilityProfile: "different-profile"},
		{Product: "postgresql", ServerVersion: "17.11", VersionLine: "17", CapabilityProfile: "postgresql-17-v1"},
	} {
		requireVersionError(t, validatePlatformDatabaseVersion(changed), "unsupported_platform_database_version")
	}
}

func TestExternalSourceCertificationDoesNotOpenPlatformStorage(t *testing.T) {
	// Model an external-source certification without certifying control-table
	// migrations or service repositories on this product/version.
	before := append([]versionRegistration(nil), databaseVersionRegistrations...)
	t.Cleanup(func() { databaseVersionRegistrations = before })
	for i := range databaseVersionRegistrations {
		registration := &databaseVersionRegistrations[i]
		if registration.option.Driver == "mysql" && registration.option.VersionLine == "8.4" {
			registration.option.Status = "supported"
		}
		if registration.option.Driver == "mysql" && registration.option.VersionLine == "26.7" {
			registration.maximum = [3]int{26, 7, 1}
		}
	}
	for _, server := range []string{"8.4.9", "26.7.1"} {
		db, _ := newFixtureDB(t, mysqlVersionFixture(server, "MySQL Community Server - GPL"))
		actual, err := InspectDatabaseVersion(context.Background(), db, "mysql", "auto", "")
		if err != nil || len(actual.Capabilities) == 0 {
			t.Fatal("new external-source certification did not open its source range", err)
		}
		requireVersionError(t, validatePlatformDatabaseVersion(actual), "unsupported_platform_database_version")
	}
}
