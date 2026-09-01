package kubernetes

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// lookupPath resolves a dotted field path with numeric list indices —
// spec.template.spec.containers[0].image — against a decoded object.
//
// It returns found = false for a path that does not exist, which is a fail
// (the document describes a field the object does not have), as distinct from
// an error, which is a path that cannot be interpreted at all.
func lookupPath(object map[string]any, path string) (value any, found bool, err error) {
	segments, err := parsePath(path)
	if err != nil {
		return nil, false, err
	}
	var current any = object
	for i, segment := range segments {
		switch step := segment.(type) {
		case string:
			node, ok := current.(map[string]any)
			if !ok {
				return nil, false, nil
			}
			current, ok = node[step]
			if !ok {
				return nil, false, nil
			}
		case int:
			list, ok := current.([]any)
			if !ok {
				return nil, false, nil
			}
			if step < 0 || step >= len(list) {
				return nil, false, nil
			}
			current = list[step]
		default:
			return nil, false, fmt.Errorf("path %q: unusable segment %d", path, i)
		}
	}
	return current, true, nil
}

// parsePath splits a path into string keys and integer indices.
func parsePath(path string) ([]any, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("path is empty")
	}
	var segments []any
	for _, part := range strings.Split(path, ".") {
		name, rest, hasIndex := strings.Cut(part, "[")
		if name == "" && !hasIndex {
			return nil, fmt.Errorf("path %q has an empty segment", path)
		}
		if name != "" {
			segments = append(segments, name)
		}
		for hasIndex {
			var index string
			index, rest, _ = strings.Cut(rest, "]")
			n, err := strconv.Atoi(index)
			if err != nil {
				return nil, fmt.Errorf("path %q: %q is not a list index", path, index)
			}
			segments = append(segments, n)
			_, rest, hasIndex = strings.Cut(rest, "[")
		}
	}
	return segments, nil
}

// equalValues compares a field's value with the value a runbook asserts.
//
// JSON types are significant: the string "3" does not equal the number 3, since
// a replica count and an image tag are different kinds of claim. Numbers are
// compared numerically, because the API server returns integers as int64 while
// a decoded JSON document holds float64, and 3 is 3.
func equalValues(a, b any) bool {
	return reflect.DeepEqual(normaliseNumbers(a), normaliseNumbers(b))
}

func normaliseNumbers(value any) any {
	switch v := value.(type) {
	case int:
		return float64(v)
	case int32:
		return float64(v)
	case int64:
		return float64(v)
	case float32:
		return float64(v)
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return v.String()
		}
		return f
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = normaliseNumbers(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = normaliseNumbers(item)
		}
		return out
	default:
		return value
	}
}

// asNumber reports a value as a float when it is numeric, which is what
// field_gte needs and what it refuses to guess at when the field is a string.
func asNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case float32:
		return float64(v), true
	case float64:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// describe renders a field value for a one-line summary. Strings are shown
// bare; anything else through JSON, truncated, because a result is a summary
// and not a manifest dump.
func describe(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	const limit = 120
	if len(encoded) > limit {
		return string(encoded[:limit]) + "…"
	}
	return string(encoded)
}

// formatNumber renders a float without a trailing .0, so that a replica count
// reads as 3 rather than 3.000000.
func formatNumber(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
