package data

import (
	"context"
	"encoding/json"
	"strings"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// Inserting a non-null FK creates a Link fact. Only a certified same-source
// holder insertion is supported; existing FK changes and endpoint deletion stay closed.
func (s *mappedMutationStore) checkCreateRelations(ctx context.Context, item c.Instance) error {
	links, err := s.h.mappedWriteLinks(ctx, s.p.TenantID, s.current.Definition.ID)
	if err != nil {
		return err
	}
	for _, definition := range links {
		link := definition.Link
		b := link.StorageBinding
		owner := link.SourceSchemaID
		if b.ForeignKeySide == "target" {
			owner = link.TargetSchemaID
		}
		if b.Mode != "foreign_key" || owner != item.SchemaID {
			continue
		}
		var raw json.RawMessage
		for _, property := range s.current.Definition.Schema.Properties {
			if property.Mapping.Column == b.ForeignKeyColumn {
				raw = item.Values[property.ID]
				break
			}
		}
		if len(raw) == 0 {
			return problem(422, "mapped_relation_field_required", "外键映射缺少稳定字段绑定")
		}
		if string(raw) == "null" {
			continue
		}
		var targetID string
		if json.Unmarshal(raw, &targetID) != nil || !uuidPattern.MatchString(targetID) || targetID != strings.ToLower(targetID) {
			return problem(422, "invalid_link_instance", "关系目标必须为规范UUID")
		}
		if err := s.createForeignKeyRelation(ctx, definition, item, targetID); err != nil {
			return err
		}
	}
	return nil
}

func (s *mappedMutationStore) createForeignKeyRelation(ctx context.Context, definition c.Definition, item c.Instance, targetID string) error {
	link := definition.Link
	current, err := s.h.currentLink(ctx, s.p, definition.ID)
	if err != nil {
		return err
	}
	if digest(*current.Definition.Link) != digest(*link) {
		return problem(409, "definition_changed", "关系定义已改变")
	}
	decision, err := s.h.authorizeLink(ctx, s.p, "link.create", current.Definition.ID)
	if err != nil {
		return err
	}
	plan, err := s.h.planLink(ctx, current.Definition)
	if err != nil {
		return err
	}
	defer plan.close()
	if err := s.h.linkGates(ctx, s.platformTx, s.p, plan, &current); err != nil {
		return err
	}
	holder, referenced := plan.source, plan.target
	holderTables, targetTables := plan.sourceTables, plan.targetTables
	if link.StorageBinding.ForeignKeySide == "target" {
		holder, referenced = plan.target, plan.source
		holderTables, targetTables = plan.targetTables, plan.sourceTables
	}
	if holder.ETag != s.current.ETag || holder.Definition.ID != item.SchemaID || len(holderTables) != 1 || holderTables[0].locator() != s.table.locator() || len(targetTables) != 1 || targetTables[0].source.ID != s.table.source.ID {
		return problem(422, "cross_source_link_unsupported", "关系创建仅支持同一专用来源的单主表")
	}
	if err := validateMappedWriteTable(ctx, referenced.Definition, targetTables); err != nil {
		return err
	}
	state, enabled, err := readMappedWriteState(ctx, targetTables[0], referenced, s.tx)
	if err != nil {
		return err
	}
	if !enabled {
		return mappedWriteUnsupported("mapped_relation_target_not_initialized")
	}
	ownerDecision, err := s.h.authorize(ctx, s.p, "instance.read", holder.Definition.ID)
	if err != nil {
		return err
	}
	targetDecision, err := s.h.authorize(ctx, s.p, "instance.read", referenced.Definition.ID)
	if err != nil {
		return err
	}
	if err := checkRowFilter(ctx, s.tx, holder.Definition.Schema, item.Values, ownerDecision.RowFilter); err != nil {
		return err
	}
	targetStore := &mappedMutationStore{tx: s.tx, table: targetTables[0], current: referenced, p: s.p, state: state, h: s.h}
	target, exists, err := targetStore.Lookup(ctx, mutationMatch{ID: targetID})
	if err != nil {
		return err
	}
	if !exists {
		return problem(404, "not_found", "关系目标不存在或不可访问")
	}
	if err := checkRowFilter(ctx, s.tx, referenced.Definition.Schema, target.Values, targetDecision.RowFilter); err != nil {
		return err
	}
	s.recheckRelations = append(s.recheckRelations, func(ctx context.Context) error {
		if err := s.h.recheckLink(ctx, s.p, "link.create", current.Definition.ID, decision); err != nil {
			return err
		}
		if err := s.h.recheck(ctx, s.p, "instance.read", holder.Definition.ID, ownerDecision); err != nil {
			return err
		}
		return s.h.recheck(ctx, s.p, "instance.read", referenced.Definition.ID, targetDecision)
	})
	// FK is the relation fact; no shadow platform association row is created.
	edge := c.LinkInstance{ID: c.NewID(), LinkID: current.Definition.ID, SourceID: item.ID, TargetID: targetID, ETag: c.NewID()}
	if link.StorageBinding.ForeignKeySide == "target" {
		edge.SourceID, edge.TargetID = targetID, item.ID
	}
	raw, err := json.Marshal(edge)
	if err != nil {
		return err
	}
	_, err = s.tx.ExecContext(ctx, "INSERT INTO af_source_write_outbox(id,tenant_id,schema_id,entity_id,event_type,payload) VALUES(?,?,?,?,?,?)", c.NewID(), s.p.TenantID, current.Definition.ID, edge.ID, "link.created", string(raw))
	return err
}
