package ontology

import "testing"

func validDraft() Draft {
	binding := &DataBinding{
		Source:     "source",
		Resource:   "pumps",
		PrimaryKey: []string{"id"},
		Fields: map[string]FieldBinding{
			"id":     {Field: "pump_id"},
			"status": {Field: "state"},
		},
	}
	pump := ObjectType{
		Name:        "Pump",
		StorageMode: StorageVirtual,
		Properties: []Property{
			{Name: "id", Type: TypeString, PrimaryKey: true},
			{Name: "status", Type: TypeEnum, Enum: "PumpStatus"},
		},
		Binding: binding,
	}
	vendor := Vendor{
		ID:      "acme",
		Enums:   []EnumType{{Name: "PumpStatus", Values: []EnumValue{{Value: "running", Name: "running"}}}},
		Domains: []Domain{{ID: "equipment", Objects: []ObjectType{pump}}},
	}
	return Draft{
		Workspace:   Workspace{ID: "yard"},
		DataSources: []DataSource{{ID: "source", Driver: "memory"}},
		Vendors:     []Vendor{vendor},
	}
}

func TestValidateAcceptsRuntimeDraft(t *testing.T) {
	if errs := Validate(validDraft()); len(errs) > 0 {
		t.Fatalf("unexpected validation errors: %#v", errs)
	}
}

func TestValidateRejectsBrokenBindingAndEnum(t *testing.T) {
	draft := validDraft()
	obj := &draft.Vendors[0].Domains[0].Objects[0]
	obj.Properties[1].Enum = "Missing"
	obj.Binding.PrimaryKey = []string{"missing"}
	errs := Validate(draft)
	if len(errs) != 2 {
		t.Fatalf("got %d errors, want 2: %#v", len(errs), errs)
	}
}
