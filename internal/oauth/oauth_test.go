package oauth

import (
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testPassword = "correct horse battery staple"
	callback     = "http://localhost:3333/callback"
	verifier     = "a-very-long-random-code-verifier-0123456789-abcdefghijklmnop"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type harness struct {
	t      *testing.T
	server *httptest.Server
	auth   *Server
	clock  *clock
	http   *http.Client
}

func newHarness(t *testing.T) *harness {
	// The SDK middleware checks expiry against the real clock, so start there.
	clk := &clock{t: time.Now()}
	mux := http.NewServeMux()
	h := &harness{t: t, clock: clk}
	h.server = httptest.NewServer(mux)
	t.Cleanup(h.server.Close)
	h.auth = New(Config{PublicURL: h.server.URL, OwnerPassword: testPassword, Secret: []byte(strings.Repeat("s", 32)), Now: clk.now})
	h.auth.Register(mux)
	mux.Handle(ResourcePath, h.auth.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("mcp ok"))
	})))
	h.http = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return h
}

func (h *harness) getJSON(path string) map[string]any {
	resp, err := h.http.Get(h.server.URL + path)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatalf("%s: %v", path, err)
	}
	return out
}

func (h *harness) register(body string) (int, map[string]any) {
	resp, err := h.http.Post(h.server.URL+"/oauth/register", "application/json", strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (h *harness) clientID() string {
	status, out := h.register(`{"redirect_uris":["` + callback + `"],"client_name":"Test <Client>"}`)
	if status != http.StatusCreated {
		h.t.Fatalf("register: %d %v", status, out)
	}
	return out["client_id"].(string)
}

func challenge(v string) string {
	sum := sha256.Sum256([]byte(v))
	return b64.EncodeToString(sum[:])
}

// params is a valid authorization request for clientID.
func (h *harness) params(clientID string) url.Values {
	return url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {callback}, "state": {"xyz"},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "resource": {h.server.URL + ResourcePath},
	}
}

// authorize submits the password form and returns the response.
func (h *harness) authorize(params url.Values, password string) *http.Response {
	form := url.Values{}
	for k, v := range params {
		form[k] = v
	}
	form.Set("password", password)
	form.Set("decision", "allow")
	resp, err := h.http.PostForm(h.server.URL+"/oauth/authorize", form)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func (h *harness) code(clientID string) string {
	resp := h.authorize(h.params(clientID), testPassword)
	if resp.StatusCode != http.StatusFound {
		h.t.Fatalf("authorize: %d", resp.StatusCode)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc.Query().Get("state") != "xyz" || loc.Query().Get("iss") != h.server.URL {
		h.t.Fatalf("redirect = %s", loc)
	}
	return loc.Query().Get("code")
}

func (h *harness) token(form url.Values) (int, map[string]any) {
	resp, err := h.http.PostForm(h.server.URL+"/oauth/token", form)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (h *harness) callMCP(bearer string) *http.Response {
	req, _ := http.NewRequest(http.MethodPost, h.server.URL+ResourcePath, strings.NewReader("{}"))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := h.http.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestDiscoveryDocuments(t *testing.T) {
	h := newHarness(t)
	resource := h.getJSON("/.well-known/oauth-protected-resource/mcp")
	if resource["resource"] != h.server.URL+"/mcp" || resource["authorization_servers"].([]any)[0] != h.server.URL {
		t.Fatalf("resource metadata = %v", resource)
	}
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/openid-configuration"} {
		meta := h.getJSON(path)
		if meta["issuer"] != h.server.URL || meta["registration_endpoint"] != h.server.URL+"/oauth/register" ||
			meta["code_challenge_methods_supported"].([]any)[0] != "S256" {
			t.Fatalf("%s = %v", path, meta)
		}
	}
}

func TestFullFlowAndTokenUse(t *testing.T) {
	h := newHarness(t)

	resp := h.callMCP("")
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "/.well-known/oauth-protected-resource/mcp") {
		t.Fatalf("no token: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}

	clientID := h.clientID()
	page, err := h.http.Get(h.server.URL + "/oauth/authorize?" + h.params(clientID).Encode())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != http.StatusOK || !strings.Contains(string(body), "Test &lt;Client&gt;") || page.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("authorize page: %d", page.StatusCode)
	}

	if resp := h.authorize(h.params(clientID), "wrong password!"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	code := h.code(clientID)

	base := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "redirect_uri": {callback}}
	bad := url.Values{}
	for k, v := range base {
		bad[k] = v
	}
	bad.Set("code_verifier", "not-the-verifier-not-the-verifier-not-the-verifier")
	if status, out := h.token(bad); status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("wrong verifier: %d %v", status, out)
	}

	base.Set("code_verifier", verifier)
	status, tokens := h.token(base)
	if status != http.StatusOK || tokens["token_type"] != "Bearer" {
		t.Fatalf("exchange: %d %v", status, tokens)
	}
	if status, out := h.token(base); status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("code replay: %d %v", status, out)
	}

	access, refresh := tokens["access_token"].(string), tokens["refresh_token"].(string)
	if resp := h.callMCP(access); resp.StatusCode != http.StatusOK {
		t.Fatalf("with access token: %d", resp.StatusCode)
	}
	// A refresh token, a client ID or a code must never work as an access token.
	for _, wrongKind := range []string{refresh, clientID, code, access + "x"} {
		if resp := h.callMCP(wrongKind); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong token kind accepted: %d", resp.StatusCode)
		}
	}

	refreshForm := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}}
	status, rotated := h.token(refreshForm)
	if status != http.StatusOK || rotated["access_token"] == access {
		t.Fatalf("refresh: %d %v", status, rotated)
	}
	if status, _ := h.token(refreshForm); status != http.StatusBadRequest {
		t.Fatalf("refresh token reuse: %d", status)
	}

	h.clock.advance(2 * time.Hour)
	if resp := h.callMCP(access); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired access token: %d", resp.StatusCode)
	}
}

func TestCodeExpires(t *testing.T) {
	h := newHarness(t)
	clientID := h.clientID()
	code := h.code(clientID)
	h.clock.advance(2 * time.Minute)
	status, out := h.token(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID}, "code_verifier": {verifier}})
	if status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("expired code: %d %v", status, out)
	}
}

func TestCodeIsBoundToClient(t *testing.T) {
	h := newHarness(t)
	code := h.code(h.clientID())
	status, second := h.register(`{"redirect_uris":["http://127.0.0.1:9/cb"]}`)
	if status != http.StatusCreated {
		t.Fatalf("register second client: %d", status)
	}
	status, out := h.token(url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {second["client_id"].(string)}, "code_verifier": {verifier}})
	if status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("code used by another client: %d %v", status, out)
	}
}

func TestUnregisteredRedirectIsNotFollowed(t *testing.T) {
	h := newHarness(t)
	params := h.params(h.clientID())
	params.Set("redirect_uri", "https://evil.example/cb")
	resp, err := h.http.Get(h.server.URL + "/oauth/authorize?" + params.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
		t.Fatalf("got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, err = h.http.Get(h.server.URL + "/oauth/authorize?client_id=forged&redirect_uri=https://evil.example/cb")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
		t.Fatalf("forged client: %d", resp.StatusCode)
	}
}

func TestPKCEAndResourceAreEnforced(t *testing.T) {
	h := newHarness(t)
	clientID := h.clientID()
	for name, mutate := range map[string]func(url.Values){
		"no challenge":   func(p url.Values) { p.Del("code_challenge") },
		"plain method":   func(p url.Values) { p.Set("code_challenge_method", "plain") },
		"token response": func(p url.Values) { p.Set("response_type", "token") },
		"other resource": func(p url.Values) { p.Set("resource", "https://other.example/mcp") },
	} {
		params := h.params(clientID)
		mutate(params)
		resp, err := h.http.Get(h.server.URL + "/oauth/authorize?" + params.Encode())
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		loc, _ := url.Parse(resp.Header.Get("Location"))
		if resp.StatusCode != http.StatusFound || loc.Query().Get("error") == "" || loc.Query().Get("state") != "xyz" {
			t.Fatalf("%s: %d %s", name, resp.StatusCode, loc)
		}
	}
}

func TestDenyRedirectsWithAccessDenied(t *testing.T) {
	h := newHarness(t)
	form := h.params(h.clientID())
	form.Set("decision", "deny")
	resp, err := h.http.PostForm(h.server.URL+"/oauth/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || loc.Query().Get("error") != "access_denied" || loc.Query().Get("code") != "" {
		t.Fatalf("deny: %d %s", resp.StatusCode, loc)
	}
}

func TestPasswordGuessingIsLimited(t *testing.T) {
	h := newHarness(t)
	params := h.params(h.clientID())
	for range maxFailuresPerIP {
		if resp := h.authorize(params, "wrong password!"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong password: %d", resp.StatusCode)
		}
	}
	if resp := h.authorize(params, testPassword); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after lockout: %d", resp.StatusCode)
	}
	h.clock.advance(failureWindow)
	if resp := h.authorize(params, testPassword); resp.StatusCode != http.StatusFound {
		t.Fatalf("after the window: %d", resp.StatusCode)
	}
}

func TestConfidentialClientNeedsSecret(t *testing.T) {
	h := newHarness(t)
	status, reg := h.register(`{"redirect_uris":["https://chatgpt.com/connector_platform_oauth_redirect"],"token_endpoint_auth_method":"client_secret_post"}`)
	if status != http.StatusCreated || reg["client_secret"] == nil {
		t.Fatalf("register: %d %v", status, reg)
	}
	clientID, secret := reg["client_id"].(string), reg["client_secret"].(string)
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}, "client_id": {clientID}}
	if status, out := h.token(form); status != http.StatusUnauthorized || out["error"] != "invalid_client" {
		t.Fatalf("no secret: %d %v", status, out)
	}
	form.Set("client_secret", secret)
	if status, out := h.token(form); status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Fatalf("with secret: %d %v", status, out)
	}

	// The same secret works through HTTP Basic.
	req, _ := http.NewRequest(http.MethodPost, h.server.URL+"/oauth/token", strings.NewReader("grant_type=refresh_token&refresh_token=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(secret))
	resp, err := h.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("basic auth: %d", resp.StatusCode)
	}
}

func TestRegisterRejectsUnsafeRedirects(t *testing.T) {
	h := newHarness(t)
	for _, uri := range []string{"http://evil.example/cb", "javascript:alert(1)", "/relative", "https://ok.example/cb#frag"} {
		if status, _ := h.register(`{"redirect_uris":["` + uri + `"]}`); status != http.StatusBadRequest {
			t.Fatalf("%s accepted: %d", uri, status)
		}
	}
	if status, _ := h.register(`{"redirect_uris":["cursor://anysphere.cursor-retrieval/oauth/callback"]}`); status != http.StatusCreated {
		t.Fatalf("private-use scheme rejected: %d", status)
	}
}

func TestTokensFromAnotherDeploymentAreRejected(t *testing.T) {
	h := newHarness(t)
	other := New(Config{PublicURL: "https://other.example", OwnerPassword: testPassword, Secret: []byte(strings.Repeat("s", 32)), Now: h.clock.now})
	foreign := other.signer.sign(claims{Kind: kindAccess, ExpiresAt: h.clock.now().Add(time.Hour).Unix(), Audience: other.resource})
	if resp := h.callMCP(foreign); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("foreign audience accepted: %d", resp.StatusCode)
	}
}
