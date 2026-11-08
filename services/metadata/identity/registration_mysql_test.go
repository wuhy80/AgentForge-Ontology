package identity_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
	"github.com/wuhy80/AgentForge-Ontology/services/metadata/identity"
)

// Capacity is global, so this test requires an explicitly prepared empty
// disposable schema. It must never fill the ordinary shared development DB.
func TestMySQLRegistrationSharedCapacityAndExpiry(t *testing.T) {
	dsn := os.Getenv("IDENTITY_QUOTA_TEST_DSN")
	if dsn == "" {
		t.Skip("set IDENTITY_QUOTA_TEST_DSN to an empty security_registration_* schema")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(cfg.DBName, "security_registration_") {
		t.Fatal("registration capacity test requires a disposable schema")
	}
	ctx := context.Background()
	engine, err := storage.Open(ctx, storage.Config{Driver: "mysql", DSN: dsn, MaxOpen: 8, MaxIdle: 2})
	if err != nil {
		t.Fatal("open disposable database")
	}
	defer engine.Close()
	register := func() *httptest.ResponseRecorder {
		s := identity.NewStore(engine.DB(), nil, "https://workbench.test")
		r := httptest.NewRequest("POST", "/oauth/register", strings.NewReader(`{"client_name":"capacity-test","redirect_uris":["http://127.0.0.1:34123/callback"]}`))
		w := httptest.NewRecorder()
		s.NewHandler().ServeHTTP(w, r)
		return w
	}
	if w := register(); w.Code != http.StatusServiceUnavailable {
		t.Fatal("unmigrated registration did not fail closed")
	}
	if err := storage.Migrate(ctx, engine.DB(), "identity_quota", identity.Migrations); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM af_identity_oauth_clients").Scan(&count); err != nil || count != 0 {
		t.Fatal("capacity test schema is not empty")
	}
	now := time.Now().Unix()
	tx, err := engine.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 1024; i++ {
		expiry := now + 3600
		if i == 1023 {
			expiry = now - 1
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO af_identity_oauth_clients (id,name,redirect_uris,created_at,expires_at) VALUES (?,?,?,?,?)", c.NewID(), fmt.Sprintf("quota-%d", i), `[]`, now, expiry); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO af_identity_oauth_codes (code_hash,tenant_id,user_id,user_etag,role_hash,client_id,redirect_uri,code_challenge,scopes,resource,expires_at,consumed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,0)", strings.Repeat("a", 64), "quota", c.NewID(), strings.Repeat("b", 64), strings.Repeat("c", 64), c.NewID(), "http://127.0.0.1/callback", strings.Repeat("d", 43), "[]", "https://resource.test/mcp", now-1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Independent Store instances simulate replicas racing for the final slot.
	statuses := make(chan int, 4)
	start := make(chan struct{})
	var group sync.WaitGroup
	for i := 0; i < 4; i++ {
		group.Add(1)
		go func() { defer group.Done(); <-start; statuses <- register().Code }()
	}
	close(start)
	group.Wait()
	close(statuses)
	accepted, rejected := 0, 0
	for status := range statuses {
		switch status {
		case 201:
			accepted++
		case 429:
			rejected++
		default:
			t.Fatalf("unexpected registration status %d", status)
		}
	}
	if accepted != 1 || rejected != 3 {
		t.Fatalf("shared quota accepted=%d rejected=%d", accepted, rejected)
	}
	if err := engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM af_identity_oauth_clients").Scan(&count); err != nil || count != 1024 {
		t.Fatal("shared quota exceeded or expired client retained")
	}
	if err := engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM af_identity_oauth_codes").Scan(&count); err != nil || count != 0 {
		t.Fatal("expired authorization code was retained")
	}
	if w := register(); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal("full registry did not reject with retry budget")
	}
	// Upgrading an unbounded legacy registry must still make cleanup progress
	// when one bounded deletion does not bring it below the new capacity.
	tx, err = engine.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 1025; i++ {
		if _, err := tx.ExecContext(ctx, "INSERT INTO af_identity_oauth_clients (id,name,redirect_uris,created_at,expires_at) VALUES (?,?,?,?,?)", c.NewID(), "legacy-expired", `[]`, now, now-1); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, remaining := range []int{1025, 1024} {
		if w := register(); w.Code != 429 {
			t.Fatal("legacy cleanup ignored active-client capacity")
		}
		if err := engine.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM af_identity_oauth_clients").Scan(&count); err != nil || count != remaining {
			t.Fatal("rejected registration rolled back expired legacy cleanup")
		}
	}
	if _, err := engine.DB().ExecContext(ctx, "UPDATE af_identity_oauth_clients SET expires_at=0 WHERE name='quota-0'"); err != nil {
		t.Fatal(err)
	}
	if w := register(); w.Code != 201 {
		t.Fatal("expired client capacity was not reclaimed")
	}
}
