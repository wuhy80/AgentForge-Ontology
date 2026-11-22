package identity_test

import (
	"encoding/json"
	"net/url"
	"testing"
)

func TestMySQLMentionAccountDirectory(t *testing.T) {
	f := newGroupFixture(t)
	users := f.seedUsers(2)
	path := "/metadata/admin/mention-accounts?prefix=" + url.QueryEscape(users[0].DisplayName)
	f.check(f.request("GET", path, nil, "", nil), 401)
	person := f.login(users[0].Username)
	f.check(f.request("GET", path, &person, "", nil), 403)
	result := f.request("GET", path, &f.admin, "", nil)
	f.check(result, 200)
	var body struct {
		Data    []map[string]string
		HasMore bool
	}
	if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || len(body.Data[0]) != 2 || body.Data[0]["id"] != users[0].ID ||
		body.Data[0]["label"] != users[0].DisplayName || body.HasMore {
		t.Fatal("unexpected account projection")
	}
	wildcard := f.request("GET", "/metadata/admin/mention-accounts?prefix=%25", &f.admin, "", nil)
	f.check(wildcard, 200)
	body.Data = nil
	if err := json.Unmarshal(wildcard.Body.Bytes(), &body); err != nil || len(body.Data) != 0 {
		t.Fatal("literal wildcard prefix did not remain literal")
	}
}
