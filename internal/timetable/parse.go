package timetable

import (
	"html"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/yorukot/nycu/internal/jsonx"
)

var (
	// One <day><periods> run, e.g. "M78" or "R34". Periods use only the real
	// period-code alphabet, so a stray letter (the B/A in "TBA") never matches
	// and gets mistaken for a day.
	dayRunRE         = regexp.MustCompile(`([MTWRFSU])([yz1-9nabcd]+)`)
	dayRunSequenceRE = regexp.MustCompile(`^(?:[MTWRFSU][yz1-9nabcd]+)+$`)
	roomRE           = regexp.MustCompile(`^([^\[]*)(?:\[([^\]]*)\])?$`)
	tagRE            = regexp.MustCompile(`<[^>]+>`)
)

// Department-level keys in the course-list payload; every other key is a group of courses.
var skipKeys = map[string]bool{"dep_id": true, "dep_cname": true, "dep_ename": true, "costype": true, "brief": true, "language": true}

// Slot is one weekly meeting of a course.
type Slot struct {
	Day     *string `json:"day"`
	Periods string  `json:"periods"`
	Start   *string `json:"start"`
	End     *string `json:"end"`
	Room    *string `json:"room"`
	Campus  *string `json:"campus"`
}

// Course is one course offering with its parsed meeting times.
type Course struct {
	Acysem      string   `json:"acysem"`
	CosID       any      `json:"cos_id"`
	CosCode     any      `json:"cos_code"`
	Name        any      `json:"name"`
	NameEn      any      `json:"name_en"`
	Teacher     any      `json:"teacher"`
	Credit      *float64 `json:"credit"`
	Hours       *float64 `json:"hours"`
	Type        any      `json:"type"`
	Time        string   `json:"time"`
	Slots       []Slot   `json:"slots"`
	Departments []string `json:"departments"`
	Limit       *int64   `json:"limit"`
	Memo        string   `json:"memo"`
}

func ptr(s string) *string { return &s }

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type dayRun struct{ day, periods string }

// headRuns splits the part of a chunk before the room dash into day runs.
//
// A chunk can pack more than one day into one room: "M78R34" is Mon 7-8 and
// Thu 3-4. It is decomposed only when the whole head is valid day+period runs;
// anything else (an unknown day letter, "TBA") stays one raw, undated run
// rather than a guess.
func headRuns(head string) []dayRun {
	if head != "" && dayRunSequenceRE.MatchString(head) {
		var runs []dayRun
		for _, m := range dayRunRE.FindAllStringSubmatch(head, -1) {
			runs = append(runs, dayRun{day: m[1], periods: m[2]})
		}
		return runs
	}
	return []dayRun{{periods: head}}
}

// ParseCosTime parses the timetable's cos_time field, e.g.
// "M78R34-ED117[GF],W5-EC015[GF]", into weekly slots.
func ParseCosTime(raw string) []Slot {
	slots := []Slot{}
	for _, chunk := range strings.Split(raw, ",") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		head, rest, _ := strings.Cut(chunk, "-")
		var room, campus *string
		if m := roomRE.FindStringSubmatch(rest); m != nil {
			room = nonEmpty(strings.TrimSpace(m[1]))
			campus = nonEmpty(m[2])
		}
		for _, run := range headRuns(head) {
			slot := Slot{Periods: run.periods, Room: room, Campus: campus}
			if name, ok := dayNameOf(run.day); ok {
				slot.Day = ptr(name)
				if p, ok := periodByCode[run.periods[:1]]; ok {
					slot.Start = ptr(p.Start)
				}
				if p, ok := periodByCode[run.periods[len(run.periods)-1:]]; ok {
					slot.End = ptr(p.End)
				}
			}
			slots = append(slots, slot)
		}
	}
	return slots
}

func stripHTML(s string) string {
	return strings.TrimSpace(html.UnescapeString(tagRE.ReplaceAllString(s, "")))
}

func toFloat(v any) *float64 {
	var s string
	switch x := v.(type) {
	case string:
		s = strings.TrimSpace(x)
	default:
		s = jsonx.Str(v)
	}
	if s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &f
}

func toLimit(v any) *int64 {
	s := jsonx.Str(v)
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// ParseCourses flattens the course-list payload (department → group → course)
// into one entry per course, in payload order. A course listed under several
// departments appears once, with every department named.
func ParseCourses(raw []byte) []Course {
	deps, err := jsonx.Members(raw)
	if err != nil {
		return []Course{}
	}
	var order []string
	courses := map[string]*Course{}
	for _, dep := range deps {
		members, err := jsonx.Members(dep.Value)
		if err != nil {
			continue
		}
		var depName string
		for _, m := range members {
			if m.Key == "dep_cname" {
				v, _ := jsonx.Decode(m.Value)
				depName = jsonx.Str(v)
			}
		}
		for _, group := range members {
			if skipKeys[group.Key] {
				continue
			}
			entries, err := jsonx.Members(group.Value)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				decoded, _ := jsonx.Decode(entry.Value)
				course := jsonx.Obj(decoded)
				if course == nil {
					continue
				}
				c, seen := courses[entry.Key]
				if !seen {
					cosTime := jsonx.Str(course["cos_time"])
					c = &Course{
						Acysem:      jsonx.Str(course["acy"]) + jsonx.Str(course["sem"]),
						CosID:       course["cos_id"],
						CosCode:     jsonx.OrNil(course["cos_code"]),
						Name:        course["cos_cname"],
						NameEn:      jsonx.OrNil(course["cos_ename"]),
						Teacher:     jsonx.OrNil(course["teacher"]),
						Credit:      toFloat(course["cos_credit"]),
						Hours:       toFloat(course["cos_hours"]),
						Type:        jsonx.OrNil(course["cos_type"]),
						Time:        cosTime,
						Slots:       ParseCosTime(cosTime),
						Departments: []string{},
						Limit:       toLimit(course["num_limit"]),
						Memo:        stripHTML(jsonx.Str(course["memo"])),
					}
					courses[entry.Key] = c
					order = append(order, entry.Key)
				}
				entryDep := depName
				if entryDep == "" {
					entryDep = jsonx.Str(course["dep_cname"])
				}
				if entryDep != "" && !contains(c.Departments, entryDep) {
					c.Departments = append(c.Departments, entryDep)
				}
			}
		}
	}
	out := make([]Course, 0, len(order))
	for _, key := range order {
		out = append(out, *courses[key])
	}
	return out
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
