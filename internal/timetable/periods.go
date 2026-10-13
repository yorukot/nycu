package timetable

import (
	"bytes"
	"encoding/json"
)

// Period is one class period and its clock times.
type Period struct {
	Code  string `json:"code"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// Periods is NYCU's period table, in day order.
var Periods = []Period{
	{"y", "06:00", "06:50"},
	{"z", "07:00", "07:50"},
	{"1", "08:00", "08:50"},
	{"2", "09:00", "09:50"},
	{"3", "10:10", "11:00"},
	{"4", "11:10", "12:00"},
	{"n", "12:20", "13:10"},
	{"5", "13:20", "14:10"},
	{"6", "14:20", "15:10"},
	{"7", "15:30", "16:20"},
	{"8", "16:30", "17:20"},
	{"9", "17:30", "18:20"},
	{"a", "18:30", "19:20"},
	{"b", "19:30", "20:20"},
	{"c", "20:30", "21:20"},
	{"d", "21:30", "22:20"},
}

type dayName struct{ letter, name string }

// dayNames maps the timetable's day letters to weekday names, Monday first.
var dayNames = []dayName{
	{"M", "Mon"}, {"T", "Tue"}, {"W", "Wed"}, {"R", "Thu"}, {"F", "Fri"}, {"S", "Sat"}, {"U", "Sun"},
}

var periodByCode = func() map[string]Period {
	m := make(map[string]Period, len(Periods))
	for _, p := range Periods {
		m[p.Code] = p
	}
	return m
}()

func dayNameOf(letter string) (string, bool) {
	for _, d := range dayNames {
		if d.letter == letter {
			return d.name, true
		}
	}
	return "", false
}

// Days is the day-letter table. It marshals as an object in weekday order.
type Days struct{}

func (Days) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, d := range dayNames {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, _ := json.Marshal(d.letter)
		value, _ := json.Marshal(d.name)
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Reference is the static period and day table.
type Reference struct {
	Periods []Period `json:"periods"`
	Days    Days     `json:"days"`
}

// PeriodTable returns the static reference table. It makes no network call.
func PeriodTable() Reference {
	return Reference{Periods: Periods}
}
