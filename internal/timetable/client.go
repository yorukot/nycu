// Package timetable reads the public NYCU course timetable
// (timetable.nycu.edu.tw), which needs no login.
//
// cos_id is the same identifier E3 uses in its course shortname: in
// "1151.535702" the part before the dot is the acysem and the part after is the
// cos_id, so a lookup chains directly off the E3 course list.
package timetable

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yorukot/nycu/internal/jsonx"
	"github.com/yorukot/nycu/internal/toolerr"
)

const (
	DefaultBase = "https://timetable.nycu.edu.tw/"
	// MaxLookupIDs caps one lookup call.
	MaxLookupIDs = 20

	userAgent        = "nycu-mcp/1.0"
	lookupWorkers    = 4
	semesterCacheTTL = time.Hour
	maxResponseBytes = 32 << 20
	unreachableHint  = "check network or https://timetable.nycu.edu.tw"
	acysemHint       = "use a code from timetable_list_semesters, e.g. 1151 or 99X"
)

// Client calls the timetable API.
type Client struct {
	base string
	http *http.Client
	now  func() time.Time

	mu          sync.Mutex
	semesters   []any
	semestersAt time.Time
}

// NewClient builds a Client. An empty base uses DefaultBase; a nil httpClient
// uses a 30-second timeout.
func NewClient(base string, httpClient *http.Client) *Client {
	if base == "" {
		base = DefaultBase
	}
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
		}
	}
	return &Client{base: base, http: httpClient, now: time.Now}
}

// call runs one API route and returns the raw JSON body.
func (c *Client) call(ctx context.Context, route string, form url.Values) ([]byte, error) {
	endpoint := c.base + "?r=" + route
	method, body := http.MethodGet, io.Reader(nil)
	if form != nil {
		method, body = http.MethodPost, strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, toolerr.New("timetable unreachable", "could not build the request", unreachableHint)
	}
	req.Header.Set("User-Agent", userAgent)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, toolerr.New("timetable unreachable", transportReason(err), unreachableHint)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, toolerr.New("timetable unreachable", transportReason(err), unreachableHint)
	}
	switch {
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return nil, toolerr.New("timetable rejected the query", resp.Status,
			"check acysem against timetable_list_semesters and the search value")
	case resp.StatusCode >= 500:
		return nil, toolerr.New("timetable unreachable", resp.Status, unreachableHint)
	}
	if _, err := jsonx.Decode(raw); err != nil {
		return nil, toolerr.New("timetable: non-JSON response", preview(raw),
			"check the acysem / search value; the API returns plain text on a malformed query")
	}
	return raw, nil
}

func transportReason(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return "request timed out"
		}
		return urlErr.Err.Error()
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

// Semesters lists semester codes, newest first. The list is cached for an hour.
func (c *Client) Semesters(ctx context.Context) ([]any, error) {
	c.mu.Lock()
	if c.semesters != nil && c.now().Sub(c.semestersAt) < semesterCacheTTL {
		cached := c.semesters
		c.mu.Unlock()
		return cached, nil
	}
	c.mu.Unlock()

	raw, err := c.call(ctx, "main/get_acysem", nil)
	if err != nil {
		return nil, err
	}
	decoded, _ := jsonx.Decode(raw)
	semesters := []any{}
	for _, entry := range jsonx.Arr(decoded) {
		if code := jsonx.Get(jsonx.Obj(entry), "T"); code != nil {
			semesters = append(semesters, code)
		}
	}

	c.mu.Lock()
	c.semesters, c.semestersAt = semesters, c.now()
	c.mu.Unlock()
	return semesters, nil
}

// resolveAcysem returns acysem, or the latest semester when it is empty.
func (c *Client) resolveAcysem(ctx context.Context, acysem string) (string, error) {
	if acysem != "" {
		return acysem, nil
	}
	semesters, err := c.Semesters(ctx)
	if err != nil {
		return "", err
	}
	if len(semesters) == 0 {
		return "", toolerr.New("no semesters available", "get_acysem returned an empty list", "pass acysem explicitly")
	}
	return jsonx.Str(semesters[0]), nil
}

// splitAcysem splits "1151" into ("115", "1"). The semester is 1, 2 or X (summer).
func splitAcysem(acysem string) (string, string, error) {
	if len(acysem) != 3 && len(acysem) != 4 {
		return "", "", toolerr.New("bad acysem", fmt.Sprintf("%q is not 3 or 4 characters", acysem), acysemHint)
	}
	acy, sem := acysem[:len(acysem)-1], acysem[len(acysem)-1:]
	if sem != "1" && sem != "2" && sem != "X" {
		return "", "", toolerr.New("bad acysem", fmt.Sprintf("%q has semester %q, expected 1, 2, or X", acysem, sem), acysemHint)
	}
	return acy, sem, nil
}

// SearchBy names the field a search matches.
type SearchBy string

const (
	ByName    SearchBy = "name"
	ByTeacher SearchBy = "teacher"
	ByCode    SearchBy = "code"
)

var searchOption = map[SearchBy]string{ByName: "crsname", ByTeacher: "teaname", ByCode: "cos_code"}

var optionField = map[string]string{"crsname": "m_crsname", "teaname": "m_teaname", "cos_id": "m_cos_id", "cos_code": "m_cos_code"}

func (c *Client) query(ctx context.Context, acysem, option, value string) ([]Course, error) {
	acy, sem, err := splitAcysem(acysem)
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"m_acy": {acy}, "m_sem": {sem}, "m_acyend": {acy}, "m_semend": {sem},
		"m_dep_uid": {"**"}, "m_group": {"**"}, "m_grade": {"**"}, "m_class": {"**"},
		"m_option":  {option},
		"m_crsname": {"**"}, "m_teaname": {"**"}, "m_cos_id": {"**"}, "m_cos_code": {"**"},
		"m_crstime": {"**"}, "m_crsoutline": {"**"}, "m_costype": {"**"}, "m_selcampus": {"**"},
	}
	form.Set(optionField[option], value)
	raw, err := c.call(ctx, "main/get_cos_list", form)
	if err != nil {
		return nil, err
	}
	if decoded, _ := jsonx.Decode(raw); decoded != nil {
		if msg, isString := decoded.(string); isString {
			return nil, toolerr.New("timetable rejected the query", msg, "check the acysem or search value")
		}
	}
	return ParseCourses(raw), nil
}

// Search finds courses by name, teacher or course code in one semester
// (the latest when acysem is empty).
func (c *Client) Search(ctx context.Context, query string, by SearchBy, acysem string) ([]Course, map[string]any, error) {
	if by == "" {
		by = ByName
	}
	option, ok := searchOption[by]
	if !ok {
		return nil, nil, toolerr.New("bad search field", fmt.Sprintf("%q is not name, teacher or code", by), "")
	}
	sem, err := c.resolveAcysem(ctx, acysem)
	if err != nil {
		return nil, nil, err
	}
	courses, err := c.query(ctx, sem, option, query)
	if err != nil {
		return nil, nil, err
	}
	return courses, map[string]any{"count": len(courses), "acysem": sem, "query": query, "by": string(by)}, nil
}

// Lookup fetches specific courses by cos_id. IDs not offered that semester are
// listed in metadata.missing instead of failing the call.
func (c *Client) Lookup(ctx context.Context, cosIDs []string, acysem string) ([]Course, map[string]any, error) {
	if len(cosIDs) > MaxLookupIDs {
		return nil, nil, toolerr.New(fmt.Sprintf("too many cos_ids (%d)", len(cosIDs)),
			fmt.Sprintf("lookup accepts at most %d ids per call", MaxLookupIDs), "split the ids across multiple lookup calls")
	}
	sem, err := c.resolveAcysem(ctx, acysem)
	if err != nil {
		return nil, nil, err
	}

	results := make([][]Course, len(cosIDs))
	errs := make([]error, len(cosIDs))
	var wg sync.WaitGroup
	slots := make(chan struct{}, lookupWorkers)
	for i, id := range cosIDs {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			results[i], errs[i] = c.query(ctx, sem, "cos_id", id)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, nil, err
		}
	}

	rows := []Course{}
	index := map[string]int{}
	missing := []string{}
	for i, id := range cosIDs {
		if len(results[i]) == 0 {
			missing = append(missing, id)
			continue
		}
		for _, course := range results[i] {
			key := jsonx.Str(course.CosID)
			if at, seen := index[key]; seen {
				rows[at] = course
				continue
			}
			index[key] = len(rows)
			rows = append(rows, course)
		}
	}
	return rows, map[string]any{"count": len(rows), "acysem": sem, "missing": missing}, nil
}
