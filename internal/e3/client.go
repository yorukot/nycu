// Package e3 reads NYCU E3 (Moodle) through the Moodle Web Services REST API.
package e3

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yorukot/nycu/internal/jsonx"
	"github.com/yorukot/nycu/internal/toolerr"
)

const (
	DefaultBase    = "https://e3p.nycu.edu.tw"
	DefaultService = "moodle_mobile_app"

	userAgent = "nycu-mcp/1.0"
	// Bounds the N+1 fan-out (one call per course, assignment or quiz).
	parallelism = 4
	// Caps a Web Services response; a full-semester course outline is far below this.
	maxResponseBytes = 32 << 20
)

const tokenHint = "run `nycu e3-token` locally and update E3_TOKEN on the server"

// Client calls one Moodle site with one Web Services token.
type Client struct {
	base             string
	token            string
	http             *http.Client
	maxDownloadBytes int64
	now              func() time.Time

	mu     sync.Mutex
	userID int64
}

// Options configures a Client. Zero values pick the defaults.
type Options struct {
	Base             string
	Token            string
	UserID           int64
	MaxDownloadBytes int64
	HTTPClient       *http.Client
	Now              func() time.Time
}

// NewClient builds a Client. UserID may be zero; it is then looked up once from
// the site info and cached.
func NewClient(opts Options) *Client {
	c := &Client{
		base:             strings.TrimRight(opts.Base, "/"),
		token:            opts.Token,
		userID:           opts.UserID,
		http:             opts.HTTPClient,
		maxDownloadBytes: opts.MaxDownloadBytes,
		now:              opts.Now,
	}
	if c.base == "" {
		c.base = DefaultBase
	}
	if c.http == nil {
		c.http = &http.Client{
			Timeout:   60 * time.Second,
			Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
		}
	}
	if c.maxDownloadBytes <= 0 {
		c.maxDownloadBytes = 10 << 20
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c
}

// Base is the Moodle site root, without a trailing slash.
func (c *Client) Base() string { return c.base }

// Token is the Web Services token. Callers use it only to redact output.
func (c *Client) Token() string { return c.token }

// Params are Web Services arguments. Slices become Moodle's key[0]=a&key[1]=b
// form, bools become 1/0 and nil values are dropped.
type Params map[string]any

func (p Params) encode(form url.Values) {
	for key, value := range p {
		switch v := value.(type) {
		case nil:
		case bool:
			if v {
				form.Set(key, "1")
			} else {
				form.Set(key, "0")
			}
		case []int64:
			for i, item := range v {
				form.Set(fmt.Sprintf("%s[%d]", key, i), strconv.FormatInt(item, 10))
			}
		case []string:
			for i, item := range v {
				form.Set(fmt.Sprintf("%s[%d]", key, i), item)
			}
		default:
			form.Set(key, fmt.Sprint(v))
		}
	}
}

// Call runs one Web Services function and returns the decoded response.
func (c *Client) Call(ctx context.Context, function string, params Params) (any, error) {
	form := url.Values{}
	params.encode(form)
	form.Set("wstoken", c.token)
	form.Set("wsfunction", function)
	form.Set("moodlewsrestformat", "json")

	result, err := c.postForm(ctx, c.base+"/webservice/rest/server.php", form)
	if err != nil {
		return nil, err
	}
	if m := jsonx.Obj(result); m != nil {
		if _, isException := m["exception"]; isException {
			code := jsonx.Str(m["errorcode"])
			if code == "" {
				code = "?"
			}
			message := jsonx.Str(m["message"])
			if code == "invalidtoken" || code == "accessexception" {
				return nil, toolerr.New("invalid or expired token", message, tokenHint)
			}
			return nil, toolerr.New("e3p WS error: "+code, message,
				fmt.Sprintf("'%s' was rejected; check the id arguments, or that you are enrolled in that course", function))
		}
	}
	return result, nil
}

// postForm posts a form and decodes the JSON reply. The token travels in the
// body, never in the URL, so the URL is safe to report.
func (c *Client) postForm(ctx context.Context, endpoint string, form url.Values) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, toolerr.New("e3p network error", "could not build the request", "check E3_BASE")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, toolerr.New("e3p network error", networkReason(err), "check connectivity / base URL")
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, toolerr.New("e3p network error", networkReason(err), "check connectivity / base URL")
	}
	if resp.StatusCode >= 400 {
		return nil, toolerr.New("e3p HTTP error", resp.Status, endpoint)
	}
	value, err := jsonx.Decode(raw)
	if err != nil {
		return nil, toolerr.New("e3p: non-JSON response", preview(raw),
			"usually an E3 login/maintenance page; run `e3p_whoami` to check the token")
	}
	return value, nil
}

// UserID returns the authenticated user's id, looking it up once when it was
// not configured. A failed lookup is retried on the next call.
func (c *Client) UserID(ctx context.Context) (int64, error) {
	c.mu.Lock()
	id := c.userID
	c.mu.Unlock()
	if id != 0 {
		return id, nil
	}
	info, err := c.Call(ctx, "core_webservice_get_site_info", nil)
	if err != nil {
		return 0, err
	}
	id, ok := jsonx.Int(jsonx.Get(jsonx.Obj(info), "userid"))
	if !ok || id == 0 {
		return 0, toolerr.New("missing userid", "core_webservice_get_site_info returned no userid", "set E3_USERID on the server")
	}
	c.mu.Lock()
	c.userID = id
	c.mu.Unlock()
	return id, nil
}

// networkReason describes a transport failure without its URL. *url.Error
// embeds the full request URL, which for downloads carries the token.
func networkReason(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return "request timed out"
		}
		err = urlErr.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "request canceled"
	}
	return err.Error()
}

func preview(raw []byte) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 200 {
		raw = raw[:200]
	}
	return strings.ToValidUTF8(string(raw), "�")
}

// forEach runs fn for 0..n-1 with bounded parallelism. The first failure
// cancels the calls still running and is the error returned; the errors those
// canceled calls report are discarded.
func forEach(ctx context.Context, n int, fn func(ctx context.Context, i int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	sem := make(chan struct{}, parallelism)
	for i := range n {
		sem <- struct{}{}
		if ctx.Err() != nil {
			<-sem
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(ctx, i); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr == nil && ctx.Err() != nil {
		// The caller's context ended; report that rather than partial results.
		return toolerr.New("e3p request canceled", networkReason(ctx.Err()), "")
	}
	return firstErr
}
