package contracts

import "encoding/json"

// PermissionValueRef is a typed policy operand, resolved only by the authority.
// Object lookups explicitly grant a minimal attribute read for authorization;
// they never grant discovery, result projection, or ordinary instance access.
type PermissionValueRef struct {
	Source             string `json:"source"`
	Attribute          string `json:"attribute,omitempty"`
	SchemaID           string `json:"schemaId,omitempty"`
	FieldID            string `json:"fieldId,omitempty"`
	InstanceID         string `json:"instanceId,omitempty"`
	MatchFieldID       string `json:"matchFieldId,omitempty"`
	MatchUserAttribute string `json:"matchUserAttribute,omitempty"`
	Mode               string `json:"mode,omitempty"`
}

// These messages are service-only. Tenant identity is carried by the verified
// internal transport, never by an untrusted policy request or browser context.
type ObjectContextRequest struct {
	SchemaID     string          `json:"schemaId"`
	FieldID      string          `json:"fieldId"`
	InstanceID   string          `json:"instanceId,omitempty"`
	MatchFieldID string          `json:"matchFieldId,omitempty"`
	MatchValue   json.RawMessage `json:"matchValue,omitempty"`
	Mode         string          `json:"mode,omitempty"`
}

type ObjectContextResult struct {
	Value  json.RawMessage `json:"value"`
	Digest string          `json:"digest"`
}
