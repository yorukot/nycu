package config

import (
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func valid() map[string]string {
	return map[string]string{
		"PUBLIC_URL":     "https://nycu.example.com/",
		"OWNER_PASSWORD": "a long owner password",
		"AUTH_SECRET":    strings.Repeat("k", 32),
		"E3_TOKEN":       "0123456789abcdef",
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://nycu.example.com" || cfg.Port != "8080" || cfg.E3Base != "https://e3p.nycu.edu.tw" ||
		cfg.E3DownloadMaxBytes != 10<<20 || cfg.E3UserID != 0 || cfg.Limits.MaxStringChars != 20_000 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadReportsEveryProblemWithoutSecrets(t *testing.T) {
	values := map[string]string{
		"PUBLIC_URL":     "http://nycu.example.com/mcp",
		"OWNER_PASSWORD": "short-pass",
		"AUTH_SECRET":    "too-short-secret",
		"E3_USERID":      "abc",
	}
	_, err := Load(env(values))
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	for _, want := range []string{"E3_TOKEN", "PUBLIC_URL must be an origin", "AUTH_SECRET must be at least", "OWNER_PASSWORD must be at least", "E3_USERID"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in %s", want, msg)
		}
	}
	for _, secret := range []string{"short-pass", "too-short-secret"} {
		if strings.Contains(msg, secret) {
			t.Fatalf("error echoes a secret: %s", msg)
		}
	}
}

func TestPublicURLAllowsHTTPOnlyOnLoopback(t *testing.T) {
	values := valid()
	values["PUBLIC_URL"] = "http://localhost:8080"
	if _, err := Load(env(values)); err != nil {
		t.Fatal(err)
	}
	values["PUBLIC_URL"] = "http://nycu.example.com"
	if _, err := Load(env(values)); err == nil || !strings.Contains(err.Error(), "must use https") {
		t.Fatalf("got %v", err)
	}
}
