package metadata

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

const (
	lifecycleDraft  = "1a1a1111-1111-4111-8111-111111111111"
	lifecycleReview = "2b2b2222-2222-4222-8222-222222222222"
	lifecycleDone   = "3c3c3333-3333-4333-8333-333333333333"
	lifecycleSubmit = "4d4d4444-4444-4444-8444-444444444444"
	lifecycleFinish = "5e5e5555-5555-4555-8555-555555555555"
)

func lifecycleSchema() contracts.Definition {
	d := validSchema()
	d.Schema.Lifecycle = &contracts.SchemaLifecycle{InitialStageID: lifecycleDraft,
		Stages: []contracts.LifecycleStage{{ID: lifecycleDraft, Name: "草稿"}, {ID: lifecycleReview, Name: "审核中"}, {ID: lifecycleDone, Name: "已完成"}},
		Transitions: []contracts.LifecycleTransition{
			{ID: lifecycleSubmit, Name: "提交审核", From: lifecycleDraft, To: lifecycleReview},
			{ID: lifecycleFinish, Name: "归档", From: lifecycleReview, To: lifecycleDone},
		}}
	return d
}

func TestLifecycleKeepsStableIDsAndNormalizesNames(t *testing.T) {
	d := lifecycleSchema()
	d.Schema.Lifecycle.Stages[1].Name = " 审核中 "
	d.Schema.Lifecycle.Transitions[0].Name = "提交 审核"
	if err := canonicalDefinition(&d, false); err != nil {
		t.Fatalf("lifecycle rejected: %v", err)
	}
	l := d.Schema.Lifecycle
	if l.Stages[1].Name != "审核中" || l.Transitions[0].Name != "提交 审核" {
		t.Fatalf("lifecycle not normalized: %+v", l)
	}
	if l.Stages[0].ID != lifecycleDraft || l.Transitions[1].ID != lifecycleFinish {
		t.Fatal("canonicalization rewrote stable IDs")
	}
}

func TestLifecycleRejectsUnsupportedDeclarations(t *testing.T) {
	cases := map[string]func(*contracts.SchemaLifecycle){
		"no stages": func(l *contracts.SchemaLifecycle) { l.Stages = nil },
		"too many stages": func(l *contracts.SchemaLifecycle) {
			for i := 0; i <= contracts.MaxLifecycleStages; i++ {
				l.Stages = append(l.Stages, contracts.LifecycleStage{ID: contracts.NewID(), Name: "补充阶段" + strings.Repeat("甲", i)})
			}
		},
		"unknown initial": func(l *contracts.SchemaLifecycle) { l.InitialStageID = lifecycleFinish },
		"duplicate name":  func(l *contracts.SchemaLifecycle) { l.Stages[1].Name = " 草稿 " },
		"empty stage id":  func(l *contracts.SchemaLifecycle) { l.Stages[1].ID = "" },
		"bad stage id":    func(l *contracts.SchemaLifecycle) { l.Stages[1].ID = "stage-2" },
		"long stage name": func(l *contracts.SchemaLifecycle) {
			l.Stages[1].Name = strings.Repeat("状", contracts.MaxLifecycleNameRunes+1)
		},
		"unknown target":  func(l *contracts.SchemaLifecycle) { l.Transitions[1].To = contracts.NewID() },
		"self transition": func(l *contracts.SchemaLifecycle) { l.Transitions[1].To = l.Transitions[1].From },
		"duplicate move":  func(l *contracts.SchemaLifecycle) { l.Transitions[1].Name = "提交审核" },
		"too many moves": func(l *contracts.SchemaLifecycle) {
			for i := 0; i <= contracts.MaxLifecycleTransitions; i++ {
				l.Transitions = append(l.Transitions, contracts.LifecycleTransition{ID: contracts.NewID(), Name: "流转" + strings.Repeat("甲", i), From: lifecycleReview, To: lifecycleDone})
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := lifecycleSchema()
			mutate(d.Schema.Lifecycle)
			if err := canonicalDefinition(&d, false); err == nil {
				t.Fatal("unsupported lifecycle accepted")
			}
		})
	}
}

func TestLifecycleIsRejectedOnExternalMappings(t *testing.T) {
	d := lifecycleSchema()
	d.Schema.Mode = "db_2_schema"
	d.Schema.StorageBinding = &contracts.StorageBinding{DataSourceID: lifecycleDraft, Table: "devices"}
	if err := canonicalDefinition(&d, false); err == nil || !strings.Contains(err.Error(), "生命周期") {
		t.Fatalf("mapped schema accepted a platform lifecycle: %v", err)
	}
}

func copyDefinition(d contracts.Definition) contracts.Definition {
	raw, _ := json.Marshal(d)
	var out contracts.Definition
	_ = json.Unmarshal(raw, &out)
	return out
}

func TestLifecycleEditsNeedNoMigrationWhileStructureStillDoes(t *testing.T) {
	old := lifecycleSchema()
	edited := copyDefinition(old)
	edited.Description = "补充说明"
	edited.Schema.Lifecycle.Stages[2].Name = "已归档"
	edited.Schema.Lifecycle.Transitions = edited.Schema.Lifecycle.Transitions[:1]
	if err := compatibleUpdate(old, edited); err != nil {
		t.Fatalf("lifecycle edit demanded a migration: %v", err)
	}
	structural := copyDefinition(old)
	structural.Schema.Properties[0].DataType = "int64"
	if err := compatibleUpdate(old, structural); err == nil {
		t.Fatal("field type change bypassed migration")
	}
}
