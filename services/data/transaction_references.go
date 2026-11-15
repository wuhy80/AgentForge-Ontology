package data

import (
	"encoding/json"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

// Validate references without guessing their eventual result IDs. A valid UUID
// placeholder lets the existing command validation enforce nonempty values;
// it is replaced after the source mutation succeeds and never reaches storage.
func transactionReferenceValues(schema *c.Schema, item c.TransactionOperation, prior map[string]string) (map[string]json.RawMessage, error) {
	if len(item.ValueRefs) == 0 {
		return item.Values, nil
	}
	values, err := normalizeMutationValues(schema, item.Values)
	if err != nil {
		return values, err
	}
	if item.Operation != "create" && item.Operation != "update" && item.Operation != "upsert" {
		return nil, problem(422, "invalid_transaction_reference", "只有 create/update/upsert 的 values 可引用前项结果 ID")
	}
	for field, ref := range item.ValueRefs {
		property, found := findProperty(schema, field)
		if !found || property.ID != field || property.DataType != "uuid" {
			return nil, problem(422, "invalid_transaction_reference", "结果引用须绑定稳定 UUID 属性 ID")
		}
		if _, exists := values[field]; exists {
			return nil, problem(422, "invalid_transaction_reference", "同一属性不能同时声明字面值和结果引用")
		}
		if item.Match != nil {
			if _, exists := item.Match.Values[field]; exists {
				return nil, problem(422, "invalid_transaction_reference", "结果引用不能改变 upsert 匹配键")
			}
		}
		operation := prior[ref.ClientKey]
		if !validWriteKey(ref.ClientKey) || operation != "create" && operation != "upsert" {
			return nil, problem(422, "invalid_transaction_reference", "结果引用只能指向本事务前面的 create/upsert clientKey")
		}
		values[field] = json.RawMessage(`"00000000-0000-4000-8000-000000000000"`)
	}
	return values, nil
}

// Resolve only successful, already authorized mutation results. Normal mutation
// building still enforces writable fields, types, enums and new-row policies.
func resolveTransactionReferences(item preparedTransactionOperation, completed map[string]mutationSavedItem) (mutationCommand, error) {
	command := item.command
	if len(item.valueRefs) == 0 {
		return command, nil
	}
	command.Values = make(map[string]json.RawMessage, len(item.command.Values))
	for field, value := range item.command.Values {
		command.Values[field] = value
	}
	for field, ref := range item.valueRefs {
		result, exists := completed[ref.ClientKey]
		if !exists || (result.Outcome != "created" && result.Outcome != "updated") || !uuidPattern.MatchString(result.Instance.ID) {
			return mutationCommand{}, problem(500, "transaction_reference_unresolved", "无法核对事务内结果引用")
		}
		command.Values[field], _ = json.Marshal(result.Instance.ID)
	}
	return command, nil
}
