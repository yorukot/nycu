package e3

import (
	"context"
	"net/url"

	"github.com/yorukot/nycu/internal/jsonx"
	"github.com/yorukot/nycu/internal/toolerr"
)

// RequestToken exchanges a Moodle username and password for a Web Services
// token through /login/token.php. Only the local `nycu e3-token` command calls
// it; the server never sees the password.
func RequestToken(ctx context.Context, c *Client, username, password, service string) (string, error) {
	if service == "" {
		service = DefaultService
	}
	form := url.Values{"username": {username}, "password": {password}, "service": {service}}
	res, err := c.postForm(ctx, c.base+"/login/token.php", form)
	if err != nil {
		return "", err
	}
	body := jsonx.Obj(res)
	if msg := jsonx.Str(jsonx.Get(body, "error")); msg != "" {
		hint := jsonx.Str(body["errorcode"])
		if hint == "" {
			hint = "if you use NYCU portal SSO, your username here is the student ID (the 9-digit number), not the portal display name"
		}
		return "", toolerr.New("login failed", msg, hint)
	}
	token := jsonx.Str(jsonx.Get(body, "token"))
	if token == "" {
		return "", toolerr.New("login failed", "the reply carried no token", "check that E3 allows the "+service+" service")
	}
	return token, nil
}

// WithToken returns a copy of the client that uses another token.
func (c *Client) WithToken(token string) *Client {
	return NewClient(Options{Base: c.base, Token: token, MaxDownloadBytes: c.maxDownloadBytes, HTTPClient: c.http, Now: c.now})
}
