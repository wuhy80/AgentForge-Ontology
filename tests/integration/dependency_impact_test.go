package integration_test

import (
	"net/http"
	"testing"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// All definitions, drafts, grants and reads go through the real public session
// chain. Database access is limited to the parent fixture's isolated cleanup.
func verifyDefinitionImpactHTTP(t *testing.T, admin, outsider *publicSecurityHTTP, client *http.Client, metadataURL, tenant string, category, group c.Organization, schemaID, enumID string) {
	t.Helper()
	base := metadataURL + "/metadata/definitions"
	action := c.Definition{Kind: c.KindAction, Name: "依赖影响动作", CategoryID: category.ID, GroupID: group.ID, Action: &c.Callable{SideEffects: "none", TargetSchemaIDs: []string{schemaID}}}
	var draft, current c.CurrentDefinition
	headers := admin.request("POST", base, action, nil, 201, &draft)
	actionURL := base + "/" + draft.Definition.ID
	admin.request("POST", actionURL+"/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
	admin.request("GET", actionURL, nil, nil, 200, &current)
	candidate := current.Definition
	candidate.Name = "依赖影响动作草稿"
	admin.request("PUT", actionURL+"/draft", candidate, map[string]string{"If-Match": `"` + current.ETag + `"`}, 200, &draft)
	impactURL := base + "/" + schemaID + "/dependencies"
	var report c.DefinitionDependencyImpact
	admin.request("GET", impactURL, nil, nil, 200, &report)
	if len(report.Dependencies) != 1 || report.Dependencies[0].Definition.ID != enumID || len(report.Dependents) != 2 {
		t.Fatal("public report omitted current or saved draft references")
	}
	admin.request("GET", actionURL+"/dependencies?view=draft", nil, nil, 200, &report)
	if report.Definition.ETag != draft.ETag || report.Definition.Name != candidate.Name {
		t.Fatal("draft identity/ETag mixed with current")
	}
	outsider.request("GET", impactURL, nil, nil, 404, nil)
	var role c.Role
	var user c.User
	admin.request("POST", metadataURL+"/metadata/admin/roles", c.RoleInput{Name: "依赖查看验收"}, nil, 201, &role)
	const password = "DependencyImpactFixture!2026"
	admin.request("POST", metadataURL+"/metadata/admin/users", c.UserInput{Username: "impact-reader", DisplayName: "依赖查看用户", Password: password, Enabled: true, RoleIDs: []string{role.ID}}, nil, 201, &user)
	var policy c.PolicyRecord
	admin.request("GET", metadataURL+"/metadata/admin/policy", nil, nil, 200, &policy)
	policy.Current.Rules = append(policy.Current.Rules,
		c.PermissionRule{ID: c.NewID(), RoleID: role.ID, Effect: "allow", Operation: "definition.read", ResourceID: schemaID},
		c.PermissionRule{ID: c.NewID(), RoleID: role.ID, Effect: "allow", Operation: "definition.read", ResourceID: current.Definition.ID})
	savePolicy := func() {
		headers := admin.request("PUT", metadataURL+"/metadata/admin/policy", policy.Current, map[string]string{"If-Match": `"` + policy.ETag + `"`}, 200, &policy)
		admin.request("POST", metadataURL+"/metadata/admin/policy/publish", nil, map[string]string{"If-Match": headers.Get("ETag")}, 200, nil)
		admin.request("GET", metadataURL+"/metadata/admin/policy", nil, nil, 200, &policy)
	}
	login := func() *publicSecurityHTTP {
		browser := &publicSecurityHTTP{t: t, client: client}
		var session c.Session
		headers := browser.request("POST", metadataURL+"/metadata/auth/login", c.LoginRequest{Tenant: tenant, Username: user.Username, Password: password}, nil, 200, &session)
		response := http.Response{Header: headers}
		browser.cookies, browser.csrf = response.Cookies(), session.CSRFToken
		return browser
	}
	savePolicy()
	reader := login()
	report = c.DefinitionDependencyImpact{}
	reader.request("GET", impactURL, nil, nil, 200, &report)
	if len(report.Dependencies) != 0 || len(report.Dependents) != 1 || report.Dependents[0].Definition.View != "current" {
		t.Fatal("definition read disclosed hidden enum or editable draft")
	}
	reader.request("GET", actionURL+"/dependencies?view=draft", nil, nil, 404, nil)
	policy.Current.Rules = append(policy.Current.Rules, c.PermissionRule{ID: c.NewID(), RoleID: role.ID, Effect: "allow", Operation: "definition.edit", ResourceID: current.Definition.ID})
	savePolicy()
	reader = login()
	report = c.DefinitionDependencyImpact{}
	reader.request("GET", impactURL, nil, nil, 200, &report)
	if len(report.Dependents) != 2 {
		t.Fatal("draft edit authorization did not expose saved reference")
	}
	// Removing root read cannot be bypassed by owning an editable referencing draft.
	kept := []c.PermissionRule{}
	for _, rule := range policy.Current.Rules {
		if rule.RoleID == role.ID && rule.ResourceID == schemaID {
			continue
		}
		kept = append(kept, rule)
	}
	policy.Current.Rules = kept
	savePolicy()
	reader = login()
	reader.request("GET", impactURL, nil, nil, 404, nil)
}
