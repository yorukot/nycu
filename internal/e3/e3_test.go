package e3

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yorukot/nycu/internal/toolerr"
)

const testToken = "synthetic-token&value"

var testNow = time.Unix(1_760_000_000, 0)

// fakeMoodle serves Web Services calls from a table keyed by wsfunction.
type fakeMoodle struct {
	t         *testing.T
	server    *httptest.Server
	functions map[string]func(form url.Values) any
	files     http.HandlerFunc
	calls     atomic.Int32
}

func newFakeMoodle(t *testing.T) *fakeMoodle {
	f := &fakeMoodle{t: t, functions: map[string]func(url.Values) any{}}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if strings.Contains(r.URL.Path, "pluginfile.php") {
			if f.files == nil {
				http.NotFound(w, r)
				return
			}
			f.files(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("bad form: %v", err)
		}
		if r.PostForm.Get("wstoken") != testToken {
			t.Errorf("wstoken = %q", r.PostForm.Get("wstoken"))
		}
		fn := r.PostForm.Get("wsfunction")
		handler, ok := f.functions[fn]
		if !ok {
			t.Errorf("unexpected wsfunction %s", fn)
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(handler(r.PostForm))
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeMoodle) client(opts Options) *Client {
	opts.Base = f.server.URL
	opts.Token = testToken
	if opts.UserID == 0 {
		opts.UserID = 123
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = f.server.Client()
	}
	opts.Now = func() time.Time { return testNow }
	return NewClient(opts)
}

// assertJSON compares the JSON form of got against want.
func assertJSON(t *testing.T, got any, want string) {
	t.Helper()
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var g, w any
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad want JSON: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("got  %s\nwant %s", raw, want)
	}
}

func assertNoSecret(t *testing.T, err error) {
	t.Helper()
	var te *toolerr.Error
	if !errors.As(err, &te) {
		t.Fatalf("want *toolerr.Error, got %T %v", err, err)
	}
	for _, s := range []string{te.Message, te.Why, te.Hint, err.Error()} {
		if strings.Contains(s, testToken) || strings.Contains(s, url.QueryEscape(testToken)) || strings.Contains(s, "token=") {
			t.Fatalf("error leaks the token or token URL: %q", s)
		}
	}
}

// ── download ─────────────────────────────────────────────────────

func TestDownloadRejectsForeignOrNonFileURLs(t *testing.T) {
	f := newFakeMoodle(t)
	host := strings.TrimPrefix(f.server.URL, "https://")
	c := f.client(Options{})
	for _, raw := range []string{
		"https://foreign.test/pluginfile.php/file",
		"http://" + host + "/pluginfile.php/file",
		"https://" + host + "/not-a-file",
		"https://user@" + host + "/pluginfile.php/file",
		"https://" + host + ":1/pluginfile.php/file",
		"not a url",
	} {
		_, err := c.Download(context.Background(), raw)
		var te *toolerr.Error
		if !errors.As(err, &te) || te.Message != "invalid download URL" {
			t.Fatalf("%s: got %v", raw, err)
		}
		assertNoSecret(t, err)
	}
	if n := f.calls.Load(); n != 0 {
		t.Fatalf("rejected URLs reached the server %d times", n)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, errors.New("failed " + req.URL.String())
}

func TestDownloadFailureNeverReportsTokenOrURL(t *testing.T) {
	f := newFakeMoodle(t)
	c := f.client(Options{HTTPClient: &http.Client{Transport: failingTransport{}}})
	_, err := c.Download(context.Background(), f.server.URL+"/webservice/pluginfile.php/file")
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Message != "download failed" {
		t.Fatalf("got %v", err)
	}
	assertNoSecret(t, err)
}

func TestDownloadAddsTokenOnceAndKeepsQuery(t *testing.T) {
	f := newFakeMoodle(t)
	f.files = func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if !reflect.DeepEqual(q["token"], []string{testToken}) || q.Get("forcedownload") != "1" {
			t.Errorf("query = %v", q)
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("example file"))
	}
	c := f.client(Options{})
	file, err := c.Download(context.Background(), f.server.URL+"/webservice/pluginfile.php/1/mod_resource/content/0/file.pdf?token=old&forcedownload=1#frag")
	if err != nil {
		t.Fatal(err)
	}
	if string(file.Body) != "example file" || file.Name != "file.pdf" || file.MIMEType != "application/pdf" {
		t.Fatalf("file = %+v", file)
	}
	if strings.Contains(file.URI, "token") || !strings.Contains(file.URI, "forcedownload=1") || strings.Contains(file.URI, "#") {
		t.Fatalf("shown URI = %s", file.URI)
	}
}

func TestDownloadRejectsCrossOriginRedirect(t *testing.T) {
	f := newFakeMoodle(t)
	f.files = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://foreign.test/pluginfile.php/file?token="+url.QueryEscape(testToken), http.StatusFound)
	}
	_, err := f.client(Options{}).Download(context.Background(), f.server.URL+"/pluginfile.php/file")
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Message != "download failed" || !strings.Contains(te.Why, "redirected") {
		t.Fatalf("got %v", err)
	}
	assertNoSecret(t, err)
}

func TestDownloadSizeLimit(t *testing.T) {
	f := newFakeMoodle(t)
	f.files = func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("0123456789A")) }
	_, err := f.client(Options{MaxDownloadBytes: 10}).Download(context.Background(), f.server.URL+"/pluginfile.php/a.txt")
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Message != "file too large" {
		t.Fatalf("got %v", err)
	}

	f.files = func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("0123456789")) }
	file, err := f.client(Options{MaxDownloadBytes: 10}).Download(context.Background(), f.server.URL+"/pluginfile.php/a.txt")
	if err != nil || len(file.Body) != 10 || file.MIMEType != "text/plain" {
		t.Fatalf("at the limit: %+v %v", file, err)
	}
}

// ── web services ─────────────────────────────────────────────────

func TestInvalidTokenMapsToRefreshHint(t *testing.T) {
	f := newFakeMoodle(t)
	f.functions["core_webservice_get_site_info"] = func(url.Values) any {
		return map[string]any{"exception": "moodle_exception", "errorcode": "invalidtoken", "message": "Invalid token - token not found"}
	}
	_, err := f.client(Options{}).Whoami(context.Background())
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Message != "invalid or expired token" || !strings.Contains(te.Hint, "nycu e3-token") {
		t.Fatalf("got %v", err)
	}
}

func TestOtherExceptionNamesTheFunction(t *testing.T) {
	f := newFakeMoodle(t)
	f.functions["core_course_get_contents"] = func(url.Values) any {
		return map[string]any{"exception": "required_capability_exception", "errorcode": "nopermissions", "message": "no"}
	}
	_, _, err := f.client(Options{}).Content(context.Background(), 9)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Message != "e3p WS error: nopermissions" || !strings.Contains(te.Hint, "core_course_get_contents") {
		t.Fatalf("got %v", err)
	}
}

func TestNonJSONResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>maintenance</html>"))
	}))
	defer server.Close()
	c := NewClient(Options{Base: server.URL, Token: testToken, HTTPClient: server.Client()})
	_, err := c.Whoami(context.Background())
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Message != "e3p: non-JSON response" || te.Why != "<html>maintenance</html>" {
		t.Fatalf("got %v", err)
	}
}

func TestParamsUseMoodleArrayForm(t *testing.T) {
	form := url.Values{}
	Params{"courseids": []int64{4, 9}, "status": true, "off": false, "skip": nil, "n": 3}.encode(form)
	want := url.Values{"courseids[0]": {"4"}, "courseids[1]": {"9"}, "status": {"1"}, "off": {"0"}, "n": {"3"}}
	if !reflect.DeepEqual(form, want) {
		t.Fatalf("form = %v", form)
	}
}

func TestUserIDIsLookedUpOnce(t *testing.T) {
	f := newFakeMoodle(t)
	var lookups atomic.Int32
	f.functions["core_webservice_get_site_info"] = func(url.Values) any {
		lookups.Add(1)
		return map[string]any{"userid": 77}
	}
	f.functions["core_enrol_get_users_courses"] = func(form url.Values) any {
		if form.Get("userid") != "77" {
			t.Errorf("userid = %s", form.Get("userid"))
		}
		return []any{}
	}
	c := NewClient(Options{Base: f.server.URL, Token: testToken, HTTPClient: f.server.Client()})
	for range 2 {
		if _, _, err := c.Courses(context.Background(), false); err != nil {
			t.Fatal(err)
		}
	}
	if lookups.Load() != 1 {
		t.Fatalf("site info called %d times", lookups.Load())
	}
}

func TestHTMLToTextKeepsParagraphs(t *testing.T) {
	if got := htmlToText("<p>Hi&nbsp; all</p><p>Line<br>two</p>"); got != "Hi all\nLine\ntwo" {
		t.Fatalf("got %q", got)
	}
	if got := htmlToText("<p>a</p>\n\n\n\n<p>b</p>"); got != "a\n\nb" {
		t.Fatalf("blank runs: got %q", got)
	}
}

func TestCoursesHideInvisibleAndSortNewestFirst(t *testing.T) {
	f := newFakeMoodle(t)
	f.functions["core_enrol_get_users_courses"] = func(url.Values) any {
		return []any{
			map[string]any{"id": 1, "shortname": "old", "fullname": "Old", "startdate": 100, "visible": 1, "enrolledusercount": 30},
			map[string]any{"id": 2, "shortname": "hidden", "fullname": "Hidden", "startdate": 300, "visible": 0},
			map[string]any{"id": 3, "shortname": "new", "fullname": "New", "startdate": 200},
		}
	}
	rows, meta, err := f.client(Options{}).Courses(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, rows, `[
		{"id":3,"shortname":"new","fullname":"New","startdate":200,"progress":null,"enrolled":null},
		{"id":1,"shortname":"old","fullname":"Old","startdate":100,"progress":null,"enrolled":30}]`)
	if meta["count"] != 2 {
		t.Fatalf("meta = %v", meta)
	}
}

func TestAssignmentsWithStatus(t *testing.T) {
	f := newFakeMoodle(t)
	f.functions["mod_assign_get_assignments"] = func(form url.Values) any {
		if form.Get("courseids[0]") != "9" {
			t.Errorf("courseids = %v", form)
		}
		return map[string]any{"courses": []any{map[string]any{"id": 9, "shortname": "C", "assignments": []any{
			map[string]any{"id": 2, "cmid": 20, "name": "Late", "duedate": 500, "cutoffdate": 0, "intro": "<p>Do &amp; submit</p>",
				"introattachments": []any{map[string]any{"filename": "spec.pdf", "fileurl": "https://e3/pluginfile.php/spec.pdf"}}},
			map[string]any{"id": 1, "cmid": 10, "name": "Early", "duedate": 100},
		}}}}
	}
	f.functions["mod_assign_get_submission_status"] = func(form url.Values) any {
		status := map[string]string{"1": "submitted", "2": "new"}[form.Get("assignid")]
		return map[string]any{"lastattempt": map[string]any{"gradingstatus": "notgraded", "submission": map[string]any{"status": status, "timemodified": 42}}}
	}
	rows, meta, err := f.client(Options{}).Assignments(context.Background(), 9, true)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, rows, `[
		{"courseid":9,"course_shortname":"C","assignid":1,"cmid":10,"name":"Early","duedate":100,"cutoffdate":null,"allowsubmissionsfromdate":null,"intro":"","attachments":[],
		 "submission_status":"submitted","grading_status":"notgraded","timemodified":42},
		{"courseid":9,"course_shortname":"C","assignid":2,"cmid":20,"name":"Late","duedate":500,"cutoffdate":null,"allowsubmissionsfromdate":null,"intro":"Do & submit",
		 "attachments":[{"name":"spec.pdf","url":"https://e3/pluginfile.php/spec.pdf"}],
		 "submission_status":"new","grading_status":"notgraded","timemodified":42}]`)
	if meta["with_status"] != true {
		t.Fatalf("meta = %v", meta)
	}

	rows, _, err = f.client(Options{}).Assignments(context.Background(), 9, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rows[0])
	if strings.Contains(string(raw), "submission_status") {
		t.Fatalf("status fields present without status=true: %s", raw)
	}
}

func TestAnnouncementsListRecentNewsPostsOnly(t *testing.T) {
	f := newFakeMoodle(t)
	now := testNow.Unix()
	f.functions["mod_forum_get_forums_by_courses"] = func(url.Values) any {
		return []any{map[string]any{"id": 1, "course": 9, "type": "news"}, map[string]any{"id": 2, "course": 9, "type": "general"}}
	}
	f.functions["mod_forum_get_forum_discussions"] = func(form url.Values) any {
		if form.Get("forumid") != "1" {
			t.Errorf("forumid = %s", form.Get("forumid"))
		}
		return map[string]any{"discussions": []any{
			map[string]any{"discussion": 5, "subject": "old", "created": now - 40*day, "pinned": true, "message": ""},
			map[string]any{"discussion": 6, "subject": "new", "created": now - day, "message": "<p>hello</p>",
				"attachments": []any{map[string]any{"filename": "a.pdf", "fileurl": "https://example.test/webservice/pluginfile.php/a.pdf"}}},
		}}
	}
	c := f.client(Options{})
	rows, _, err := c.Announcements(context.Background(), 9, 30, 20)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, rows, `[{"courseid":9,"forumid":1,"discussionid":6,"subject":"new","author":null,"created":`+
		jsonInt(now-day)+`,"pinned":false,"message":"hello","attachments":[{"name":"a.pdf","url":"https://example.test/webservice/pluginfile.php/a.pdf"}],"url":"`+
		c.Base()+`/mod/forum/discuss.php?d=6"}]`)
}

func jsonInt(n int64) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

func TestUpdatesNameChangedModules(t *testing.T) {
	f := newFakeMoodle(t)
	f.functions["core_course_get_updates_since"] = func(url.Values) any {
		return map[string]any{"instances": []any{
			map[string]any{"contextlevel": "module", "id": 7, "updates": []any{map[string]any{"name": "configuration", "timeupdated": 100}, map[string]any{"name": "contentfiles"}}},
			map[string]any{"contextlevel": "module", "id": 8, "updates": []any{}},
		}}
	}
	f.functions["core_course_get_contents"] = func(url.Values) any {
		return []any{map[string]any{"modules": []any{map[string]any{"id": 7, "modname": "resource", "name": "Slides", "url": "https://example.test/mod/resource/view.php?id=7"}}}}
	}
	rows, _, err := f.client(Options{}).Updates(context.Background(), 9, 7)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, rows, `[{"courseid":9,"cmid":7,"modname":"resource","name":"Slides","url":"https://example.test/mod/resource/view.php?id=7","changes":["configuration","contentfiles"],"timeupdated":100}]`)
}

func TestNotificationsFilterUnreadAndStripBoilerplate(t *testing.T) {
	f := newFakeMoodle(t)
	f.functions["message_popup_get_popup_notifications"] = func(form url.Values) any {
		if form.Get("useridto") != "123" {
			t.Errorf("useridto = %s", form.Get("useridto"))
		}
		return map[string]any{"unreadcount": 1, "notifications": []any{
			map[string]any{"id": 1, "subject": "a", "smallmessage": "<p>Changed.調整您的通知偏好設定</p>", "read": false, "timecreated": 2},
			map[string]any{"id": 2, "subject": "b", "smallmessage": "Old", "read": true, "timecreated": 1},
			map[string]any{"id": 3, "subject": "c", "smallmessage": "Gone", "deleted": true},
		}}
	}
	rows, meta, err := f.client(Options{}).Notifications(context.Background(), 20, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Text != "Changed." {
		t.Fatalf("rows = %+v", rows)
	}
	assertJSON(t, meta, `{"count":1,"unreadcount":1}`)
}

func TestDiscussionSkipsDeletedPosts(t *testing.T) {
	f := newFakeMoodle(t)
	f.functions["mod_forum_get_discussion_posts"] = func(url.Values) any {
		return map[string]any{"posts": []any{
			map[string]any{"id": 1, "parentid": nil, "subject": "Q", "author": map[string]any{"fullname": "A"}, "timecreated": 1, "message": "<p>q</p>"},
			map[string]any{"id": 2, "parentid": 1, "subject": "Re", "author": map[string]any{"fullname": "B"}, "timecreated": 2, "message": "", "isdeleted": true},
		}}
	}
	rows, _, err := f.client(Options{}).Discussion(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, rows, `[{"postid":1,"parentid":null,"subject":"Q","author":"A","created":1,"message":"q","attachments":[]}]`)
}

func TestQuizzesWithAttempts(t *testing.T) {
	f := newFakeMoodle(t)
	f.functions["mod_quiz_get_quizzes_by_courses"] = func(url.Values) any {
		return map[string]any{"quizzes": []any{map[string]any{"id": 3, "course": 9, "coursemodule": 4, "name": "Quiz", "timeclose": 50, "attempts": 2, "grade": 10}}}
	}
	f.functions["mod_quiz_get_user_attempts"] = func(form url.Values) any {
		if form.Get("quizid") != "3" {
			t.Errorf("quizid = %s", form.Get("quizid"))
		}
		return map[string]any{"attempts": []any{map[string]any{"attempt": 1, "state": "finished", "timestart": 10, "timefinish": 20, "sumgrades": 8}}}
	}
	rows, _, err := f.client(Options{}).Quizzes(context.Background(), 9, true)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, rows, `[{"courseid":9,"quizid":3,"cmid":4,"name":"Quiz","timeopen":null,"timeclose":50,"timelimit":null,"attempts_allowed":2,"grade":10,"intro":"",
		"attempts":[{"attempt":1,"state":"finished","timestart":10,"timefinish":20,"sumgrades":8}]}]`)
}

func TestContentListsModuleFiles(t *testing.T) {
	f := newFakeMoodle(t)
	f.functions["core_course_get_contents"] = func(url.Values) any {
		return []any{map[string]any{"name": "Week 1", "modules": []any{
			map[string]any{"id": 7, "modname": "resource", "name": "Slides", "instance": 70, "url": "https://e3/mod/resource/view.php?id=7",
				"contents": []any{
					map[string]any{"type": "file", "filename": "w1.pdf", "filesize": 10, "mimetype": "application/pdf", "fileurl": "https://e3/webservice/pluginfile.php/w1.pdf"},
					map[string]any{"type": "url", "fileurl": "https://elsewhere"},
				}},
			map[string]any{"id": 8, "modname": "forum", "name": "News", "instance": 80},
		}}}
	}
	rows, meta, err := f.client(Options{}).Content(context.Background(), 9)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, rows, `[
		{"section":"Week 1","modname":"resource","name":"Slides","cmid":7,"instance":70,"url":"https://e3/mod/resource/view.php?id=7",
		 "files":[{"name":"w1.pdf","size":10,"mimetype":"application/pdf","url":"https://e3/webservice/pluginfile.php/w1.pdf"}]},
		{"section":"Week 1","modname":"forum","name":"News","cmid":8,"instance":80,"url":null,"files":[]}]`)
	assertJSON(t, meta, `{"count":2,"courseid":9}`)
}

func TestFanOutReportsTheFailure(t *testing.T) {
	f := newFakeMoodle(t)
	f.functions["core_enrol_get_users_courses"] = func(url.Values) any {
		return []any{map[string]any{"id": 1}, map[string]any{"id": 2}, map[string]any{"id": 3}}
	}
	f.functions["gradereport_user_get_grade_items"] = func(form url.Values) any {
		if form.Get("courseid") == "2" {
			return map[string]any{"exception": "x", "errorcode": "notingroup", "message": "no access"}
		}
		return map[string]any{"usergrades": []any{}}
	}
	_, _, err := f.client(Options{}).Grades(context.Background(), 0)
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Message != "e3p WS error: notingroup" {
		t.Fatalf("got %v", err)
	}
}
