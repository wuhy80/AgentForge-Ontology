package contracts

import (
	"errors"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const MaxScriptSourceBytes = 64 * 1024

var scriptEntrypoint = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

var runtimeKeywords = map[string]string{
	"python":     "False None True and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield",
	"javascript": "await break case catch class const continue debugger default delete do else enum export extends false finally for function if implements import in instanceof interface let new null package private protected public return static super switch this throw true try typeof var void while with yield eval arguments",
	"go":         "break default func interface select case defer go map struct chan else goto package switch const fallthrough if range type continue for import return var main init _",
}

// ValidateScriptRuntime validates the executable subset, shared by publication
// and invocation. It deliberately never falls back to a different language.
func ValidateScriptRuntime(runtime *ScriptRuntime) error {
	if runtime == nil {
		return errors.New("尚未配置可执行源码")
	}
	keywords, supported := runtimeKeywords[runtime.Language]
	if !supported {
		return errors.New("运行器仅支持python、javascript和go")
	}
	if !utf8.ValidString(runtime.Source) || strings.TrimSpace(runtime.Source) == "" || len(runtime.Source) > MaxScriptSourceBytes || strings.ContainsRune(runtime.Source, 0) {
		return errors.New("源码必须为有效UTF-8文本，非空且不超过64KiB，不能含空字符")
	}
	if !scriptEntrypoint.MatchString(runtime.Entrypoint) || strings.HasPrefix(runtime.Entrypoint, "__") {
		return errors.New("入口必须为1至64字符的普通函数标识，且不能以双下划线开头")
	}
	for _, keyword := range strings.Fields(keywords) {
		if runtime.Entrypoint == keyword {
			return errors.New("入口不能使用所选语言的保留名称")
		}
	}
	if runtime.TimeoutSeconds < 1 || runtime.TimeoutSeconds > 30 {
		return errors.New("执行超时必须为1至30秒")
	}
	return nil
}

// CallableSignaturesEqual compares the name-to-value protocol accepted by the
// first Python runner. Each definition owns its own property IDs. Parameter
// order is immaterial; all declared value semantics must match.
func CallableSignaturesEqual(left, right *Callable) bool {
	if left == nil || right == nil {
		return false
	}
	canonical := func(properties []Property) []Property {
		result := append([]Property{}, properties...)
		for i := range result {
			result[i].ID = ""
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
		return result
	}
	return reflect.DeepEqual(canonical(left.Input), canonical(right.Input)) && reflect.DeepEqual(canonical(left.Output), canonical(right.Output))
}
