package e3

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/yorukot/nycu/internal/toolerr"
)

// TestLive calls the real E3. It runs only with NYCU_LIVE=1. Without E3_TOKEN
// it checks only that TLS and the error mapping work; with E3_TOKEN it also
// runs read-only calls.
func TestLive(t *testing.T) {
	if os.Getenv("NYCU_LIVE") != "1" {
		t.Skip("set NYCU_LIVE=1 to call e3p.nycu.edu.tw")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	_, err := NewClient(Options{Token: "not-a-real-token"}).Whoami(ctx)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Message != "invalid or expired token" {
		t.Fatalf("bogus token: want invalid token error, got %v", err)
	}

	token := os.Getenv("E3_TOKEN")
	if token == "" {
		t.Log("E3_TOKEN not set; skipped authenticated calls")
		return
	}
	c := NewClient(Options{Token: token})
	profile, err := c.Whoami(ctx)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	courses, _, err := c.Courses(ctx, false)
	if err != nil {
		t.Fatalf("courses: %v", err)
	}
	due, _, err := c.Due(ctx, 14, 50)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	t.Logf("user %v: %d visible courses, %d due events", profile.UserID, len(courses), len(due))
}
