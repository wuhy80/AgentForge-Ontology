package metadata

import (
	"regexp"
	"unicode/utf8"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
)

var lifecycleID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// canonicalLifecycle normalizes a Schema lifecycle declaration and rejects
// declarations Data could not honor. Stage and transition IDs are assigned once
// and then stay stable so a bound transition never follows a renamed display
// text. External mapped schemas keep their stages in the source system, so they
// cannot declare a platform lifecycle in this slice.
func canonicalLifecycle(s *contracts.Schema) error {
	l := s.Lifecycle
	if l == nil {
		return nil
	}
	if s.StorageBinding != nil {
		return problem(422, "unsupported_capability", "外部映射模型暂不支持记录生命周期")
	}
	if len(l.Stages) == 0 || len(l.Stages) > contracts.MaxLifecycleStages {
		return problem(400, "invalid_lifecycle", "生命周期阶段数必须为1到20")
	}
	stageIDs := map[string]bool{}
	stageNames := map[string]bool{}
	for i := range l.Stages {
		stage := &l.Stages[i]
		if !lifecycleID.MatchString(stage.ID) || stageIDs[stage.ID] {
			return problem(400, "invalid_lifecycle", "阶段标识为空、格式不合法或重复")
		}
		stageIDs[stage.ID] = true
		name, key, err := lifecycleName(stage.Name)
		if err != nil {
			return err
		}
		if stageNames[key] {
			return problem(400, "invalid_lifecycle", "阶段名称重复")
		}
		stage.Name, stageNames[key] = name, true
	}
	if !stageIDs[l.InitialStageID] {
		return problem(400, "invalid_lifecycle", "初始阶段必须是已声明的阶段")
	}
	if len(l.Transitions) > contracts.MaxLifecycleTransitions {
		return problem(400, "invalid_lifecycle", "流转数不能超过40")
	}
	transitionIDs := map[string]bool{}
	transitionNames := map[string]bool{}
	for i := range l.Transitions {
		transition := &l.Transitions[i]
		if !lifecycleID.MatchString(transition.ID) || transitionIDs[transition.ID] {
			return problem(400, "invalid_lifecycle", "流转标识为空、格式不合法或重复")
		}
		transitionIDs[transition.ID] = true
		name, key, err := lifecycleName(transition.Name)
		if err != nil {
			return err
		}
		if transitionNames[key] {
			return problem(400, "invalid_lifecycle", "流转名称重复")
		}
		transition.Name, transitionNames[key] = name, true
		if !stageIDs[transition.From] || !stageIDs[transition.To] || transition.From == transition.To {
			return problem(400, "invalid_lifecycle", "流转必须连接两个不同的已声明阶段")
		}
	}
	return nil
}

func lifecycleName(name string) (string, string, error) {
	normalized, key, err := normalizeName(name)
	if err != nil {
		return "", "", err
	}
	if utf8.RuneCountInString(normalized) > contracts.MaxLifecycleNameRunes {
		return "", "", problem(400, "invalid_lifecycle", "阶段和流转名称不能超过64个字符")
	}
	return normalized, key, nil
}
