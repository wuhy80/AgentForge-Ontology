package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/libs/storage"
)

// Requires an explicitly disposable database. This test deliberately fills the
// entire invented-account partition to prove it cannot block real accounts.
func TestMySQLSharedLoginBudget(t *testing.T) {
	dsn := os.Getenv("IDENTITY_THROTTLE_TEST_DSN")
	if dsn == "" {
		t.Skip("set IDENTITY_THROTTLE_TEST_DSN to a disposable MySQL database")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal("open isolated login-budget database")
	}
	defer db.Close()
	ctx := context.Background()
	var existingTables int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE()").Scan(&existingTables); err != nil || existingTables != 0 {
		t.Fatal("shared login budget test requires an empty disposable database")
	}
	if err := storage.Migrate(ctx, db, "metadata", Migrations); err != nil {
		t.Fatal("explicit identity migrations failed")
	}
	first, second := NewStore(db, nil, "https://login.test"), NewStore(db, nil, "https://login.test")
	tenant := "login-" + c.NewID()
	if err := first.Bootstrap(ctx, tenant, "ReviewUser", "LoginBudgetReview!2026"); err != nil {
		t.Fatal("bootstrap isolated account")
	}
	input := c.LoginRequest{Tenant: tenant, Username: " reviewuser ", Password: "incorrect"}
	r := httptest.NewRequest("POST", "https://login.test/metadata/auth/login", nil)
	r.RemoteAddr = "198.51.100.1:20000"
	var mu sync.Mutex
	var accepted []loginReservation
	limited := 0
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := []*Store{first, second}[i%2]
			reservation, err := store.loginBudget(ctx, r, input)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				accepted = append(accepted, reservation)
				return
			}
			var access *platform.AccessError
			if errors.As(err, &access) && access.Status == 429 {
				limited++
			} else {
				t.Error("unexpected shared protection failure")
			}
		}(i)
	}
	wg.Wait()
	if len(accepted) != 5 || limited != 7 {
		t.Fatalf("atomic admission accepted=%d limited=%d", len(accepted), limited)
	}
	if _, err := NewStore(db, nil, "https://login.test").loginBudget(ctx, r, input); err == nil {
		t.Fatal("new Store reset the same identity's failed attempts")
	}
	if err := second.releaseLoginBudget(ctx, accepted[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := first.loginBudget(ctx, r, input); err != nil {
		t.Fatal("a successful reservation did not release exactly one slot")
	}
	if _, err := first.loginBudget(ctx, r, input); err == nil {
		t.Fatal("release incorrectly erased previous failures")
	}
	if _, err := db.ExecContext(ctx, "UPDATE af_identity_login_budgets SET expires_at=UNIX_TIMESTAMP()-1 WHERE bucket_id=?", accepted[0].bucket); err != nil {
		t.Fatal("expire isolated budget")
	}
	current, err := second.loginBudget(ctx, r, input)
	if err != nil {
		t.Fatal("expired budget did not recover using database time")
	}
	if err := first.releaseLoginBudget(ctx, accepted[0]); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT attempt_count FROM af_identity_login_budgets WHERE bucket_id=?", current.bucket).Scan(&count); err != nil || count != 1 {
		t.Fatal("old reservation cleared a newer window")
	}
	var secret []byte
	if err := db.QueryRowContext(ctx, "SELECT secret FROM af_identity_login_budget_key WHERE id=1").Scan(&secret); err != nil {
		t.Fatal("read isolated shared key")
	}
	publicIP := ""
	for suffix := 2; suffix < 255; suffix++ {
		candidate := fmt.Sprintf("198.51.100.%d:20000", suffix)
		request := r.Clone(ctx)
		request.RemoteAddr = candidate
		bucket, _ := sharedLoginBucket(secret, loginKey(request, input), true)
		if bucket != current.bucket {
			publicIP = candidate
			break
		}
	}
	if publicIP == "" {
		t.Fatal("choose independent public login test source")
	}
	publicLogin := func(store *Store, username string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(c.LoginRequest{Tenant: tenant, Username: username, Password: "incorrect"})
		request := httptest.NewRequest("POST", "https://login.test/metadata/auth/login", strings.NewReader(string(body)))
		request.RemoteAddr = publicIP
		request.Header.Set("Origin", "https://login.test")
		response := httptest.NewRecorder()
		store.NewHandler().ServeHTTP(response, request)
		return response
	}
	unknown := publicLogin(first, "does-not-exist")
	for i := 0; i < 5; i++ {
		response := publicLogin([]*Store{first, second}[i%2], []string{"REVIEWUSER", " reviewuser "}[i%2])
		if response.Code != http.StatusUnauthorized || response.Body.String() != unknown.Body.String() {
			t.Fatal("bad password and invented account did not use the same login failure response")
		}
	}
	for _, store := range []*Store{first, second, NewStore(db, nil, "https://login.test")} {
		response := publicLogin(store, "ReviewUser")
		if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "900" {
			t.Fatal("public login bypassed normalized shared failure budget across Stores")
		}
	}
	// All possible invented-name buckets may be full; a real user's namespace
	// must remain available regardless of their HMAC values or table row count.
	digits := "(SELECT 0 n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9)"
	number := "a.n+10*b.n+100*c.n+1000*d.n+10000*e.n"
	fill := fmt.Sprintf("INSERT INTO af_identity_login_budgets (bucket_id,key_hash,attempt_count,expires_at) SELECT %d+%s,REPEAT('x',32),5,UNIX_TIMESTAMP()+900 FROM %s a CROSS JOIN %s b CROSS JOIN %s c CROSS JOIN %s d CROSS JOIN %s e WHERE %s<%d ON DUPLICATE KEY UPDATE attempt_count=5,expires_at=UNIX_TIMESTAMP()+900", loginBucketsPerClass, number, digits, digits, digits, digits, digits, number, loginBucketsPerClass)
	if _, err := db.ExecContext(ctx, fill); err != nil {
		t.Fatal("fill disposable invented-account partition")
	}
	if _, err := first.loginBudget(ctx, r, c.LoginRequest{Tenant: tenant, Username: "invented-account"}); err == nil {
		t.Fatal("full invented-name bucket was not limited")
	}
	if _, err := first.loginBudget(ctx, r, input); err != nil {
		t.Fatal("invented-name spray blocked a known account")
	}
	var inventedRows, allRows int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*),SUM(bucket_id>=?) FROM af_identity_login_budgets", loginBucketsPerClass).Scan(&allRows, &inventedRows); err != nil || inventedRows != loginBucketsPerClass || allRows > 2*loginBucketsPerClass {
		t.Fatal("persistent state exceeded its fixed partition budget")
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO af_identity_login_budgets (bucket_id,key_hash,attempt_count,expires_at) VALUES (131072,REPEAT('x',32),0,0)"); err == nil {
		t.Fatal("database did not enforce the bucket bound")
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM af_identity_login_budget_key WHERE id=1"); err != nil {
		t.Fatal("remove isolated protection key")
	}
	_, err = second.loginBudget(ctx, r, input)
	var access *platform.AccessError
	if !errors.As(err, &access) || access.Status != 503 || !strings.Contains(access.Code, "protection") {
		t.Fatal("missing shared state fell back to local admission")
	}
}
