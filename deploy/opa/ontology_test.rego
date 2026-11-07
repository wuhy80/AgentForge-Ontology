package ontology_test

import rego.v1

base_input := {
	"principal": {"tenantId": "tenant", "subject": "user"},
	"request": {"operation": "instance.read", "resourceId": "schema"},
}

static_grants := {"tenant": {"user": {"instance.read": {"*": {"allow": true, "readFields": ["*"]}}}}}

runtime_grant := {"allow": true, "state": "current", "readFields": ["field"], "writeFields": [], "rowFilter": {"op": "eq", "field": "field", "value": "A"}}

runtime := {
	"state": "current",
	"tenantId": "tenant",
	"subject": "user",
	"operation": "instance.read",
	"resourceId": "schema",
	"grant": runtime_grant,
}

test_static_legacy_grant_remains_available if {
	result := data.ontology.decision with input as base_input with data.grants as static_grants with data.authorization_state as "static"
	result.allow == true
	result.state == "static"
}

test_runtime_complete_grant_wins_over_static_fields if {
	request := object.union(base_input, {"runtime": runtime})
	result := data.ontology.decision with input as request with data.grants as static_grants with data.authorization_state as "static"
	result == runtime_grant
}

test_null_runtime_does_not_fall_back if {
	request := object.union(base_input, {"runtime": null})
	result := data.ontology.decision with input as request with data.grants as static_grants with data.authorization_state as "static"
	result == {"allow": false}
}

test_malformed_runtime_does_not_fall_back if {
	every malformed in [false, [], "bad", {}, {"state": "current"}] {
		request := object.union(base_input, {"runtime": malformed})
		result := data.ontology.decision with input as request with data.grants as static_grants with data.authorization_state as "static"
		result == {"allow": false}
	}
}

test_denied_runtime_does_not_fall_back if {
	denied := object.union(runtime, {"grant": {"allow": false, "state": "current"}})
	request := object.union(base_input, {"runtime": denied})
	result := data.ontology.decision with input as request with data.grants as static_grants with data.authorization_state as "static"
	result == {"allow": false}
}

test_runtime_binding_must_match_request if {
	every change in [{"tenantId": "other"}, {"subject": "other"}, {"operation": "instance.patch"}, {"resourceId": "other"}, {"state": ""}, {"state": "old"}] {
		changed := object.union(runtime, change)
		request := object.union(base_input, {"runtime": changed})
		result := data.ontology.decision with input as request with data.grants as static_grants with data.authorization_state as "static"
		result == {"allow": false}
	}
}

test_runtime_supports_resource_less_organization_operations if {
	organization_runtime := object.union(runtime, {"operation": "organization.read", "resourceId": ""})
	request := {"principal": base_input.principal, "request": {"operation": "organization.read"}, "runtime": organization_runtime}
	result := data.ontology.decision with input as request
	result == runtime_grant
}
