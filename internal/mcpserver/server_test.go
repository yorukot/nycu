package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yorukot/nycu/internal/e3"
	"github.com/yorukot/nycu/internal/timetable"
	"github.com/yorukot/nycu/internal/truncate"
)

const testToken = "synthetic-token-0123456789abcdef"

var wantTools = []string{
	"e3p_download_file", "e3p_get_content", "e3p_get_discussion", "e3p_get_submission", "e3p_list_announcements",
	"e3p_list_assignments", "e3p_list_courses", "e3p_list_due", "e3p_list_grades", "e3p_list_notifications",
	"e3p_list_quizzes", "e3p_list_updates", "e3p_whoami",
	"timetable_get_periods", "timetable_list_semesters", "timetable_lookup_courses", "timetable_search_courses",
}

// fakeE3 answers a few Web Services functions and serves one text and one binary file.
func fakeE3(t *testing.T) *httptest.Server {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/notes.txt"):
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(strings.Repeat("課程筆記 ", 50)))
			return
		case strings.HasSuffix(r.URL.Path, "/slides.pdf"):
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF-1.4 binary"))
			return
		}
		_ = r.ParseForm()
		var reply any
		switch r.PostForm.Get("wsfunction") {
		case "core_webservice_get_site_info":
			// The site URL echoes the token, which must never reach the model.
			reply = map[string]any{"userid": 5, "username": "111550000", "fullname": "Test Student", "siteurl": "https://e3/?token=" + testToken}
		case "core_course_get_contents":
			reply = map[string]any{"exception": "x", "errorcode": "invalidtoken", "message": "Invalid token " + testToken}
		case "core_enrol_get_users_courses":
			reply = []any{map[string]any{"id": 1, "shortname": "1151.535702", "fullname": strings.Repeat("長", 500)}}
		}
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(server.Close)
	return server
}

func connect(t *testing.T, limits truncate.Limits) (*mcp.ClientSession, string) {
	t.Helper()
	e3Server := fakeE3(t)
	server := New(Options{
		E3:        e3.NewClient(e3.Options{Base: e3Server.URL, Token: testToken, UserID: 5, HTTPClient: e3Server.Client()}),
		Timetable: timetable.NewClient("http://unused.invalid/", nil),
		Limits:    limits,
		Version:   "test",
	})
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, e3Server.URL
}

func call(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error %v", name, err)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var structured map[string]any
	if err := json.Unmarshal(raw, &structured); err != nil {
		t.Fatalf("%s: structured content %s", name, raw)
	}
	return res, structured
}

func TestListsExactlyTheToolsWithAnnotations(t *testing.T) {
	session, _ := connect(t, truncate.DefaultLimits())
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || tool.Annotations.OpenWorldHint == nil {
			t.Fatalf("%s: annotations %+v", tool.Name, tool.Annotations)
		}
		if want := strings.HasPrefix(tool.Name, "timetable_"); *tool.Annotations.OpenWorldHint != want {
			t.Fatalf("%s: openWorldHint = %v", tool.Name, *tool.Annotations.OpenWorldHint)
		}
		if tool.OutputSchema == nil || tool.Description == "" {
			t.Fatalf("%s: missing output schema or description", tool.Name)
		}
		if tool.Name == "e3p_whoami" && tool.Meta["openai/profile"] != true {
			t.Fatalf("e3p_whoami: _meta = %v", tool.Meta)
		}
	}
	sort.Strings(names)
	if strings.Join(names, ",") != strings.Join(wantTools, ",") {
		t.Fatalf("tools = %v", names)
	}
}

func TestPeriodsEnvelope(t *testing.T) {
	session, _ := connect(t, truncate.DefaultLimits())
	res, out := call(t, session, "timetable_get_periods", nil)
	if res.IsError {
		t.Fatalf("error: %v", out)
	}
	data := out["data"].(map[string]any)
	if len(data["periods"].([]any)) != 16 || data["days"].(map[string]any)["R"] != "Thu" {
		t.Fatalf("data = %v", data)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, `"periods"`) {
		t.Fatalf("text content = %s", text)
	}
}

func TestSecretsAreScrubbedFromResultsAndErrors(t *testing.T) {
	session, _ := connect(t, truncate.DefaultLimits())
	res, out := call(t, session, "e3p_whoami", nil)
	raw, _ := json.Marshal(res)
	if res.IsError || strings.Contains(string(raw), testToken) || out["data"].(map[string]any)["siteurl"] != "https://e3/?token=[redacted]" {
		t.Fatalf("whoami leaked or failed: %s", raw)
	}

	// The error path must also satisfy the output schema, or the client reports a protocol error.
	res, out = call(t, session, "e3p_get_content", map[string]any{"courseid": 9})
	raw, _ = json.Marshal(res)
	if !res.IsError || strings.Contains(string(raw), testToken) {
		t.Fatalf("content error leaked or succeeded: %s", raw)
	}
	if out["error"].(map[string]any)["message"] != "invalid or expired token" {
		t.Fatalf("error = %v", out["error"])
	}
}

func TestLongStringsAreTruncatedWithReport(t *testing.T) {
	session, _ := connect(t, truncate.Limits{MaxStringChars: 100, MaxTotalChars: 10_000})
	_, out := call(t, session, "e3p_list_courses", nil)
	report, ok := out["_truncation"].(map[string]any)
	if !ok || report["fields"].([]any)[0] != "data[0].fullname" {
		t.Fatalf("_truncation = %v", out["_truncation"])
	}
	name := out["data"].([]any)[0].(map[string]any)["fullname"].(string)
	if !strings.Contains(name, "truncated 400 of 500 chars") {
		t.Fatalf("fullname = %q", name)
	}
}

func TestInputValidationRejectsBadArguments(t *testing.T) {
	session, _ := connect(t, truncate.DefaultLimits())
	for name, args := range map[string]map[string]any{
		"timetable_search_courses": {"query": "x", "by": "room"},
		"timetable_lookup_courses": {"cos_ids": []any{}},
		"e3p_get_discussion":       {},
	} {
		res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err == nil && !res.IsError {
			t.Fatalf("%s accepted %v", name, args)
		}
	}
}

func TestDownloadReturnsEmbeddedResource(t *testing.T) {
	session, base := connect(t, truncate.Limits{MaxStringChars: 20_000, MaxTotalChars: 100})

	res, out := call(t, session, "e3p_download_file", map[string]any{"url": base + "/webservice/pluginfile.php/1/slides.pdf"})
	if res.IsError {
		t.Fatalf("pdf: %v", out)
	}
	resource := res.Content[0].(*mcp.EmbeddedResource).Resource
	if string(resource.Blob) != "%PDF-1.4 binary" || resource.MIMEType != "application/pdf" || strings.Contains(resource.URI, "token") {
		t.Fatalf("pdf resource = %+v", resource)
	}
	if out["data"].(map[string]any)["name"] != "slides.pdf" {
		t.Fatalf("data = %v", out["data"])
	}

	res, out = call(t, session, "e3p_download_file", map[string]any{"url": base + "/webservice/pluginfile.php/1/notes.txt"})
	text := res.Content[0].(*mcp.EmbeddedResource).Resource.Text
	if !strings.HasPrefix(text, "課程筆記") || !strings.Contains(text, "truncated 150 of 250 chars") || out["_truncation"] == nil {
		t.Fatalf("text resource = %q, out = %v", text, out)
	}

	res, out = call(t, session, "e3p_download_file", map[string]any{"url": "https://foreign.example/pluginfile.php/x"})
	if !res.IsError || out["error"].(map[string]any)["message"] != "invalid download URL" {
		t.Fatalf("foreign URL: %v", out)
	}
}
