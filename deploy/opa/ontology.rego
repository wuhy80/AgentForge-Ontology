package ontology

import rego.v1

# Initial policy deliberately accepts one complete grant per operation/resource.
# Row and field constraints remain coupled; no cross-role Cartesian union.
default decision := {"allow": false}

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
	input.principal.tenantId != ""
	input.principal.subject != ""
	grant.allow == true
	is_string(data.authorization_state)
	data.authorization_state != ""
}
