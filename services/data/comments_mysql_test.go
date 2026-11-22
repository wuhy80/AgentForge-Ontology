package data

import (
	"context"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestMySQLCommentInboxPagination(t *testing.T) {
	f := newMutationFixture(t)
	row := f.create("inbox-page")
	path := "/instances/" + row.ID + "/comments"
	parent := mutationDecode[c.RecordComment](t, f.call("POST", path,
		map[string]string{"body": "parent", "requestId": c.NewID()}, "", 201))
	schema := f.current.Definition.ID
	author := c.NewID()
	// Equal timestamps exercise the stable ID tie-breaker across the page boundary.
	at := time.Now().UTC().Truncate(time.Microsecond)
	for range replyInboxBatch + 2 {
		_, err := f.engine.DB().ExecContext(context.Background(), "INSERT INTO af_data_record_comments (tenant_id,schema_id,entity_id,id,author,parent_id,body,created_at,etag) VALUES (?,?,?,?,?,?,?,?,?)",
			f.p.TenantID, schema, row.ID, c.NewID(), author, parent.ID, "reply", at, c.NewID())
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = f.engine.DB().Exec("DELETE FROM af_data_record_comments WHERE tenant_id=?", f.p.TenantID)
	})
	inbox := func(cursor string, status int) struct {
		Data       []inboxReply
		NextCursor string
	} {
		r := httptest.NewRequest("GET", "/data/inbox/comment-replies?cursor="+url.QueryEscape(cursor), nil)
		r = r.WithContext(platform.WithPrincipal(r.Context(), f.p))
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("inbox HTTP %d, want %d", w.Code, status)
		}
		if status != 200 {
			return struct {
				Data       []inboxReply
				NextCursor string
			}{}
		}
		return mutationDecode[struct {
			Data       []inboxReply
			NextCursor string
		}](t, w)
	}
	first := inbox("", 200)
	if len(first.Data) != replyInboxBatch || first.NextCursor == "" {
		t.Fatal("first page is incomplete")
	}
	second := inbox(first.NextCursor, 200)
	if len(second.Data) != 2 || second.NextCursor != "" {
		t.Fatal("second page is incomplete")
	}
	seen := map[string]bool{}
	for _, item := range append(first.Data, second.Data...) {
		if item.RecordID != row.ID || seen[item.ID] {
			t.Fatal("unexpected or duplicated reply")
		}
		seen[item.ID] = true
	}
	f.mu.Lock()
	f.policy["instance.read"] = c.Decision{Allow: false}
	f.mu.Unlock()
	denied := inbox(first.NextCursor, 200)
	if len(denied.Data) != 0 {
		t.Fatal("continuation returned revoked replies")
	}
	f.p.Subject = author
	inbox(first.NextCursor, 422)
}

func TestMySQLRecordNotifications(t *testing.T) {
	f := newMutationFixture(t)
	row := f.create("record-notifications")
	path := "/instances/" + row.ID + "/comments"
	parent := mutationDecode[c.RecordComment](t, f.call("POST", path,
		map[string]string{"body": "parent", "requestId": c.NewID()}, "", 201))
	recipient := f.p.Subject
	f.p.Subject = c.NewID()
	reply := mutationDecode[c.RecordComment](t, f.call("POST", path,
		map[string]string{"body": "reply", "requestId": c.NewID(), "parentId": parent.ID}, "", 201))
	const inbox = "/data/inbox/notifications"
	mark := inbox + "/comment_reply/" + row.SchemaID + "/" + row.ID + "/" + reply.ID
	call := func(method, path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r.WithContext(platform.WithPrincipal(r.Context(), f.p)))
		if w.Code != status {
			t.Fatalf("%s %s returned %d, want %d", method, path, w.Code, status)
		}
		return w
	}
	list := func() []c.RecordNotice {
		return mutationDecode[c.RecordNoticePage](t, call("GET", inbox, 200)).Data
	}
	if len(list()) != 0 {
		t.Fatal("sender can see recipient's notifications")
	}
	call("PUT", mark, 404)
	f.p.Subject = recipient
	if got := list(); len(got) != 1 || got[0].ID != reply.ID || got[0].Read || got[0].Kind != "comment_reply" {
		t.Fatalf("unexpected unread notice: %+v", got)
	}
	call("PUT", mark, 204)
	call("PUT", mark, 204)
	if got := list(); len(got) != 1 || !got[0].Read {
		t.Fatalf("read notice missing from history: %+v", got)
	}
	legacy := call("GET", "/data/inbox/comment-replies", 200)
	if len(mutationDecode[struct{ Data []inboxReply }](t, legacy).Data) != 0 {
		t.Fatal("legacy inbox still lists acknowledged reply")
	}
	f.mu.Lock()
	f.policy["instance.read"] = c.Decision{Allow: false}
	f.mu.Unlock()
	if len(list()) != 0 {
		t.Fatal("revoked record still appears in history")
	}
	call("PUT", mark, 403)
}

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
