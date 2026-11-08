package data

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

var writePermissionNames = []string{"instance.create", "instance.patch", "instance.delete", "instance.upsert", "instance.upsert.overwrite"}

func (h *handler) optionalWriteDecisions(ctx context.Context, p c.Principal, id string) (mutationDecisions, error) {
	if h.cfg.Authorizer == nil {
		return nil, problem(503, "authorization_unavailable", "权限服务未配置")
	}
	out := mutationDecisions{}
	state := ""
	for _, name := range writePermissionNames {
		decision, err := h.cfg.Authorizer.Authorize(ctx, p, c.AuthorizationRequest{Operation: name, ResourceID: id, Kind: c.KindSchema})
		if err != nil {
			return nil, problem(503, "authorization_unavailable", "无法确认当前写入权限")
		}
		if decision.Allow {
			if decision.State == "" {
				return nil, problem(503, "authorization_unavailable", "缺少权威授权状态标记")
			}
			if state != "" && decision.State != state {
				return nil, problem(409, "authorization_changed", "授权已改变，请重新读取")
			}
			state = decision.State
		}
		out[name] = decision
	}
	return out, nil
}

func (h *handler) mutationReadiness(ctx context.Context, current c.CurrentDefinition) (bool, string, error) {
	if current.Definition.Schema.StorageBinding != nil {
		return h.mappedMutationReadiness(ctx, current)
	}
	if h.cfg.Engine == nil {
		return false, "storage_unavailable", nil
	}
	var status, actual string
	err := h.cfg.Engine.DB().QueryRowContext(ctx, "SELECT status,digest FROM af_data_gates WHERE tenant_id=? AND definition_id=?", current.Definition.TenantID, current.Definition.ID).Scan(&status, &actual)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "mapping_unavailable", nil
	}
	if err != nil {
		return false, "storage_unavailable", err
	}
	if status != "ready" || actual != current.ETag {
		return false, "mapping_unavailable", nil
	}
	return true, "", nil
}

func capabilityDecisions(decisions mutationDecisions, name string) ([]c.Decision, bool) {
	operation := name
	if len(name) > 5 && name[:5] == "bulk_" {
		operation = name[5:]
	}
	names := []string{mutationPermission(operation)}
	if operation == "upsert" {
		names = append(names, "instance.create", "instance.patch")
	}
	out := []c.Decision{}
	for _, permission := range names {
		decision := decisions[permission]
		if !decision.Allow {
			return nil, false
		}
		out = append(out, decision)
	}
	return out, true
}

func capabilityWriteFields(schema *c.Schema, decisions []c.Decision) []string {
	fields := []string{}
	for _, property := range schema.Properties {
		allowed := !property.ReadOnly
		for _, decision := range decisions {
			allowed = allowed && fieldAllowed(decision.WriteFields, property.ID)
		}
		if allowed {
			fields = append(fields, property.ID)
		}
	}
	return fields
}

func (h *handler) writeCapabilitiesHTTP(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		writeError(w, err)
		return
	}
	current, err := h.current(r.Context(), p.TenantID, r.PathValue("schemaId"))
	if err == nil {
		err = validateSchema(current.Definition)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	decisions, err := h.optionalWriteDecisions(r.Context(), p, current.Definition.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	authorized := false
	for _, name := range []string{"instance.create", "instance.patch", "instance.delete"} {
		authorized = authorized || decisions[name].Allow
	}
	if !authorized {
		writeError(w, problem(403, "forbidden", "无权发现此对象的写入能力"))
		return
	}
	ready, reason, err := h.mutationReadiness(r.Context(), current)
	if err != nil {
		writeError(w, err)
		return
	}
	deleteReady := true
	protectedFields := map[string]bool{}
	createProtected := map[string]bool{}
	if ready && current.Definition.Schema.StorageBinding != nil {
		deleteReady, err = h.mappedDeleteReadiness(r.Context(), current)
		if err != nil {
			writeError(w, err)
			return
		}
		protectedFields, err = h.mappedProtectedWriteFields(r.Context(), current)
		if err != nil {
			writeError(w, err)
			return
		}
		createProtected, err = h.mappedCreateProtectedWriteFields(r.Context(), p, current)
		if err != nil {
			writeError(w, err)
			return
		}
	}
	out := c.WriteCapabilities{SchemaID: current.Definition.ID, Operations: []c.WriteOperationCapability{}, Fields: []c.FormField{}, MatchModes: []string{}, UniqueKeys: []c.WriteUniqueKey{}, OverwriteWriteFields: []string{}, OverwriteUniqueKeyIDs: []string{}, MaxBulkItems: c.MaxBulkWriteItems, MaxRequestBytes: c.MaxDataRequestBytes, MaxResponseBytes: c.MaxDataResponseBytes}
	visible := map[string]bool{}
	for _, name := range []string{"create", "update", "delete", "upsert", "bulk_create", "bulk_update", "bulk_delete", "bulk_upsert"} {
		operationProtected := protectedFields
		if name == "create" || name == "bulk_create" {
			operationProtected = createProtected
		}
		relevant, allow := capabilityDecisions(decisions, name)
		operation := c.WriteOperationCapability{Name: name, Allowed: ready && allow, WriteFields: []string{}}
		contextErr := validateWriteContext(current, relevant, map[string]bool{current.Definition.ID: true})
		switch {
		case !allow:
			operation.Reason = "forbidden"
		case !ready:
			operation.Reason = reason
		case contextErr != nil:
			operation.Allowed, operation.Reason = false, "context_write_unsupported"
		case (name == "delete" || name == "bulk_delete") && !deleteReady:
			operation.Allowed, operation.Reason = false, "mapped_relation_mutation_unsupported"
		}
		if allow && name != "delete" && name != "bulk_delete" {
			for _, id := range capabilityWriteFields(current.Definition.Schema, relevant) {
				if !operationProtected[id] {
					operation.WriteFields = append(operation.WriteFields, id)
					visible[id] = true
				}
			}
			for _, property := range current.Definition.Schema.Properties {
				for _, decision := range relevant {
					if fieldAllowed(decision.ReadFields, property.ID) {
						visible[property.ID] = true
					}
				}
			}
		}
		out.Operations = append(out.Operations, operation)
	}
	for _, property := range current.Definition.Schema.Properties {
		if visible[property.ID] {
			out.Fields = append(out.Fields, c.FormField{ID: property.ID, Name: property.Name, DataType: property.DataType, Required: property.Required, Nullable: property.Nullable, ReadOnly: property.ReadOnly || (protectedFields[property.ID] && createProtected[property.ID]), Precision: property.Precision, Scale: property.Scale, EnumID: property.EnumID})
		}
	}
	upsertRelevant, upsertAllowed := capabilityDecisions(decisions, "upsert")
	upsertAllowed = upsertAllowed && validateWriteContext(current, upsertRelevant, map[string]bool{current.Definition.ID: true}) == nil
	if ready && upsertAllowed {
		out.MatchModes = []string{"id"}
		out.OverwriteAllowed = decisions["instance.upsert.overwrite"].Allow
		overwriteRelevant := append([]c.Decision(nil), upsertRelevant...)
		if out.OverwriteAllowed {
			overwriteRelevant = append(overwriteRelevant, decisions["instance.upsert.overwrite"])
			out.OverwriteAllowed = validateWriteContext(current, overwriteRelevant, map[string]bool{current.Definition.ID: true}) == nil
		}
		if out.OverwriteAllowed {
			for _, fieldID := range capabilityWriteFields(current.Definition.Schema, overwriteRelevant) {
				if !protectedFields[fieldID] {
					out.OverwriteWriteFields = append(out.OverwriteWriteFields, fieldID)
				}
			}
		}
		for _, index := range current.Definition.Schema.Indexes {
			allowed := index.Unique
			for _, fieldID := range index.FieldIDs {
				property, _ := findProperty(current.Definition.Schema, fieldID)
				allowed = allowed && !property.ReadOnly && !protectedFields[fieldID]
				for _, decision := range upsertRelevant {
					allowed = allowed && fieldAllowed(decision.ReadFields, fieldID) && fieldAllowed(decision.WriteFields, fieldID)
				}
			}
			if allowed {
				out.UniqueKeys = append(out.UniqueKeys, c.WriteUniqueKey{ID: index.ID, Name: index.Name, FieldIDs: append([]string(nil), index.FieldIDs...)})
				if out.OverwriteAllowed {
					overwriteKey := true
					for _, fieldID := range index.FieldIDs {
						for _, decision := range overwriteRelevant {
							overwriteKey = overwriteKey && fieldAllowed(decision.ReadFields, fieldID) && fieldAllowed(decision.WriteFields, fieldID)
						}
					}
					if overwriteKey {
						out.OverwriteUniqueKeyIDs = append(out.OverwriteUniqueKeyIDs, index.ID)
					}
				}
			}
		}
		if len(out.UniqueKeys) > 0 {
			out.MatchModes = append(out.MatchModes, "unique_key")
		}
	}
	for _, name := range writePermissionNames {
		now, e := h.cfg.Authorizer.Authorize(r.Context(), p, c.AuthorizationRequest{Operation: name, ResourceID: current.Definition.ID, Kind: c.KindSchema})
		if e != nil {
			writeError(w, problem(503, "authorization_unavailable", "无法确认当前写入权限"))
			return
		}
		if digest(now) != digest(decisions[name]) {
			writeError(w, problem(409, "authorization_changed", "授权已改变，请重新读取"))
			return
		}
	}
	latest, err := h.current(r.Context(), p.TenantID, current.Definition.ID)
	if err != nil || latest.ETag != current.ETag {
		writeError(w, problem(409, "definition_changed", "模型已改变，请重新读取"))
		return
	}
	sort.Slice(out.UniqueKeys, func(i, j int) bool { return out.UniqueKeys[i].ID < out.UniqueKeys[j].ID })
	writeJSON(w, 200, out)
}
