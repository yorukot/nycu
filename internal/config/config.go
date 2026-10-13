// Package config reads the server configuration from environment variables.
package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/yorukot/nycu/internal/e3"
	"github.com/yorukot/nycu/internal/truncate"
)

const (
	minSecretBytes   = 32
	minPasswordChars = 12
)

// Config is the validated server configuration.
type Config struct {
	Port               string
	PublicURL          string
	OwnerPassword      string
	AuthSecret         []byte
	E3Token            string
	E3UserID           int64
	E3Base             string
	E3DownloadMaxBytes int64
	Limits             truncate.Limits
}

// Load reads and validates the configuration. It reports every problem at
// once, naming the variables, and never echoes a secret value.
func Load(getenv func(string) string) (Config, error) {
	var problems []string
	cfg := Config{
		Port:          valueOr(getenv("PORT"), "8080"),
		PublicURL:     strings.TrimRight(strings.TrimSpace(getenv("PUBLIC_URL")), "/"),
		OwnerPassword: getenv("OWNER_PASSWORD"),
		AuthSecret:    []byte(getenv("AUTH_SECRET")),
		E3Token:       strings.TrimSpace(getenv("E3_TOKEN")),
		E3Base:        strings.TrimRight(valueOr(getenv("E3_BASE"), e3.DefaultBase), "/"),
		Limits:        truncate.DefaultLimits(),
	}

	var missing []string
	for _, name := range []string{"PUBLIC_URL", "OWNER_PASSWORD", "AUTH_SECRET", "E3_TOKEN"} {
		if strings.TrimSpace(getenv(name)) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		problems = append(problems, "missing required variables: "+strings.Join(missing, ", "))
	}

	if cfg.PublicURL != "" {
		if err := checkPublicURL(cfg.PublicURL); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(cfg.AuthSecret) > 0 && len(cfg.AuthSecret) < minSecretBytes {
		problems = append(problems, fmt.Sprintf("AUTH_SECRET must be at least %d bytes (try `openssl rand -base64 48`)", minSecretBytes))
	}
	if cfg.OwnerPassword != "" && len([]rune(cfg.OwnerPassword)) < minPasswordChars {
		problems = append(problems, fmt.Sprintf("OWNER_PASSWORD must be at least %d characters", minPasswordChars))
	}
	if u, err := url.Parse(cfg.E3Base); err != nil || u.Scheme != "https" || u.Host == "" {
		problems = append(problems, "E3_BASE must be an https URL")
	}

	var err error
	if cfg.E3UserID, err = positiveInt(getenv, "E3_USERID", 0); err != nil {
		problems = append(problems, err.Error())
	}
	if cfg.E3DownloadMaxBytes, err = positiveInt(getenv, "E3_DOWNLOAD_MAX_BYTES", 10<<20); err != nil {
		problems = append(problems, err.Error())
	}
	maxString, err := positiveInt(getenv, "MCP_MAX_STRING_CHARS", truncate.DefaultMaxStringChars)
	if err != nil {
		problems = append(problems, err.Error())
	}
	maxTotal, err := positiveInt(getenv, "MCP_MAX_TOTAL_CHARS", truncate.DefaultMaxTotalChars)
	if err != nil {
		problems = append(problems, err.Error())
	}
	cfg.Limits = truncate.Limits{MaxStringChars: int(maxString), MaxTotalChars: int(maxTotal)}

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return cfg, nil
}

// checkPublicURL requires an origin: https, or http on a loopback host for local testing.
func checkPublicURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return fmt.Errorf("PUBLIC_URL must be an origin such as https://nycu.example.com, without a path")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if host := u.Hostname(); host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return nil
		}
	}
	return fmt.Errorf("PUBLIC_URL must use https (plain http is allowed only for localhost)")
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

func positiveInt(getenv func(string) string, name string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return n, nil
}
