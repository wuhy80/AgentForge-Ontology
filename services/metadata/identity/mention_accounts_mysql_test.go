package identity_test

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
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

func TestMySQLSharedMentionAccounts(t *testing.T) {
	f := newGroupFixture(t)
	users := f.seedUsers(3)
	group := f.createGroup(contracts.UserGroupInput{Name: "Mention", Enabled: true, UserIDs: []string{users[0].ID, users[1].ID}})
	person := f.login(users[0].Username)
	path := "/metadata/auth/mention-accounts?prefix=user-"
	f.check(f.request("GET", path, &person, "", nil), 403)
	f.store.SharedGroupMentionAccounts = true
	result := f.request("GET", path, &person, "", nil)
	f.check(result, 200)
	var body struct{ Data []struct{ ID string } }
	if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 2 {
		t.Fatalf("got %d shared-group accounts", len(body.Data))
	}
	for _, item := range body.Data {
		if item.ID != users[0].ID && item.ID != users[1].ID {
			t.Fatal("account outside shared group")
		}
	}
	f.updateGroup(group, contracts.UserGroupInput{Name: group.Name, Enabled: false, UserIDs: []string{users[0].ID, users[1].ID}})
	person = f.login(users[0].Username)
	result = f.request("GET", path, &person, "", nil)
	f.check(result, 200)
	if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil || len(body.Data) != 0 {
		t.Fatal("disabled group returned accounts")
	}
}

func TestMySQLMentionTargetValidation(t *testing.T) {
	f := newGroupFixture(t)
	users := f.seedUsers(3)
	group := f.createGroup(contracts.UserGroupInput{Name: "Mention validation", Enabled: true, UserIDs: []string{users[0].ID, users[1].ID}})
	ctx := context.Background()
	check := contracts.MentionTargetCheck{Actor: contracts.Principal{TenantID: f.tenant, Subject: users[0].ID}, TargetIDs: []string{users[1].ID}}
	if err := f.store.ValidateMentionTargets(ctx, check); err == nil {
		t.Fatal("shared directory is disabled")
	}
	f.store.SharedGroupMentionAccounts = true
	if err := f.store.ValidateMentionTargets(ctx, check); err != nil {
		t.Fatal(err)
	}
	check.TargetIDs = append(check.TargetIDs, users[2].ID)
	if err := f.store.ValidateMentionTargets(ctx, check); err == nil {
		t.Fatal("accepted user outside shared groups")
	}
	check.TargetIDs = check.TargetIDs[:1]
	f.updateGroup(group, contracts.UserGroupInput{Name: group.Name, Enabled: false, UserIDs: []string{users[0].ID, users[1].ID}})
	if err := f.store.ValidateMentionTargets(ctx, check); err == nil {
		t.Fatal("accepted target after shared group was disabled")
	}
	check.Actor.Subject = f.admin.session.User.ID
	if err := f.store.ValidateMentionTargets(ctx, check); err != nil {
		t.Fatal(err)
	}
	check.Actor.TenantID = contracts.NewID()
	if err := f.store.ValidateMentionTargets(ctx, check); err == nil {
		t.Fatal("accepted actor in an unrelated tenant")
	}
}
