// Package truncate keeps one tool result from swallowing the caller's context.
//
// Only string leaves are shortened. Structure, types and array lengths survive
// untouched, so truncation never changes the result's shape and a caller is
// never silently handed a shorter list than the tool produced.
package truncate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"unicode/utf8"
)

const (
	DefaultMaxStringChars = 20_000
	DefaultMaxTotalChars  = 120_000
	minStringCap          = 200
)

// Limits caps a single string leaf and the whole serialized value, in characters.
type Limits struct {
	MaxStringChars int
	MaxTotalChars  int
}

// DefaultLimits returns the limits used when nothing is configured.
func DefaultLimits() Limits {
	return Limits{MaxStringChars: DefaultMaxStringChars, MaxTotalChars: DefaultMaxTotalChars}
}

// Report describes what was shortened. It is attached to the result as _truncation.
type Report struct {
	Fields        []string `json:"fields"`
	OriginalChars int      `json:"original_chars"`
	Limit         int      `json:"limit"`
}

// Apply shortens string leaves of a decoded JSON value (maps, slices, strings,
// numbers, bools, nil).
//
// Pass 1 caps every string at MaxStringChars. If the serialized result is still
// over MaxTotalChars, pass 2 divides the total budget across the string leaves
// (floor 200 characters) and re-walks the original value once. If that cap
// would fall below the floor, the output stays over budget instead of being
// shredded into useless fragments.
func Apply(value any, hint string, limits Limits) (any, *Report) {
	original := SerializedLength(value)

	state := &walkState{cap: limits.MaxStringChars, hint: hint}
	result := walk(value, "", state)

	if SerializedLength(result) > limits.MaxTotalChars {
		leaves := countStringLeaves(value)
		cap := limits.MaxStringChars
		if leaves > 0 {
			cap = max(minStringCap, limits.MaxTotalChars/leaves)
		}
		if cap < limits.MaxStringChars {
			state = &walkState{cap: cap, hint: hint}
			result = walk(value, "", state)
		}
	}

	if len(state.fields) == 0 {
		return result, nil
	}
	return result, &Report{Fields: state.fields, OriginalChars: original, Limit: state.cap}
}

// SerializedLength is the character length of the value's compact JSON form.
func SerializedLength(value any) int {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return 0
	}
	return utf8.RuneCount(bytes.TrimRight(buf.Bytes(), "\n"))
}

type walkState struct {
	cap    int
	hint   string
	fields []string
}

func walk(value any, path string, state *walkState) any {
	switch v := value.(type) {
	case string:
		length := utf8.RuneCountInString(v)
		if length <= state.cap {
			return v
		}
		if path == "" {
			path = "<root>"
		}
		state.fields = append(state.fields, path)
		return string([]rune(v)[:state.cap]) + marker(state.cap, length, state.hint)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = walk(item, path+"["+strconv.Itoa(i)+"]", state)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for _, key := range sortedKeys(v) {
			child := key
			if path != "" {
				child = path + "." + key
			}
			out[key] = walk(v[key], child, state)
		}
		return out
	default:
		return value
	}
}

func marker(kept, original int, hint string) string {
	suffix := ""
	if hint != "" {
		suffix = " — " + hint
	}
	return fmt.Sprintf("\n…[truncated %d of %d chars%s]", original-kept, original, suffix)
}

func countStringLeaves(value any) int {
	switch v := value.(type) {
	case string:
		return 1
	case []any:
		n := 0
		for _, item := range v {
			n += countStringLeaves(item)
		}
		return n
	case map[string]any:
		n := 0
		for _, item := range v {
			n += countStringLeaves(item)
		}
		return n
	default:
		return 0
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
