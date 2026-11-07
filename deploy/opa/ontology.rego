package ontology

import rego.v1

# Initial policy deliberately accepts one complete grant per operation/resource.
# Row and field constraints remain coupled; no cross-role Cartesian union.
default decision := {"allow": false}

runtime_present if {
	"runtime" in object.keys(input)
}

# The authoritative service supplies one complete grant scoped to this exact
# identity and operation. Merely including runtime disables all static grants,
# even when runtime is null, malformed, empty or explicitly denies access.
decision := input.runtime.grant if {
	runtime_present
	is_object(input.runtime)
	is_string(input.runtime.state)
	input.runtime.state != ""
	input.runtime.tenantId == input.principal.tenantId
	input.runtime.subject == input.principal.subject
	input.runtime.operation == input.request.operation
	input.runtime.resourceId == object.get(input.request, "resourceId", "")
	input.principal.tenantId != ""
	input.principal.subject != ""
	input.runtime.grant.allow == true
	input.runtime.grant.state == input.runtime.state
}

grant := specific if {
	specific := data.grants[input.principal.tenantId][input.principal.subject][input.request.operation][input.request.resourceId]
} else := wildcard if {
	wildcard := data.grants[input.principal.tenantId][input.principal.subject][input.request.operation]["*"]
}

decision := {
	"allow": true,
	"state": data.authorization_state,
	"readFields": object.get(grant, "readFields", []),
	"writeFields": object.get(grant, "writeFields", []),
	"rowFilter": object.get(grant, "rowFilter", null),
	"newRowFilter": object.get(grant, "newRowFilter", null),
} if {
	not runtime_present
	input.principal.tenantId != ""
	input.principal.subject != ""
	grant.allow == true
	is_string(data.authorization_state)
	data.authorization_state != ""
}
