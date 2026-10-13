package timetable

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yorukot/nycu/internal/toolerr"
)

func assertJSON(t *testing.T, got any, want string) {
	t.Helper()
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var g, w any
	_ = json.Unmarshal(raw, &g)
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad want JSON: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("got  %s\nwant %s", raw, want)
	}
}

func TestParseCosTime(t *testing.T) {
	cases := []struct {
		raw, want string
	}{
		{"", `[]`},
		{"M56-ED117[GF]", `[{"day":"Mon","periods":"56","start":"13:20","end":"15:10","room":"ED117","campus":"GF"}]`},
		// One chunk packing two days into one room.
		{"M78R34-ED117[GF]", `[
			{"day":"Mon","periods":"78","start":"15:30","end":"17:20","room":"ED117","campus":"GF"},
			{"day":"Thu","periods":"34","start":"10:10","end":"12:00","room":"ED117","campus":"GF"}]`},
		{"W5-EC015[GF], F9ab-", `[
			{"day":"Wed","periods":"5","start":"13:20","end":"14:10","room":"EC015","campus":"GF"},
			{"day":"Fri","periods":"9ab","start":"17:30","end":"20:20","room":null,"campus":null}]`},
		// TBA must stay undated instead of reading T as Tuesday.
		{"TBA", `[{"day":null,"periods":"TBA","start":null,"end":null,"room":null,"campus":null}]`},
		{"-Online", `[{"day":null,"periods":"","start":null,"end":null,"room":"Online","campus":null}]`},
		{"M1-A[B]extra", `[{"day":"Mon","periods":"1","start":"08:00","end":"08:50","room":null,"campus":null}]`},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) { assertJSON(t, ParseCosTime(tc.raw), tc.want) })
	}
}

const courseList = `{
  "dep-b": {
    "dep_id": "B", "dep_cname": "資工系", "dep_ename": "CS",
    "1": {
      "1151_535702": {"acy": "115", "sem": "1", "cos_id": "535702", "cos_code": "CSIC10001", "cos_cname": "計算機概論", "cos_ename": "",
        "teacher": "王小明", "cos_credit": "3.00", "cos_hours": "3", "cos_type": "必修", "cos_time": "T56-EC015[GF]",
        "num_limit": "60", "memo": "<b>Bring</b> laptop&amp;charger"},
      "1151_100001": {"acy": "115", "sem": "1", "cos_id": "100001", "cos_cname": "Z", "cos_credit": "", "num_limit": "", "cos_time": ""}
    }
  },
  "dep-a": {
    "dep_cname": "電機系",
    "2": {"1151_535702": {"acy": "115", "sem": "1", "cos_id": "535702", "cos_cname": "計算機概論"}},
    "costype": {"ignored": {}},
    "language": []
  }
}`

func TestParseCoursesKeepsOrderAndMergesDepartments(t *testing.T) {
	assertJSON(t, ParseCourses([]byte(courseList)), `[
		{"acysem":"1151","cos_id":"535702","cos_code":"CSIC10001","name":"計算機概論","name_en":null,"teacher":"王小明",
		 "credit":3,"hours":3,"type":"必修","time":"T56-EC015[GF]",
		 "slots":[{"day":"Tue","periods":"56","start":"13:20","end":"15:10","room":"EC015","campus":"GF"}],
		 "departments":["資工系","電機系"],"limit":60,"memo":"Bring laptop&charger"},
		{"acysem":"1151","cos_id":"100001","cos_code":null,"name":"Z","name_en":null,"teacher":null,
		 "credit":null,"hours":null,"type":null,"time":"","slots":[],"departments":["資工系"],"limit":null,"memo":""}]`)
}

func TestParseCoursesToleratesOtherShapes(t *testing.T) {
	for _, raw := range []string{`[]`, `"error"`, `{}`, `{"x": 1}`} {
		if got := ParseCourses([]byte(raw)); len(got) != 0 {
			t.Fatalf("%s: got %v", raw, got)
		}
	}
}

func TestSplitAcysem(t *testing.T) {
	for _, ok := range []string{"1151", "1142", "99X", "114X"} {
		if _, _, err := splitAcysem(ok); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "11", "11513", "1153"} {
		_, _, err := splitAcysem(bad)
		var te *toolerr.Error
		if !errors.As(err, &te) || te.Message != "bad acysem" {
			t.Fatalf("%s: got %v", bad, err)
		}
	}
}

// fakeTimetable answers get_acysem and get_cos_list by cos_id.
func fakeTimetable(t *testing.T, byID map[string]string) (*Client, *atomic.Int32) {
	var acysemCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("r") {
		case "main/get_acysem":
			acysemCalls.Add(1)
			_, _ = w.Write([]byte(`[{"T":"1151"},{"T":"114X"}]`))
		case "main/get_cos_list":
			_ = r.ParseForm()
			if r.PostForm.Get("m_acy") != "115" || r.PostForm.Get("m_sem") != "1" {
				t.Errorf("semester fields = %v", r.PostForm)
			}
			switch r.PostForm.Get("m_option") {
			case "cos_id":
				body, ok := byID[r.PostForm.Get("m_cos_id")]
				if !ok {
					body = `[]`
				}
				_, _ = w.Write([]byte(body))
			case "crsname":
				if r.PostForm.Get("m_crsname") == "bad" {
					_, _ = w.Write([]byte(`"查詢條件錯誤"`))
					return
				}
				_, _ = w.Write([]byte(courseList))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return NewClient(server.URL+"/", server.Client()), &acysemCalls
}

func TestLookupReportsMissingIDs(t *testing.T) {
	one := `{"d":{"dep_cname":"資工系","1":{"k":{"acy":"115","sem":"1","cos_id":"535702","cos_cname":"計概"}}}}`
	c, acysemCalls := fakeTimetable(t, map[string]string{"535702": one})
	rows, meta, err := c.Lookup(context.Background(), []string{"999999", "535702"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].CosID != "535702" {
		t.Fatalf("rows = %+v", rows)
	}
	assertJSON(t, meta, `{"count":1,"acysem":"1151","missing":["999999"]}`)

	// The semester list is cached.
	if _, _, err := c.Lookup(context.Background(), []string{"535702"}, ""); err != nil {
		t.Fatal(err)
	}
	if acysemCalls.Load() != 1 {
		t.Fatalf("get_acysem called %d times", acysemCalls.Load())
	}
}

func TestLookupCapsIDs(t *testing.T) {
	c := NewClient("http://unused.invalid/", nil)
	_, _, err := c.Lookup(context.Background(), make([]string, MaxLookupIDs+1), "1151")
	var te *toolerr.Error
	if !errors.As(err, &te) || !strings.HasPrefix(te.Message, "too many cos_ids") {
		t.Fatalf("got %v", err)
	}
}

func TestSearchReportsRejectedQuery(t *testing.T) {
	c, _ := fakeTimetable(t, nil)
	courses, meta, err := c.Search(context.Background(), "計概", ByName, "1151")
	if err != nil || len(courses) != 2 {
		t.Fatalf("search: %v %v", courses, err)
	}
	assertJSON(t, meta, `{"count":2,"acysem":"1151","query":"計概","by":"name"}`)

	_, _, err = c.Search(context.Background(), "bad", ByName, "1151")
	var te *toolerr.Error
	if !errors.As(err, &te) || te.Message != "timetable rejected the query" || te.Why != "查詢條件錯誤" {
		t.Fatalf("got %v", err)
	}
}

func TestPeriodTableKeepsWeekdayOrder(t *testing.T) {
	raw, _ := json.Marshal(PeriodTable())
	if !strings.Contains(string(raw), `"days":{"M":"Mon","T":"Tue","W":"Wed","R":"Thu","F":"Fri","S":"Sat","U":"Sun"}`) {
		t.Fatalf("got %s", raw)
	}
	if len(PeriodTable().Periods) != 16 {
		t.Fatal("period table changed")
	}
}
