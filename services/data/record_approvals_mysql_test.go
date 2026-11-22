package data

import (
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestMySQLRecordApprovalConcurrency(t *testing.T) {
	f := newMutationFixture(t)
	row := f.create("approval")
	applicant := f.p.Subject
	reviewer := c.NewID()
	f.mentionCheck = func(check c.MentionTargetCheck) int {
		if check.Actor.Subject == applicant && len(check.TargetIDs) == 1 && check.TargetIDs[0] == reviewer {
			return 204
		}
		return 403
	}
	path := "/instances/" + row.ID + "/approvals"
	f.call("POST", path, map[string]string{"assignee": reviewer}, "", 204)
	f.call("POST", path, map[string]string{"assignee": reviewer}, "", 409)
	history := func() []recordApproval {
		return mutationDecode[struct{ Data []recordApproval }](t, f.call("GET", path, nil, "", 200)).Data
	}
	entries := history()
	if len(entries) != 1 || entries[0].CanDecide {
		t.Fatalf("invalid applicant view: %+v", entries)
	}
	id := entries[0].ID
	f.call("PUT", path+"/"+id, map[string]string{"decision": "approve"}, "", 404)
	f.p.Subject = reviewer
	entries = history()
	if len(entries) != 1 || !entries[0].CanDecide {
		t.Fatalf("invalid reviewer view: %+v", entries)
	}
	f.call("PUT", path+"/"+id, map[string]string{"decision": "return"}, "", 422)

	endpoint := "/data/schemas/" + row.SchemaID + path + "/" + id
	statuses := make(chan int, 2)
	for _, body := range []string{`{"decision":"approve"}`, `{"decision":"return","note":"revise"}`} {
		go func() {
			req := httptest.NewRequest("PUT", endpoint, strings.NewReader(body))
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req.WithContext(platform.WithPrincipal(req.Context(), f.p)))
			statuses <- rec.Code
		}()
	}
	outcomes := map[int]int{}
	for range 2 {
		outcomes[<-statuses]++
	}
	if outcomes[204] != 1 || outcomes[409] != 1 {
		t.Fatalf("unexpected decisions: %v", outcomes)
	}
	entries = history()
	if len(entries) != 1 || entries[0].State == "pending" || entries[0].CanDecide || entries[0].DecidedAt == nil {
		t.Fatalf("invalid resolved history: %+v", entries)
	}
	f.p.Subject = c.NewID()
	if len(history()) != 0 {
		t.Fatal("unrelated account sees approval")
	}
}
