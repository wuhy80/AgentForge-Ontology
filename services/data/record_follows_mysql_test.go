package data

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestMySQLFollowedRecordUpdate(t *testing.T) {
	f := newMutationFixture(t)
	row := f.create("follow-updates")
	path := "/instances/" + row.ID
	f.call("PUT", path+"/follow", nil, "", 204)
	f.call("PUT", path+"/follow", nil, "", 204)
	if got := mutationDecode[c.RecordFollow](t, f.call("GET", path+"/follow", nil, "", 200)); !got.Following {
		t.Fatal("record should be followed")
	}

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
	list := func() c.RecordNoticePage {
		return mutationDecode[c.RecordNoticePage](t, call("GET", inbox, 200))
	}
	update := func(value string) {
		row = mutationDecode[c.Instance](t, f.call("PATCH", path,
			c.WriteRequest{Values: map[string]json.RawMessage{testFieldDept: raw(value)}}, row.ETag, 200))
	}
	update("own")
	if len(list().Data) != 0 {
		t.Fatal("self-update generated a notification")
	}
	recipient := f.p.Subject
	editor := c.NewID()
	f.p.Subject = editor
	update("edited")
	if len(list().Data) != 0 {
		t.Fatal("editor can see recipient notice")
	}
	f.p.Subject = recipient
	page := list()
	if len(page.Data) != 1 || page.Data[0].Kind != recordUpdateNoticeKind || page.Unread == nil || page.Unread.Count != 1 {
		t.Fatalf("expected one unread update: %+v", page)
	}
	notice := inbox + "/" + recordUpdateNoticeKind + "/" + row.SchemaID + "/" + row.ID + "/" + page.Data[0].ID
	call("PUT", notice, 204)
	call("PUT", notice, 204)
	if page := list(); len(page.Data) != 1 || !page.Data[0].Read || page.Unread.Count != 0 {
		t.Fatalf("expected one read update: %+v", page)
	}
	f.mu.Lock()
	f.policy["instance.read"] = c.Decision{Allow: false}
	f.mu.Unlock()
	call("PUT", notice, 403)
	if len(list().Data) != 0 {
		t.Fatal("revoked record is still visible")
	}
	f.mu.Lock()
	delete(f.policy, "instance.read")
	f.mu.Unlock()
	f.call("DELETE", path+"/follow", nil, "", 204)
	f.p.Subject = editor
	update("after-unfollow")
	f.p.Subject = recipient
	if len(list().Data) != 1 {
		t.Fatal("unsubscribe did not stop new notifications")
	}
	f.call("DELETE", path, nil, row.ETag, 204)
	if len(list().Data) != 0 {
		t.Fatal("deleted record still appears in notifications")
	}
}
