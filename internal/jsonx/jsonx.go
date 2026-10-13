// Package jsonx reads loosely typed upstream JSON.
//
// Moodle and the timetable API are inconsistent about types (a field can be a
// number in one release and a string in the next, or be absent), so responses
// are decoded generically and read through these nil-safe accessors instead of
// strict structs. Numbers stay json.Number, so values passed through to tool
// output keep their exact upstream form.
package jsonx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// Decode parses JSON, keeping numbers as json.Number.
func Decode(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after JSON value")
	}
	return v, nil
}

// Obj returns v as an object, or nil when it is not one.
func Obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// Arr returns v as an array, or nil when it is not one.
func Arr(v any) []any {
	a, _ := v.([]any)
	return a
}

// Get returns m[key], or nil when m is nil or the key is absent.
func Get(m map[string]any, key string) any {
	if m == nil {
		return nil
	}
	return m[key]
}

// Str returns v when it is a string, the literal text when it is a number, and
// "" otherwise.
func Str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	default:
		return ""
	}
}

// Truthy mirrors Python truthiness, which the original tools relied on for
// "value or None" defaults: nil, false, 0, "" and empty containers are false.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	default:
		return true
	}
}

// OrNil returns v when it is truthy, else nil.
func OrNil(v any) any {
	if Truthy(v) {
		return v
	}
	return nil
}

// First returns the first truthy value, or the last one when none is truthy.
func First(values ...any) any {
	for _, v := range values {
		if Truthy(v) {
			return v
		}
	}
	if len(values) == 0 {
		return nil
	}
	return values[len(values)-1]
}

// Num returns v as a float64 for sorting and arithmetic. Anything that is not
// a number, or a numeric string, reads as 0.
func Num(v any) float64 {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	default:
		return 0
	}
}

// Int returns v as an int64 when it holds an integral number.
func Int(v any) (int64, bool) {
	switch x := v.(type) {
	case json.Number:
		i, err := x.Int64()
		return i, err == nil
	case float64:
		return int64(x), x == float64(int64(x))
	case int:
		return int64(x), true
	case int64:
		return x, true
	default:
		return 0, false
	}
}

// Member is one key/value pair of an object, in document order.
type Member struct {
	Key   string
	Value json.RawMessage
}

// Members decodes a JSON object while keeping its key order, which Go maps lose.
func Members(data []byte) ([]Member, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("expected a JSON object")
	}
	var members []Member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("expected an object key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		members = append(members, Member{Key: key, Value: raw})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return members, nil
}
