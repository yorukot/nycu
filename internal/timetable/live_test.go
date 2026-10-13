package timetable

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLive calls the real public timetable. It runs only with NYCU_LIVE=1, so
// CI never depends on the NYCU network.
func TestLive(t *testing.T) {
	if os.Getenv("NYCU_LIVE") != "1" {
		t.Skip("set NYCU_LIVE=1 to call timetable.nycu.edu.tw")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c := NewClient("", nil)

	semesters, err := c.Semesters(ctx)
	if err != nil || len(semesters) == 0 {
		t.Fatalf("semesters: %v %v", semesters, err)
	}
	courses, meta, err := c.Search(ctx, "微積分", ByName, "")
	if err != nil || len(courses) == 0 {
		t.Fatalf("search: %v %v", meta, err)
	}
	t.Logf("latest %v, %d calculus courses, first %v %v", semesters[0], len(courses), courses[0].CosID, courses[0].Time)
}
