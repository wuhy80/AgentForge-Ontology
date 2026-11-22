package data

import (
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
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
	type unreadCount struct{ Unread int }
	if got := mutationDecode[unreadCount](t, f.call("GET", inbox, nil, "", 200)); got.Unread != 1 {
		t.Fatalf("unread count: %d", got.Unread)
	}
	f.p.Subject = c.NewID()
	f.call("PUT", inbox+"/"+out.Data[1].ID, nil, "", 404)
	f.p.Subject = author
	f.call("PUT", inbox+"/"+out.Data[1].ID, nil, "", 204)
	f.call("PUT", inbox+"/"+out.Data[1].ID, nil, "", 204)
	if got := mutationDecode[unreadCount](t, f.call("GET", inbox, nil, "", 200)); got.Unread != 0 {
		t.Fatalf("unread after marking: %d", got.Unread)
	}
	f.call("DELETE", "/instances/"+row.ID, nil, row.ETag, 204)
	f.call("GET", path, nil, "", 404)
}
