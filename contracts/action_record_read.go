package contracts

import (
	"encoding/json"
	"errors"
	"slices"
)

const ActionRecordInputName = "当前记录"
const MaxActionRecordBytes = 32 << 10

type ActionRecordRead struct {
	SchemaID        string   `json:"schemaId"`
	InstanceInputID string   `json:"instanceInputId"`
	FieldIDs        []string `json:"fieldIds"`
}

type ActionRecordReadRequest struct {
	Session          SessionCheck `json:"session"`
	ActionID         string       `json:"actionId"`
	DefinitionDigest string       `json:"definitionDigest"`
	InstanceID       string       `json:"instanceId"`
	TenantID         string       `json:"tenantId"`
	SubjectID        string       `json:"subjectId"`
}

type ActionRecordSnapshot struct {
	ETag   string                     `json:"etag"`
	Values map[string]json.RawMessage `json:"values"`
}

func ValidateActionRecordRead(d Definition) error {
	if d.Function != nil && d.Function.RecordRead != nil {
		return errors.New("当前记录读取仅支持独立源码 Action")
	}
	a := d.Action
	if a == nil || a.RecordRead == nil {
		return nil
	}
	read := a.RecordRead
	if d.Kind != KindAction || a.Runtime == nil || a.FunctionID != "" || a.ArtifactRef != "" ||
		a.SideEffects != "none" || len(a.DataWrite) != 0 || read.SchemaID == "" ||
		!slices.Contains(a.TargetSchemaIDs, read.SchemaID) || len(read.FieldIDs) == 0 || len(read.FieldIDs) > 16 {
		return errors.New("读取声明需要无副作用的独立源码 Action、已关联模型及 1 至 16 个字段")
	}
	seen := map[string]bool{}
	for _, id := range read.FieldIDs {
		if id == "" || len(id) > 128 || seen[id] {
			return errors.New("读取字段 ID 为空、过长或重复")
		}
		seen[id] = true
	}
	validAnchor := false
	for _, p := range a.Input {
		if p.Name == ActionRecordInputName {
			return errors.New("输入名称“当前记录”保留给读取上下文")
		}
		if p.ID == read.InstanceInputID && p.ID != "" && p.DataType == "uuid" && p.Required && !p.Nullable && len(p.Default) == 0 {
			validAnchor = true
		}
	}
	if !validAnchor {
		return errors.New("记录 ID 须来自显式提供的必填 UUID 参数")
	}
	return nil
}
