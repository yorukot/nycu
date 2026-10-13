package oauth

import (
	"html/template"
	"net/http"
	"net/url"
)

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>NYCU · Authorize</title>
<style>
:root { color-scheme: light dark; --bg: #f6f6f4; --card: #fff; --fg: #1d1d1b; --muted: #6b6b66; --line: #deded9; --accent: #1f5fbf; --error: #b3261e; }
@media (prefers-color-scheme: dark) { :root { --bg: #161615; --card: #20201f; --fg: #ededea; --muted: #a3a39d; --line: #3a3a37; --accent: #7aa7ff; --error: #ff8a80; } }
* { box-sizing: border-box; }
body { margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 16px; background: var(--bg); color: var(--fg); font: 16px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; }
main { width: 100%; max-width: 400px; background: var(--card); border: 1px solid var(--line); border-radius: 12px; padding: 28px; }
h1 { font-size: 20px; margin: 0 0 4px; }
p { margin: 0 0 16px; color: var(--muted); }
dl { margin: 0 0 20px; display: grid; grid-template-columns: auto 1fr; gap: 4px 12px; font-size: 14px; }
dt { color: var(--muted); }
dd { margin: 0; overflow-wrap: anywhere; }
label { display: block; font-size: 14px; margin-bottom: 6px; }
input[type=password] { width: 100%; padding: 10px 12px; font: inherit; color: inherit; background: transparent; border: 1px solid var(--line); border-radius: 8px; }
input[type=password]:focus { outline: 2px solid var(--accent); outline-offset: 1px; }
.error { color: var(--error); font-size: 14px; margin: 10px 0 0; }
.actions { display: flex; gap: 8px; margin-top: 20px; }
button { flex: 1; padding: 10px; font: inherit; border-radius: 8px; border: 1px solid var(--line); background: transparent; color: inherit; cursor: pointer; }
button.primary { background: var(--accent); border-color: var(--accent); color: #fff; }
@media (prefers-color-scheme: dark) { button.primary { color: #10121a; } }
</style>
</head>
<body>
<main>
{{if .Fatal}}
<h1>Cannot authorize</h1>
<p>{{.Fatal}}</p>
{{else}}
<h1>Connect to NYCU</h1>
<p>An app asks to read your E3 coursework and the NYCU timetable through this server.</p>
<dl>
<dt>App</dt><dd>{{if .ClientName}}{{.ClientName}}{{else}}Unnamed client{{end}}</dd>
<dt>Returns to</dt><dd>{{.RedirectHost}}</dd>
</dl>
<form method="post" action="/oauth/authorize">
{{range $key, $value := .Hidden}}<input type="hidden" name="{{$key}}" value="{{$value}}">
{{end}}
<label for="password">Owner password</label>
<input id="password" name="password" type="password" autocomplete="current-password" autofocus required>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<div class="actions">
<button type="submit" name="decision" value="deny" formnovalidate>Deny</button>
<button type="submit" name="decision" value="allow" class="primary">Authorize</button>
</div>
</form>
{{end}}
</main>
</body>
</html>
`))

type pageData struct {
	Fatal        string
	ClientName   string
	RedirectHost string
	Hidden       map[string]string
	Error        string
}

func setPageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func renderAuthorizePage(w http.ResponseWriter, status int, req authRequest, errMsg string) {
	host := req.RedirectURI
	if u, err := url.Parse(req.RedirectURI); err == nil && u.Host != "" {
		host = u.Scheme + "://" + u.Host
	}
	hidden := map[string]string{
		"response_type":         "code",
		"client_id":             req.ClientID,
		"redirect_uri":          req.RedirectURI,
		"state":                 req.State,
		"code_challenge":        req.CodeChallenge,
		"code_challenge_method": "S256",
		"scope":                 req.Scope,
		"resource":              req.Resource,
	}
	setPageHeaders(w)
	w.WriteHeader(status)
	_ = pageTemplate.Execute(w, pageData{ClientName: req.ClientName, RedirectHost: host, Hidden: hidden, Error: errMsg})
}

func renderErrorPage(w http.ResponseWriter, status int, message string) {
	setPageHeaders(w)
	w.WriteHeader(status)
	_ = pageTemplate.Execute(w, pageData{Fatal: message})
}
