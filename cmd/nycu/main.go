// Command nycu serves NYCU E3 and the public timetable as a remote MCP server,
// and helps the owner obtain an E3 token.
//
//	nycu serve        run the server (the default)
//	nycu e3-token     log in to E3 locally and print the E3_TOKEN to configure
//	nycu healthcheck  probe /healthz, for the container HEALTHCHECK
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/term"

	"github.com/yorukot/nycu/internal/config"
	"github.com/yorukot/nycu/internal/e3"
	"github.com/yorukot/nycu/internal/mcpserver"
	"github.com/yorukot/nycu/internal/oauth"
	"github.com/yorukot/nycu/internal/timetable"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cmd, args := "serve", os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "e3-token":
		err = e3Token(args)
	case "healthcheck":
		err = healthcheck()
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q; use serve, e3-token, healthcheck or version\n", cmd)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func serve() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	e3Client := e3.NewClient(e3.Options{
		Base:             cfg.E3Base,
		Token:            cfg.E3Token,
		UserID:           cfg.E3UserID,
		MaxDownloadBytes: cfg.E3DownloadMaxBytes,
	})
	mcpServer := mcpserver.New(mcpserver.Options{
		E3:        e3Client,
		Timetable: timetable.NewClient("", nil),
		Limits:    cfg.Limits,
		Version:   version,
	})
	authServer := oauth.New(oauth.Config{PublicURL: cfg.PublicURL, OwnerPassword: cfg.OwnerPassword, Secret: cfg.AuthSecret})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	authServer.Register(mux)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
	})
	mux.Handle(oauth.ResourcePath, withCORS(authServer.Middleware()(mcpHandler)))

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           accessLog(logger, mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	logger.Info("listening", "addr", srv.Addr, "public_url", cfg.PublicURL, "mcp", cfg.PublicURL+oauth.ResourcePath, "version", version)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// withCORS lets browser-based MCP clients reach /mcp. Preflight requests carry
// no token, so they are answered before the auth check.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id, WWW-Authenticate")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Mcp-Session-Id, MCP-Protocol-Version, Last-Event-ID")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// accessLog logs method, path, status and duration. It never logs query
// strings or headers, which carry codes, tokens and passwords.
func accessLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" {
			return
		}
		logger.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "ms", time.Since(start).Milliseconds())
	})
}

func healthcheck() error {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %s", resp.Status)
	}
	return nil
}

// e3Token logs in to E3 on this machine and prints the token to configure on
// the server. The password is read at a hidden prompt and goes only to E3.
func e3Token(args []string) error {
	fs := flag.NewFlagSet("e3-token", flag.ExitOnError)
	base := fs.String("base", e3.DefaultBase, "Moodle site root")
	username := fs.String("username", "", "Moodle login, usually the student ID")
	service := fs.String("service", e3.DefaultService, "Web Services service shortname")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: nycu e3-token [-username ID] [-base URL]")
		fmt.Fprintln(fs.Output(), "\nLogs in to E3 and prints E3_TOKEN and E3_USERID for the server's environment.")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("e3-token needs an interactive terminal for the hidden password prompt")
	}
	user := strings.TrimSpace(*username)
	if user == "" {
		fmt.Fprint(os.Stderr, "E3 username (student ID): ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return err
		}
		user = strings.TrimSpace(line)
	}
	fmt.Fprint(os.Stderr, "E3 password: ")
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client := e3.NewClient(e3.Options{Base: *base})
	token, err := e3.RequestToken(ctx, client, user, string(password), *service)
	if err != nil {
		return err
	}
	profile, err := client.WithToken(token).Whoami(ctx)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Logged in as %v (%v, uid %v). Set these on the server and keep them secret:\n\n", profile.Fullname, profile.Username, profile.UserID)
	fmt.Printf("E3_TOKEN=%s\nE3_USERID=%v\n", token, profile.UserID)
	return nil
}
