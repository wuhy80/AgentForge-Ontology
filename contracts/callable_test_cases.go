package contracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxCallableTestCases = 10
const MaxCallableTestCaseBytes = 64 << 10

// Samples belong to the definition and use its stable input IDs. Missing
// required inputs may be filled in before running; supplied values must already
// obey the scalar wire protocol, including lossless int64/decimal strings.
func ValidateCallableTestCases(callable *Callable) error {
	if callable == nil || len(callable.TestCases) == 0 {
		return nil
	}
	if len(callable.TestCases) > MaxCallableTestCases {
		return errors.New("每个定义最多保存10个测试样例")
	}
	raw, err := json.Marshal(callable.TestCases)
	if err != nil || len(raw) > MaxCallableTestCaseBytes {
		return errors.New("测试样例总JSON不能超过64KiB")
	}
	inputs := map[string]Property{}
	for _, property := range callable.Input {
		inputs[property.ID] = property
	}
	ids, names := map[string]bool{}, map[string]bool{}
	validText := func(s string, limit int) bool {
		return s != "" && utf8.ValidString(s) && utf8.RuneCountInString(s) <= limit && strings.TrimSpace(s) == s && strings.IndexFunc(s, unicode.IsControl) < 0
	}
	for _, sample := range callable.TestCases {
		if !validText(sample.ID, 128) || strings.ContainsAny(sample.ID, "/\\?#") || sample.ID == "*" || ids[sample.ID] || !validText(sample.Name, 80) || names[strings.ToLower(sample.Name)] || sample.Input == nil {
			return errors.New("测试样例须有有效且不重复的标识、名称与输入对象；名称最多80字符")
		}
		ids[sample.ID], names[strings.ToLower(sample.Name)] = true, true
		for id, value := range sample.Input {
			property, ok := inputs[id]
			if !ok {
				return errors.New("测试样例只能使用当前声明的输入参数稳定ID")
			}
			if !validCallableSampleValue(property, value) {
				return errors.New("测试样例输入的类型、空值或范围不符合参数声明")
			}
		}
	}
	return nil
}

var sampleUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var sampleDecimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func validCallableSampleValue(property Property, raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return property.Nullable
	}
	if property.DataType == "boolean" {
		return bytes.Equal(raw, []byte("true")) || bytes.Equal(raw, []byte("false"))
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || !utf8.ValidString(value) {
		return false
	}
	switch property.DataType {
	case "string":
		return utf8.RuneCountInString(value) <= 255
	case "text":
		return len(value) <= MaxTextBytes
	case "longtext":
		return len(value) <= MaxLongTextBytes
	case "uuid":
		return sampleUUIDPattern.MatchString(value)
	case "timestamp":
		parsed, err := time.Parse(time.RFC3339Nano, value)
		return err == nil && parsed.Nanosecond()%1000 == 0 && parsed.UTC().Year() >= 1000 && parsed.UTC().Year() <= 9999
	case "int64":
		parsed, err := strconv.ParseInt(value, 10, 64)
		return err == nil && strconv.FormatInt(parsed, 10) == value
	case "decimal":
		if !sampleDecimalPattern.MatchString(value) || property.Precision < 1 || property.Scale < 0 || property.Scale > property.Precision {
			return false
		}
		parts := strings.Split(strings.TrimPrefix(value, "-"), ".")
		return len(strings.TrimLeft(parts[0], "0")) <= property.Precision-property.Scale && (len(parts) == 1 || len(parts[1]) <= property.Scale)
	}
	return false
}
