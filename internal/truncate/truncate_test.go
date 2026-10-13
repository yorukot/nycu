package truncate

import (
	"reflect"
	"strings"
	"testing"
)

var testLimits = Limits{MaxStringChars: 100, MaxTotalChars: 1000}

const testHint = "pass courseid"

func TestUnderCapIsUntouched(t *testing.T) {
	value := map[string]any{"data": map[string]any{"text": "short"}, "metadata": map[string]any{"count": 1}}
	got, report := Apply(value, testHint, testLimits)
	if report != nil {
		t.Fatalf("unexpected report %+v", report)
	}
	if !reflect.DeepEqual(got, value) {
		t.Fatalf("value changed: %v", got)
	}
}

func TestLongStringNamesLossAndHint(t *testing.T) {
	value := map[string]any{"data": map[string]any{"text": strings.Repeat("x", 500)}}
	got, report := Apply(value, testHint, testLimits)
	text := got.(map[string]any)["data"].(map[string]any)["text"].(string)
	if !strings.HasPrefix(text, strings.Repeat("x", 100)) {
		t.Fatal("kept prefix missing")
	}
	for _, want := range []string{"truncated 400 of 500 chars", testHint} {
		if !strings.Contains(text, want) {
			t.Fatalf("marker %q missing from %q", want, text[100:])
		}
	}
	if !reflect.DeepEqual(report.Fields, []string{"data.text"}) || report.Limit != 100 {
		t.Fatalf("report = %+v", report)
	}
}

func TestStructureTypesAndArrayLengthSurvive(t *testing.T) {
	value := map[string]any{
		"data": map[string]any{
			"rows": []any{
				map[string]any{"id": 1.0, "body": strings.Repeat("y", 300)},
				map[string]any{"id": 2.0, "body": "short"},
				map[string]any{"id": 3.0, "body": strings.Repeat("z", 300)},
			},
			"flag":    true,
			"nothing": nil,
		},
	}
	got, report := Apply(value, testHint, testLimits)
	data := got.(map[string]any)["data"].(map[string]any)
	rows := data["rows"].([]any)
	if len(rows) != 3 || rows[1].(map[string]any)["body"] != "short" || rows[2].(map[string]any)["id"] != 3.0 {
		t.Fatalf("rows changed: %v", rows)
	}
	if data["flag"] != true || data["nothing"] != nil {
		t.Fatalf("scalars changed: %v", data)
	}
	if !reflect.DeepEqual(report.Fields, []string{"data.rows[0].body", "data.rows[2].body"}) {
		t.Fatalf("fields = %v", report.Fields)
	}
}

func TestRecordsOriginalSize(t *testing.T) {
	value := map[string]any{"data": strings.Repeat("w", 400)}
	_, report := Apply(value, testHint, testLimits)
	if report.OriginalChars != SerializedLength(value) || report.OriginalChars != len(`{"data":""}`)+400 {
		t.Fatalf("original_chars = %d", report.OriginalChars)
	}
}

func TestRefusesToShredBelowFloor(t *testing.T) {
	// 40 leaves against a 1000-char total gives 25 chars each, below the 200 floor.
	leaves := make([]any, 40)
	for i := range leaves {
		leaves[i] = strings.Repeat("q", 100)
	}
	got, report := Apply(map[string]any{"data": leaves}, testHint, testLimits)
	if report != nil {
		t.Fatalf("unexpected report %+v", report)
	}
	if got.(map[string]any)["data"].([]any)[0] != strings.Repeat("q", 100) {
		t.Fatal("leaf was shortened")
	}
}

func TestSecondPassWhenFloorAllows(t *testing.T) {
	leaves := make([]any, 10)
	for i := range leaves {
		leaves[i] = strings.Repeat("q", 5000)
	}
	got, report := Apply(map[string]any{"data": leaves}, testHint, Limits{MaxStringChars: 4000, MaxTotalChars: 10_000})
	if report.Limit != 1000 || len(report.Fields) != 10 {
		t.Fatalf("report = %+v", report)
	}
	if !strings.HasPrefix(got.(map[string]any)["data"].([]any)[0].(string), strings.Repeat("q", 1000)) {
		t.Fatal("kept prefix missing")
	}
}

func TestCountsCharactersNotBytes(t *testing.T) {
	value := map[string]any{"data": strings.Repeat("課", 150)}
	got, report := Apply(value, "", testLimits)
	text := got.(map[string]any)["data"].(string)
	if !strings.HasPrefix(text, strings.Repeat("課", 100)+"\n…[truncated 50 of 150 chars]") {
		t.Fatalf("text = %q", text)
	}
	if report.OriginalChars != len(`{"data":""}`)+150 {
		t.Fatalf("original_chars = %d", report.OriginalChars)
	}
}
