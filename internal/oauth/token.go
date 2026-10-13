package oauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Token kinds. Every signed value carries its kind, so a value issued for one
// purpose (a client ID, say) can never be replayed as another (an access token).
const (
	kindClient  = "client"
	kindCode    = "code"
	kindAccess  = "access"
	kindRefresh = "refresh"
)

var errBadToken = errors.New("invalid token")

// claims is the payload of every signed value. Fields unused by a kind are omitted.
type claims struct {
	Kind      string `json:"typ"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp,omitempty"`
	ID        string `json:"jti,omitempty"`

	// Client registration.
	RedirectURIs []string `json:"ru,omitempty"`
	ClientName   string   `json:"cn,omitempty"`
	Confidential bool     `json:"conf,omitempty"`

	// Authorization code, access and refresh tokens.
	ClientHash    string `json:"cid,omitempty"`
	RedirectURI   string `json:"rdr,omitempty"`
	CodeChallenge string `json:"cc,omitempty"`
	Scope         string `json:"scp,omitempty"`
	Audience      string `json:"aud,omitempty"`
}

// signer issues and verifies "v1.<payload>.<mac>" values with HMAC-SHA256.
type signer struct {
	key []byte
	now func() time.Time
}

func newSigner(secret []byte, now func() time.Time) *signer {
	// Derive the signing key, so the configured secret is used for one purpose only.
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("nycu-oauth-signing-v1"))
	return &signer{key: mac.Sum(nil), now: now}
}

var b64 = base64.RawURLEncoding

func (s *signer) mac(data string) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func (s *signer) sign(c claims) string {
	c.IssuedAt = s.now().Unix()
	payload, _ := json.Marshal(c)
	body := "v1." + b64.EncodeToString(payload)
	return body + "." + b64.EncodeToString(s.mac(body))
}

// verify checks the signature, kind and expiry of a value.
func (s *signer) verify(token, kind string) (claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return claims{}, errBadToken
	}
	got, err := b64.DecodeString(parts[2])
	if err != nil || !hmac.Equal(got, s.mac(parts[0]+"."+parts[1])) {
		return claims{}, errBadToken
	}
	payload, err := b64.DecodeString(parts[1])
	if err != nil {
		return claims{}, errBadToken
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil || c.Kind != kind {
		return claims{}, errBadToken
	}
	if c.ExpiresAt != 0 && s.now().Unix() >= c.ExpiresAt {
		return claims{}, errBadToken
	}
	return c, nil
}

// clientSecret derives a confidential client's secret from its ID, so nothing
// has to be stored.
func (s *signer) clientSecret(clientID string) string {
	return b64.EncodeToString(s.mac("client-secret:" + clientID))
}

// clientHash shortens a client ID for embedding in codes and tokens.
func clientHash(clientID string) string {
	sum := sha256.Sum256([]byte(clientID))
	return hex.EncodeToString(sum[:16])
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b64.EncodeToString(b)
}
