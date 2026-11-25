package data

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestMySQLAccountMentionNotice(t *testing.T) {
	f := newMutationFixture(t)
	row := f.create("account-mention")
	actor, recipient, forbidden := f.p.Subject, c.NewID(), c.NewID()
	f.mentionCheck = func(check c.MentionTargetCheck) int {
		if check.Actor.Subject == actor && check.Actor.TenantID == f.p.TenantID &&
			len(check.TargetIDs) == 1 && check.TargetIDs[0] == recipient {
			return 204
		}
		return 403
	}
	path := "/instances/" + row.ID
	link := func(id string) string { return "[@person](#onto-mention-account-" + id + ")" }
	request := func(body string) c.WriteRequest {
		return c.WriteRequest{Values: map[string]json.RawMessage{testFieldDept: raw(body)}}
	}
	f.call("PATCH", path, request(link(forbidden)), row.ETag, 403)
	body := link(recipient) + " " + link(recipient)
	row = mutationDecode[c.Instance](t, f.call("PATCH", path, request(body), row.ETag, 200))
	f.call("PATCH", path, request("edited "+body), row.ETag, 200)

	const inbox = "/data/inbox/notifications"
	call := func(method, path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r.WithContext(platform.WithPrincipal(r.Context(), f.p)))
		if w.Code != status {
			t.Fatalf("%s %s HTTP %d, want %d", method, path, w.Code, status)
		}
		return w
	}
	failed := mutationDecode[c.RecordNoticePage](t, call("GET", inbox, 200))
	if len(failed.Data) != 1 || failed.Data[0].Kind != mentionFailureKind ||
		failed.Data[0].Delivery == nil || failed.Data[0].Delivery.Status != "failed" ||
		failed.Data[0].Delivery.Failure == "" || failed.Unread == nil || failed.Unread.Count != 0 {
		t.Fatalf("unexpected sender delivery history: %+v", failed)
	}
	f.mu.Lock()
	f.policy["instance.read"] = c.Decision{Allow: false}
	f.mu.Unlock()
	revoked := mutationDecode[c.RecordNoticePage](t, call("GET", inbox, 200))
	if len(revoked.Data) != 0 {
		t.Fatal("revoked sender can see delivery failure")
	}
	f.mu.Lock()
	delete(f.policy, "instance.read")
	f.mu.Unlock()
	f.p.Subject = recipient
	page := mutationDecode[c.RecordNoticePage](t, call("GET", inbox, 200))
	if len(page.Data) != 1 || page.Data[0].Kind != accountMentionNoticeKind || page.Unread == nil || page.Unread.Count != 1 {
		t.Fatalf("unexpected mention history: %+v", page)
	}
	mark := inbox + "/" + accountMentionNoticeKind + "/" + row.SchemaID + "/" + row.ID + "/" + page.Data[0].ID
	f.mu.Lock()
	f.policy["instance.read"] = c.Decision{Allow: true, ReadFields: []string{testFieldCode}, State: "restricted"}
	f.mu.Unlock()
	call("PUT", mark, 403)
	page = mutationDecode[c.RecordNoticePage](t, call("GET", inbox, 200))
	if len(page.Data) != 0 || page.Unread == nil || page.Unread.Count != 0 {
		t.Fatal("field revocation left a visible mention")
	}
	f.mu.Lock()
	delete(f.policy, "instance.read")
	f.mu.Unlock()
	call("PUT", mark, 204)
	call("PUT", mark, 204)
}
