package data

import (
	"net/http/httptest"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestMySQLRecordComments(t *testing.T) {
	f := newMutationFixture(t)
	row := f.create("commented")
	path := "/instances/" + row.ID + "/comments"
	request := map[string]string{"body": "first", "requestId": c.NewID()}
	first := mutationDecode[c.RecordComment](t, f.call("POST", path, request, "", 201))
	f.call("POST", path, request, "", 200)
	author := f.p.Subject
	f.p.Subject = c.NewID()
	f.call("POST", path, map[string]string{"body": "reply", "requestId": c.NewID(), "parentId": first.ID}, "", 201)
	f.p.Subject = author
	f.call("PATCH", path+"/"+first.ID, map[string]string{"body": "updated"}, first.ETag, 204)
	f.call("PATCH", path+"/"+first.ID, map[string]string{"body": "stale"}, first.ETag, 412)
	out := mutationDecode[c.RecordCommentPage](t, f.call("GET", path, nil, "", 200))
	if len(out.Data) != 2 || !out.CanPost || out.Data[0].Body != "updated" || out.Data[1].ParentID != first.ID || out.Data[0].Author != f.p.Subject {
		t.Fatalf("unexpected comment thread: %+v", out)
	}
	inbox := "/instances/" + row.ID + "/comment-replies"
	workspaceInbox := func() []inboxReply {
		r := httptest.NewRequest("GET", "/data/inbox/comment-replies", nil)
		r = r.WithContext(platform.WithPrincipal(r.Context(), f.p))
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("inbox HTTP %d", w.Code)
		}
		return mutationDecode[struct{ Data []inboxReply }](t, w).Data
	}
	if got := workspaceInbox(); len(got) != 1 || got[0].RecordID != row.ID || got[0].ID != out.Data[1].ID {
		t.Fatalf("unexpected workspace inbox: %+v", got)
	}
	f.mu.Lock()
	f.policy["instance.read"] = c.Decision{Allow: false}
	f.mu.Unlock()
	if got := workspaceInbox(); len(got) != 0 {
		t.Fatal("reply remained visible after record permission was revoked")
	}
	f.call("PUT", inbox+"/"+out.Data[1].ID, nil, "", 403)
	f.mu.Lock()
	delete(f.policy, "instance.read")
	f.mu.Unlock()
	type unreadCount struct{ Unread int }
	if got := mutationDecode[unreadCount](t, f.call("GET", inbox, nil, "", 200)); got.Unread != 1 {
		t.Fatalf("unread count: %d", got.Unread)
	}
	f.p.Subject = c.NewID()
	if got := workspaceInbox(); len(got) != 0 {
		t.Fatal("reply returned to another subject")
	}
	f.call("PUT", inbox+"/"+out.Data[1].ID, nil, "", 404)
	f.p.Subject = author
	f.call("PUT", inbox+"/"+out.Data[1].ID, nil, "", 204)
	if got := workspaceInbox(); len(got) != 0 {
		t.Fatal("read reply remained in workspace inbox")
	}
	f.call("PUT", inbox+"/"+out.Data[1].ID, nil, "", 204)
	if got := mutationDecode[unreadCount](t, f.call("GET", inbox, nil, "", 200)); got.Unread != 0 {
		t.Fatalf("unread after marking: %d", got.Unread)
	}
	f.call("DELETE", "/instances/"+row.ID, nil, row.ETag, 204)
	f.call("GET", path, nil, "", 404)
}
