package data

import c "github.com/wuhy80/AgentForge-Ontology/contracts"

// Context readers observe committed platform state in a separate transaction.
// Until coordinated attribute locking is available, a write cannot also change
// any source Schema used by its own authorization, including across a batch.
func validateWriteContext(current c.CurrentDefinition, decisions []c.Decision, targets map[string]bool) error {
	for _, decision := range decisions {
		if len(decision.ContextSchemaIDs) == 0 {
			continue
		}
		if current.Definition.Schema.StorageBinding != nil {
			return problem(422, "context_write_unsupported", "对象上下文暂不支持外部来源写入")
		}
		for _, id := range decision.ContextSchemaIDs {
			if targets[id] {
				return problem(422, "context_source_mutation", "同一次写入不能修改本次判权的对象来源模型")
			}
		}
	}
	return nil
}

func mutationContextDecisions(decisions mutationDecisions) []c.Decision {
	out := make([]c.Decision, 0, len(decisions))
	for _, decision := range decisions {
		out = append(out, decision)
	}
	return out
}
