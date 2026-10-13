// Package oauth is a minimal, single-owner OAuth 2.1 authorization server for
// the MCP endpoint.
//
// ChatGPT connects to a custom MCP server only with OAuth or with no
// authentication, so the server protects /mcp with OAuth: dynamic client
// registration (RFC 7591), the authorization code flow with PKCE S256,
// refresh tokens, and the discovery documents (RFC 8414, RFC 9728).
//
// There is one user. Approving a client means typing the owner password on
// the authorization page. Nothing is stored: client IDs, codes and tokens are
// HMAC-signed values, so rotating the secret revokes everything at once. The
// only memory is the set of used codes and rotated refresh tokens, and the
// password-failure counter.
package oauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

const (
	codeTTL    = time.Minute
	accessTTL  = time.Hour
	refreshTTL = 30 * 24 * time.Hour

	// ResourcePath is where the protected MCP endpoint is served.
	ResourcePath = "/mcp"
)

// Config configures the authorization server.
type Config struct {
	// PublicURL is the externally visible origin, e.g. https://nycu.example.com.
	PublicURL     string
	OwnerPassword string
	Secret        []byte
	Now           func() time.Time
}

// Server serves the OAuth endpoints and verifies access tokens.
type Server struct {
	issuer       string
	resource     string
	passwordHash [32]byte
	signer       *signer
	used         *expirySet
	limiter      *failureLimiter
	now          func() time.Time
}

// New builds a Server.
func New(cfg Config) *Server {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	issuer := strings.TrimRight(cfg.PublicURL, "/")
	return &Server{
		issuer:       issuer,
		resource:     issuer + ResourcePath,
		passwordHash: sha256.Sum256([]byte(cfg.OwnerPassword)),
		signer:       newSigner(cfg.Secret, now),
		used:         newExpirySet(),
		limiter:      newFailureLimiter(),
		now:          now,
	}
}

// ResourceMetadataURL is advertised in the WWW-Authenticate header of a 401.
func (s *Server) ResourceMetadataURL() string {
	return s.issuer + "/.well-known/oauth-protected-resource" + ResourcePath
}

// Register adds the discovery and OAuth endpoints to mux.
func (s *Server) Register(mux *http.ServeMux) {
	resourceMeta := auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               s.resource,
		AuthorizationServers:   []string{s.issuer},
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "NYCU",
	})
	mux.Handle("/.well-known/oauth-protected-resource", resourceMeta)
	mux.Handle("/.well-known/oauth-protected-resource"+ResourcePath, resourceMeta)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.serveMetadata)
	mux.HandleFunc("GET /.well-known/openid-configuration", s.serveMetadata)
	mux.HandleFunc("/oauth/register", s.serveRegister)
	mux.HandleFunc("GET /oauth/authorize", s.serveAuthorizePage)
	mux.HandleFunc("POST /oauth/authorize", s.serveAuthorizeSubmit)
	mux.HandleFunc("/oauth/token", s.serveToken)
}

// Middleware rejects requests without a valid access token, answering 401 with
// a pointer to the resource metadata so clients can start the OAuth flow.
func (s *Server) Middleware() func(http.Handler) http.Handler {
	return auth.RequireBearerToken(s.VerifyAccessToken, &auth.RequireBearerTokenOptions{ResourceMetadataURL: s.ResourceMetadataURL()})
}

// VerifyAccessToken is the auth.TokenVerifier for /mcp.
func (s *Server) VerifyAccessToken(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	c, err := s.signer.verify(token, kindAccess)
	if err != nil || c.Audience != s.resource {
		return nil, fmt.Errorf("%w: unknown, expired or foreign token", auth.ErrInvalidToken)
	}
	var scopes []string
	if c.Scope != "" {
		scopes = strings.Fields(c.Scope)
	}
	return &auth.TokenInfo{UserID: "owner", Expiration: time.Unix(c.ExpiresAt, 0), Scopes: scopes}, nil
}

// ── metadata ─────────────────────────────────────────────────────

type serverMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	ResponseModesSupported            []string `json:"response_modes_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	AuthorizationResponseISSSupported bool     `json:"authorization_response_iss_parameter_supported"`
}

var authMethods = []string{"none", "client_secret_post", "client_secret_basic"}

func (s *Server) serveMetadata(w http.ResponseWriter, _ *http.Request) {
	allowCORS(w)
	writeJSON(w, http.StatusOK, serverMetadata{
		Issuer:                            s.issuer,
		AuthorizationEndpoint:             s.issuer + "/oauth/authorize",
		TokenEndpoint:                     s.issuer + "/oauth/token",
		RegistrationEndpoint:              s.issuer + "/oauth/register",
		ResponseTypesSupported:            []string{"code"},
		ResponseModesSupported:            []string{"query"},
		GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
		CodeChallengeMethodsSupported:     []string{"S256"},
		TokenEndpointAuthMethodsSupported: authMethods,
		AuthorizationResponseISSSupported: true,
	})
}

// ── dynamic client registration (RFC 7591) ───────────────────────

type registrationRequest struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

type registrationResponse struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientSecretExpiresAt   *int64   `json:"client_secret_expires_at,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

func (s *Server) serveRegister(w http.ResponseWriter, r *http.Request) {
	allowCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req registrationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "body must be JSON client metadata")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 10 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "register between 1 and 10 redirect_uris")
		return
	}
	for _, uri := range req.RedirectURIs {
		if err := validRedirectURI(uri); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
			return
		}
	}
	if len(req.ClientName) > 200 {
		req.ClientName = req.ClientName[:200]
	}

	method := "none"
	if req.TokenEndpointAuthMethod == "client_secret_post" || req.TokenEndpointAuthMethod == "client_secret_basic" {
		method = req.TokenEndpointAuthMethod
	}
	clientID := s.signer.sign(claims{Kind: kindClient, RedirectURIs: req.RedirectURIs, ClientName: req.ClientName, Confidential: method != "none"})
	resp := registrationResponse{
		ClientID:                clientID,
		ClientIDIssuedAt:        s.now().Unix(),
		RedirectURIs:            req.RedirectURIs,
		ClientName:              req.ClientName,
		TokenEndpointAuthMethod: method,
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
	}
	if method != "none" {
		never := int64(0)
		resp.ClientSecret = s.signer.clientSecret(clientID)
		resp.ClientSecretExpiresAt = &never
	}
	writeJSON(w, http.StatusCreated, resp)
}

// validRedirectURI accepts https, http on a loopback host (native clients such
// as Claude Code), and private-use schemes. It rejects schemes that run code.
func validRedirectURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Fragment != "" {
		return fmt.Errorf("redirect_uri %q must be an absolute URL without a fragment", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		if u.Host == "" {
			return fmt.Errorf("redirect_uri %q has no host", raw)
		}
	case "http":
		host := u.Hostname()
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return fmt.Errorf("redirect_uri %q: plain http is allowed only on a loopback host", raw)
		}
	case "javascript", "data", "file", "vbscript", "blob", "about":
		return fmt.Errorf("redirect_uri %q uses a forbidden scheme", raw)
	}
	return nil
}

// ── authorization endpoint ───────────────────────────────────────

// authRequest is a validated authorization request.
type authRequest struct {
	ClientID      string
	ClientName    string
	RedirectURI   string
	State         string
	CodeChallenge string
	Scope         string
	Resource      string
}

// requestError is a failure of an authorization request. When redirect is
// set, the redirect URI was verified and the error goes back to the client;
// otherwise it is shown on the page, because an unverified URI must never
// receive a redirect.
type requestError struct {
	redirect    bool
	code        string
	description string
}

func (s *Server) parseAuthRequest(form url.Values) (authRequest, *requestError) {
	client, err := s.signer.verify(form.Get("client_id"), kindClient)
	if err != nil {
		return authRequest{}, &requestError{code: "invalid_client", description: "Unknown client. Remove the connector and add it again."}
	}
	req := authRequest{
		ClientID:      form.Get("client_id"),
		ClientName:    client.ClientName,
		RedirectURI:   form.Get("redirect_uri"),
		State:         form.Get("state"),
		CodeChallenge: form.Get("code_challenge"),
		Scope:         form.Get("scope"),
		Resource:      form.Get("resource"),
	}
	if req.RedirectURI == "" && len(client.RedirectURIs) == 1 {
		req.RedirectURI = client.RedirectURIs[0]
	}
	registered := false
	for _, uri := range client.RedirectURIs {
		if uri == req.RedirectURI {
			registered = true
		}
	}
	if !registered {
		return req, &requestError{code: "invalid_request", description: "The redirect_uri was not registered for this client."}
	}
	if form.Get("response_type") != "code" {
		return req, &requestError{redirect: true, code: "unsupported_response_type", description: "only response_type=code is supported"}
	}
	if req.CodeChallenge == "" || form.Get("code_challenge_method") != "S256" {
		return req, &requestError{redirect: true, code: "invalid_request", description: "PKCE with code_challenge_method=S256 is required"}
	}
	if req.Resource != "" && !s.isResource(req.Resource) {
		return req, &requestError{redirect: true, code: "invalid_target", description: "resource must be " + s.resource}
	}
	return req, nil
}

// isResource accepts the MCP endpoint and, for clients that send the origin, the issuer.
func (s *Server) isResource(resource string) bool {
	resource = strings.TrimRight(resource, "/")
	return resource == s.resource || resource == s.issuer
}

func (s *Server) serveAuthorizePage(w http.ResponseWriter, r *http.Request) {
	req, reqErr := s.parseAuthRequest(r.URL.Query())
	if reqErr != nil {
		s.failAuthorize(w, r, req, reqErr)
		return
	}
	renderAuthorizePage(w, http.StatusOK, req, "")
}

func (s *Server) serveAuthorizeSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	req, reqErr := s.parseAuthRequest(r.PostForm)
	if reqErr != nil {
		s.failAuthorize(w, r, req, reqErr)
		return
	}

	ip, now := clientIP(r), s.now()
	if s.limiter.blocked(ip, now) {
		renderAuthorizePage(w, http.StatusTooManyRequests, req, "Too many wrong passwords. Try again in 15 minutes.")
		return
	}
	if r.PostForm.Get("decision") == "deny" {
		redirectWithParams(w, r, req.RedirectURI, url.Values{"error": {"access_denied"}, "state": {req.State}, "iss": {s.issuer}})
		return
	}
	given := sha256.Sum256([]byte(r.PostForm.Get("password")))
	if subtle.ConstantTimeCompare(given[:], s.passwordHash[:]) != 1 {
		s.limiter.fail(ip, now)
		renderAuthorizePage(w, http.StatusUnauthorized, req, "Wrong password.")
		return
	}

	code := s.signer.sign(claims{
		Kind:          kindCode,
		ExpiresAt:     now.Add(codeTTL).Unix(),
		ID:            randomID(),
		ClientHash:    clientHash(req.ClientID),
		RedirectURI:   req.RedirectURI,
		CodeChallenge: req.CodeChallenge,
		Scope:         req.Scope,
	})
	redirectWithParams(w, r, req.RedirectURI, url.Values{"code": {code}, "state": {req.State}, "iss": {s.issuer}})
}

func (s *Server) failAuthorize(w http.ResponseWriter, r *http.Request, req authRequest, e *requestError) {
	if e.redirect {
		redirectWithParams(w, r, req.RedirectURI, url.Values{
			"error": {e.code}, "error_description": {e.description}, "state": {req.State}, "iss": {s.issuer},
		})
		return
	}
	renderErrorPage(w, http.StatusBadRequest, e.description)
}

func redirectWithParams(w http.ResponseWriter, r *http.Request, target string, params url.Values) {
	u, _ := url.Parse(target)
	query := u.Query()
	for key, values := range params {
		if len(values) > 0 && values[0] != "" {
			query.Set(key, values[0])
		}
	}
	u.RawQuery = query.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// ── token endpoint ───────────────────────────────────────────────

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope,omitempty"`
}

func (s *Server) serveToken(w http.ResponseWriter, r *http.Request) {
	allowCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "body must be form-encoded")
		return
	}
	clientID, clientSecret, hasBasic := r.BasicAuth()
	if !hasBasic {
		clientID, clientSecret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	} else if unescaped, err := url.QueryUnescape(clientID); err == nil {
		// RFC 6749 §2.3.1 form-encodes Basic credentials.
		clientID = unescaped
		if secret, err := url.QueryUnescape(clientSecret); err == nil {
			clientSecret = secret
		}
	}
	client, err := s.signer.verify(clientID, kindClient)
	if err != nil {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "unknown client_id")
		return
	}
	if client.Confidential && subtle.ConstantTimeCompare([]byte(clientSecret), []byte(s.signer.clientSecret(clientID))) != 1 {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "wrong client_secret")
		return
	}

	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.exchangeCode(w, r.PostForm, clientID)
	case "refresh_token":
		s.refresh(w, r.PostForm, clientID)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
	}
}

func (s *Server) exchangeCode(w http.ResponseWriter, form url.Values, clientID string) {
	code, err := s.signer.verify(form.Get("code"), kindCode)
	if err != nil || code.ClientHash != clientHash(clientID) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the code is invalid, expired or for another client")
		return
	}
	if redirect := form.Get("redirect_uri"); redirect != "" && redirect != code.RedirectURI {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match the authorization request")
		return
	}
	verifier := form.Get("code_verifier")
	sum := sha256.Sum256([]byte(verifier))
	if verifier == "" || subtle.ConstantTimeCompare([]byte(b64.EncodeToString(sum[:])), []byte(code.CodeChallenge)) != 1 {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match the code_challenge")
		return
	}
	if resource := form.Get("resource"); resource != "" && !s.isResource(resource) {
		oauthError(w, http.StatusBadRequest, "invalid_target", "resource must be "+s.resource)
		return
	}
	if !s.used.add(code.ID, time.Unix(code.ExpiresAt, 0), s.now()) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the code was already used")
		return
	}
	s.issueTokens(w, clientID, code.Scope)
}

func (s *Server) refresh(w http.ResponseWriter, form url.Values, clientID string) {
	old, err := s.signer.verify(form.Get("refresh_token"), kindRefresh)
	if err != nil || old.ClientHash != clientHash(clientID) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token is invalid, expired or for another client")
		return
	}
	// Rotation: each refresh token works once (until a restart clears the set).
	if !s.used.add(old.ID, time.Unix(old.ExpiresAt, 0), s.now()) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token was already used")
		return
	}
	s.issueTokens(w, clientID, old.Scope)
}

func (s *Server) issueTokens(w http.ResponseWriter, clientID, scope string) {
	now := s.now()
	hash := clientHash(clientID)
	access := s.signer.sign(claims{Kind: kindAccess, ExpiresAt: now.Add(accessTTL).Unix(), ID: randomID(), ClientHash: hash, Scope: scope, Audience: s.resource})
	refresh := s.signer.sign(claims{Kind: kindRefresh, ExpiresAt: now.Add(refreshTTL).Unix(), ID: randomID(), ClientHash: hash, Scope: scope})
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, tokenResponse{
		AccessToken:  access,
		TokenType:    "Bearer",
		ExpiresIn:    int64(accessTTL / time.Second),
		RefreshToken: refresh,
		Scope:        scope,
	})
}

// ── helpers ──────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Basic realm="nycu"`)
	}
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func allowCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Protocol-Version")
}
