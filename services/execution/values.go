package execution

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/wuhy80/AgentForge-Ontology/contracts"
)

const MaxPayloadBytes = 256 << 10

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func validateParameters(props []c.Property) error {
	if len(props) > 256 {
		return problem(422, "parameter_budget", "参数数量超过预算")
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, p := range props {
		if !validID(p.ID) || strings.TrimSpace(p.Name) == "" || ids[p.ID] || names[p.Name] {
			return problem(422, "invalid_parameters", "参数标识或名称不合法")
		}
		ids[p.ID], names[p.Name] = true, true
		if p.Mapping != nil || p.EnumID != "" && (p.DataType != "string" || p.EnumETag == "" || len(p.EnumValues) == 0) {
			return problem(422, "unsupported_parameters", "参数存储映射不支持，枚举须绑定有效当前string业务code")
		}
		switch p.DataType {
		case "string", "text", "longtext", "uuid", "int64", "boolean", "timestamp":
		case "decimal":
			if p.Precision < 1 || p.Precision > 65 || p.Scale < 0 || p.Scale > 30 || p.Scale > p.Precision {
				return problem(422, "invalid_parameters", "小数参数精度不合法")
			}
		default:
			return problem(422, "unsupported_parameters", "参数类型尚未支持")
		}
		if len(p.Default) > 0 {
			if err := validateValue(p, p.Default); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateValue(p c.Property, raw json.RawMessage) error {
	bad := func() error {
		return problem(422, "invalid_value", "参数类型、空值或范围不合法："+p.Name)
	}
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		if p.Nullable {
			return nil
		}
		return bad()
	}
	if p.DataType == "boolean" {
		if bytes.Equal(raw, []byte("true")) || bytes.Equal(raw, []byte("false")) {
			return nil
		}
		return bad()
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || !utf8.ValidString(value) {
		return bad()
	}
	if c.ValidateEnumValue(p, raw, false) != nil {
		return bad()
	}
	switch p.DataType {
	case "string":
		if utf8.RuneCountInString(value) > 255 {
			return bad()
		}
	case "text":
		if len(value) > c.MaxTextBytes {
			return bad()
		}
	case "longtext":
		if len(value) > c.MaxLongTextBytes {
			return bad()
		}
	case "uuid":
		if !uuidPattern.MatchString(value) {
			return bad()
		}
	case "timestamp":
		v, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || v.Nanosecond()%1000 != 0 || v.UTC().Year() < 1000 || v.UTC().Year() > 9999 {
			return bad()
		}
	case "int64":
		v, err := strconv.ParseInt(value, 10, 64)
		if err != nil || strconv.FormatInt(v, 10) != value {
			return bad()
		}
	case "decimal":
		if !decimalPattern.MatchString(value) {
			return bad()
		}
		parts := strings.Split(strings.TrimPrefix(value, "-"), ".")
		if len(strings.TrimLeft(parts[0], "0")) > p.Precision-p.Scale || (len(parts) == 2 && len(parts[1]) > p.Scale) {
			return bad()
		}
	default:
		return bad()
	}
	return nil
}

// HTTP uses stable IDs. Python uses the declared names from this invocation's
// exact definition snapshot, keeping raw internal IDs out of the code editor.
func buildInput(props []c.Property, input map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if err := validateParameters(props); err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, p := range props {
		known[p.ID] = true
	}
	for id := range input {
		if !known[id] {
			return nil, problem(422, "unknown_parameter", "输入包含未声明的参数")
		}
	}
	out := map[string]json.RawMessage{}
	for _, p := range props {
		raw, ok := input[p.ID]
		if !ok {
			switch {
			case len(p.Default) > 0:
				raw = p.Default
			case p.Required:
				return nil, problem(422, "missing_parameter", "缺少必填参数："+p.Name)
			default:
				continue
			}
		}
		if err := validateValue(p, raw); err != nil {
			return nil, err
		}
		out[p.Name] = bytes.Clone(raw)
	}
	if raw, _ := json.Marshal(out); len(raw) > MaxPayloadBytes {
		return nil, problem(422, "input_budget", "输入内容超过256KiB预算")
	}
	return out, nil
}

func buildOutput(props []c.Property, output map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	if err := validateParameters(props); err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, p := range props {
		known[p.Name] = true
	}
	for name := range output {
		if !known[name] {
			return nil, problem(422, "invalid_output", "函数返回了未声明的输出参数")
		}
	}
	out := map[string]json.RawMessage{}
	for _, p := range props {
		raw, ok := output[p.Name]
		if !ok {
			if p.Required {
				return nil, problem(422, "invalid_output", "函数未返回必填输出参数")
			}
			continue
		}
		if validateValue(p, raw) != nil {
			return nil, problem(422, "invalid_output", "函数输出与声明的类型或范围不符")
		}
		out[p.ID] = bytes.Clone(raw)
	}
	if raw, _ := json.Marshal(out); len(raw) > MaxPayloadBytes {
		return nil, problem(422, "output_budget", "输出内容超过256KiB预算")
	}
	return out, nil
}

func sameSignature(a, b []c.Property) bool {
	return c.CallableSignaturesEqual(&c.Callable{Input: a}, &c.Callable{Input: b})
}
