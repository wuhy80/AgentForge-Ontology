package identity

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
)

func TestPasswordsAndTypedPolicyValues(t *testing.T) {
	for _, password := range []string{"short", "123456", "123456789012345", "passwordpassword", strings.Repeat("a", 20), string(make([]byte, 73))} {
		if validPassword(password) == nil {
			t.Fatal("unsupported password accepted")
		}
	}
	for _, password := range []string{"Correct horse battery staple", "春日山川与星河共明照长夜安眠好梦"} {
		if err := validPassword(password); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		field contracts.Property
		value string
		valid bool
	}{
		{contracts.Property{DataType: "int64"}, `"9223372036854775807"`, true},
		{contracts.Property{DataType: "int64"}, `9223372036854775807`, false},
		{contracts.Property{DataType: "decimal", Precision: 18, Scale: 2}, `"123.45"`, true},
		{contracts.Property{DataType: "decimal", Precision: 18, Scale: 2}, `"123.456"`, false},
		{contracts.Property{DataType: "boolean"}, `false`, true},
		{contracts.Property{DataType: "boolean"}, `"false"`, false},
		{contracts.Property{DataType: "timestamp"}, `"2026-11-07T12:00:00.000001Z"`, true},
		{contracts.Property{DataType: "timestamp"}, `"2026-11-07T12:00:00.0000001Z"`, false},
	} {
		if err := validateValue(tc.field, json.RawMessage(tc.value)); (err == nil) != tc.valid {
			t.Fatalf("type %s expected validity %v", tc.field.DataType, tc.valid)
		}
	}
}

func TestLoginBudgetHasBoundedMemoryAndTypedErrors(t *testing.T) {
	store := NewStore(nil, nil, "http://localhost:5173")
	for attempt := 0; attempt < 5; attempt++ {
		if err := store.loginBudget("subject"); err != nil {
			t.Fatal(err)
		}
	}
	var access *platform.AccessError
	if !errors.As(store.loginBudget("subject"), &access) || access.Status != 429 {
		t.Fatal("login limit lacks public error status")
	}
	for id := 0; id < 1024; id++ {
		_ = store.loginBudget(string(rune(id)) + "other")
	}
	if store.loginBudget("unrelated-user") != nil {
		t.Fatal("invented names blocked an unrelated user")
	}
	store.resetLoginBudget("subject")
	if err := store.loginBudget("subject"); err != nil {
		t.Fatal(err)
	}
}

func TestLongTextPolicyConditionsPreserveTypeAndByteBudget(t *testing.T) {
	field := contracts.Property{ID: "body", Name: "正文", DataType: "longtext"}
	value := strings.Repeat("知", contracts.MaxLongTextBytes/3) + "a"
	encoded, _ := json.Marshal(value)
	if err := validateValue(field, encoded); err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(value + "a")
	if validateValue(field, encoded) == nil {
		t.Fatal("oversized policy value accepted")
	}
	expression := &contracts.Expression{Op: "startsWith", Field: field.ID, Value: json.RawMessage(`"正文\n"`)}
	if err := validateCondition(expression, map[string]contracts.Property{field.ID: field}); err != nil {
		t.Fatal(err)
	}
	field.DataType = "text"
	encoded, _ = json.Marshal(strings.Repeat("x", contracts.MaxTextBytes+1))
	if validateValue(field, encoded) == nil {
		t.Fatal("existing text policy budget was widened")
	}
}
