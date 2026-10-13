package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yorukot/nycu/internal/timetable"
)

// Every timetable call reads the public, unauthenticated NYCU timetable.
var timetableAnnotations = &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPtr(true)}

type noInput struct{}

type searchInput struct {
	Query  string `json:"query" jsonschema:"Search text: course name, teacher name, or course code, depending on by."`
	By     string `json:"by,omitempty" jsonschema:"Field to search. Default: name."`
	Acysem string `json:"acysem,omitempty" jsonschema:"Semester code like 1151. Default: latest semester."`
}

// cosID accepts a course ID written as a string or a number.
type cosID string

func (c *cosID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*c = cosID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("cos_id must be a string or a number")
	}
	*c = cosID(n.String())
	return nil
}

type lookupInput struct {
	CosIDs []cosID `json:"cos_ids"`
	Acysem string  `json:"acysem,omitempty"`
}

func (t *tools) registerTimetable(s *mcp.Server) {
	add(s, t, &mcp.Tool{
		Name:        "timetable_list_semesters",
		Description: "List NYCU timetable semester codes (e.g. 1151), newest first. Pass one as acysem to search/lookup, or omit it there to default to the latest semester.",
		Annotations: timetableAnnotations,
	}, "", func(ctx context.Context, _ noInput) (result, error) {
		semesters, err := t.timetable.Semesters(ctx)
		if err != nil {
			return result{}, err
		}
		return ok(semesters, map[string]any{"count": len(semesters)})
	})

	add(s, t, &mcp.Tool{
		Name:        "timetable_search_courses",
		Description: "Search NYCU course meeting times by course name, teacher, or course code. Returns day/period/room per course. Broad Chinese-name queries return many cross-department duplicates; narrow with by=code or an explicit acysem.",
		Annotations: timetableAnnotations,
		InputSchema: inputSchema[searchInput](func(schema *jsonschema.Schema) {
			schema.Properties["by"].Enum = []any{"name", "teacher", "code"}
		}),
	}, "narrow with by=code, or add acysem to scope one semester", func(ctx context.Context, in searchInput) (result, error) {
		courses, meta, err := t.timetable.Search(ctx, in.Query, timetable.SearchBy(in.By), in.Acysem)
		if err != nil {
			return result{}, err
		}
		return ok(courses, meta)
	})

	minItems, maxItems := 1, timetable.MaxLookupIDs
	add(s, t, &mcp.Tool{
		Name:        "timetable_lookup_courses",
		Description: "Look up class meeting time (day/period/room) for specific course IDs, max 20 per call. cos_id is the part after the dot in an E3 shortname like 1151.535702; chains directly off e3p_list_courses output. Unmatched IDs are reported in metadata.missing, not treated as an error.",
		Annotations: timetableAnnotations,
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"cos_ids": {
					Type:        "array",
					Items:       &jsonschema.Schema{Types: []string{"string", "integer"}},
					MinItems:    &minItems,
					MaxItems:    &maxItems,
					Description: "One or more course IDs (cos_id), e.g. 535702. Max 20 per call.",
				},
				"acysem": {Type: "string", Description: "Semester code like 1151. Default: latest semester."},
			},
			Required: []string{"cos_ids"},
		},
	}, "", func(ctx context.Context, in lookupInput) (result, error) {
		ids := make([]string, len(in.CosIDs))
		for i, id := range in.CosIDs {
			ids[i] = string(id)
		}
		courses, meta, err := t.timetable.Lookup(ctx, ids, in.Acysem)
		if err != nil {
			return result{}, err
		}
		return ok(courses, meta)
	})

	add(s, t, &mcp.Tool{
		Name:        "timetable_get_periods",
		Description: "Static reference table mapping NYCU period codes (1-9, y, z, n, a-d) to start/end times, plus day letters to weekday names. No network call.",
		Annotations: timetableAnnotations,
	}, "", func(context.Context, noInput) (result, error) {
		return ok(timetable.PeriodTable(), map[string]any{})
	})
}
