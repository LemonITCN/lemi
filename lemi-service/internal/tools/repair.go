package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 参数修复的最大深度。
const repairDepth = 6

var trailingComma = regexp.MustCompile(`,\s*([}\]])`)

// parseLoose 尽量把模型给的参数解析成 JSON 值：去掉代码围栏、结尾多余的逗号；整体被包成字符串时再解一层。
func parseLoose(raw []byte) (any, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return map[string]any{}, nil
	}
	try := func(t string) (any, error) {
		dec := json.NewDecoder(strings.NewReader(t))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if dec.More() {
			return nil, fmt.Errorf("JSON 后面还有多余内容")
		}
		return v, nil
	}
	v, err := try(s)
	if err != nil {
		t := strings.TrimSpace(s)
		t = strings.TrimPrefix(t, "```json")
		t = strings.TrimPrefix(t, "```")
		t = strings.TrimSuffix(t, "```")
		t = trailingComma.ReplaceAllString(strings.TrimSpace(t), "$1")
		if v2, err2 := try(t); err2 == nil {
			v, err = v2, nil
		}
	}
	if err != nil {
		return nil, err
	}
	if str, ok := v.(string); ok {
		if inner, err := try(str); err == nil {
			return inner, nil
		}
	}
	return v, nil
}

// Repair 按 JSON Schema 修复参数：字符串化的对象/数组解析、"20"→20、"false"→false、可选字段的 null 删除、
// 封闭对象的多余字段删除、单个值包成数组、枚举大小写修正（深度 ≤ 6）。修不好的列为问题返回。
func Repair(schema map[string]any, raw json.RawMessage) (json.RawMessage, []string) {
	v, err := parseLoose(raw)
	if err != nil {
		return nil, []string{"参数不是合法的 JSON（" + err.Error() + "）"}
	}
	var problems []string
	fixed := coerce(v, schema, "参数", 0, &problems)
	if len(problems) > 0 {
		return nil, problems
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(fixed); err != nil {
		return nil, []string{err.Error()}
	}
	return json.RawMessage(bytes.TrimSpace(buf.Bytes())), nil
}

func schemaTypes(s map[string]any) []string {
	switch t := s["type"].(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, x := range t {
			if str, ok := x.(string); ok {
				out = append(out, str)
			}
		}
		return out
	case []string:
		return t
	}
	return nil
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func asMap(v any) map[string]any {
	switch m := v.(type) {
	case map[string]any:
		return m
	}
	return nil
}

func describe(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "字符串"
	case json.Number, float64, int:
		return "数字"
	case bool:
		return "布尔值"
	case []any:
		return "数组"
	case map[string]any:
		return "对象"
	}
	return fmt.Sprintf("%T", v)
}

func coerce(v any, s map[string]any, path string, depth int, problems *[]string) any {
	if s == nil || depth > repairDepth {
		return v
	}
	types := schemaTypes(s)
	if len(types) == 0 {
		return v
	}
	if v == nil {
		if has(types, "null") {
			return nil
		}
		*problems = append(*problems, fmt.Sprintf("%s 不能为 null（应为 %s）", path, strings.Join(types, "/")))
		return nil
	}
	// 字符串化的对象或数组
	if str, ok := v.(string); ok && (has(types, "object") || has(types, "array")) && !has(types, "string") {
		t := strings.TrimSpace(str)
		if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			if p, err := parseLoose([]byte(t)); err == nil {
				v = p
			}
		}
	}
	switch {
	case has(types, "object") && asMap(v) != nil:
		m := asMap(v)
		props, _ := s["properties"].(map[string]any)
		closed := false
		if ap, ok := s["additionalProperties"].(bool); ok && !ap {
			closed = true
		}
		req := map[string]bool{}
		switch r := s["required"].(type) {
		case []string:
			for _, k := range r {
				req[k] = true
			}
		case []any:
			for _, k := range r {
				if ks, ok := k.(string); ok {
					req[ks] = true
				}
			}
		}
		out := map[string]any{}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			val := m[k]
			ps, known := props[k].(map[string]any)
			if !known {
				// 大小写不同的字段名
				for pk := range props {
					if strings.EqualFold(pk, k) {
						if _, dup := m[pk]; !dup {
							ps, known, k = props[pk].(map[string]any), true, pk
						}
						break
					}
				}
			}
			if !known {
				if closed {
					continue // 多余字段删除
				}
				out[k] = val
				continue
			}
			if val == nil && !req[k] && !has(schemaTypes(ps), "null") {
				continue // 可选字段的 null 删除
			}
			out[k] = coerce(val, ps, path+"."+k, depth+1, problems)
		}
		var missing []string
		for k := range req {
			if _, ok := out[k]; !ok {
				missing = append(missing, k)
			}
		}
		sort.Strings(missing)
		for _, k := range missing {
			*problems = append(*problems, fmt.Sprintf("缺少必填字段 %s.%s", path, k))
		}
		return out
	case has(types, "array"):
		items, _ := s["items"].(map[string]any)
		list, ok := v.([]any)
		if !ok {
			// 单个值包成数组
			if items != nil && len(schemaTypes(items)) > 0 && matchesScalar(v, schemaTypes(items)) {
				list = []any{v}
			} else {
				*problems = append(*problems, fmt.Sprintf("%s 应为数组，实际是%s", path, describe(v)))
				return v
			}
		}
		out := make([]any, len(list))
		for i, x := range list {
			out[i] = coerce(x, items, fmt.Sprintf("%s[%d]", path, i), depth+1, problems)
		}
		return out
	case has(types, "integer") || has(types, "number"):
		f, ok := toFloat(v)
		if !ok {
			if has(types, "string") {
				return v
			}
			*problems = append(*problems, fmt.Sprintf("%s 应为数字，实际是%s", path, describe(v)))
			return v
		}
		if has(types, "integer") && !has(types, "number") {
			if f != math.Trunc(f) {
				*problems = append(*problems, fmt.Sprintf("%s 应为整数，实际是 %v", path, f))
				return v
			}
			return int64(f)
		}
		return f
	case has(types, "boolean"):
		switch x := v.(type) {
		case bool:
			return x
		case string:
			switch strings.ToLower(strings.TrimSpace(x)) {
			case "true", "1", "yes":
				return true
			case "false", "0", "no", "":
				return false
			}
		case json.Number:
			if x.String() == "1" {
				return true
			}
			if x.String() == "0" {
				return false
			}
		}
		*problems = append(*problems, fmt.Sprintf("%s 应为 true 或 false，实际是%s", path, describe(v)))
		return v
	case has(types, "string"):
		var str string
		switch x := v.(type) {
		case string:
			str = x
		case json.Number:
			str = x.String()
		case bool:
			str = strconv.FormatBool(x)
		default:
			*problems = append(*problems, fmt.Sprintf("%s 应为字符串，实际是%s", path, describe(v)))
			return v
		}
		if en, ok := s["enum"].([]string); ok && len(en) > 0 {
			for _, e := range en {
				if e == str {
					return str
				}
			}
			for _, e := range en {
				if strings.EqualFold(e, strings.TrimSpace(str)) {
					return e
				}
			}
			*problems = append(*problems, fmt.Sprintf("%s 取值 %q 不在允许范围内（%s）", path, str, strings.Join(en, " / ")))
			return str
		}
		return str
	case has(types, "object"):
		*problems = append(*problems, fmt.Sprintf("%s 应为对象，实际是%s", path, describe(v)))
		return v
	}
	return v
}

func matchesScalar(v any, types []string) bool {
	switch v.(type) {
	case string:
		return has(types, "string") || has(types, "integer") || has(types, "number")
	case json.Number:
		return has(types, "integer") || has(types, "number") || has(types, "string")
	case bool:
		return has(types, "boolean")
	case map[string]any:
		return has(types, "object")
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}
