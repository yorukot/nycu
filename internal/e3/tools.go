package e3

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/yorukot/nycu/internal/jsonx"
)

// Metadata is context about a result, returned next to the data.
type Metadata map[string]any

const day = 86400

// ── whoami ───────────────────────────────────────────────────────

type Profile struct {
	UserID   any `json:"userid"`
	Username any `json:"username"`
	Fullname any `json:"fullname"`
	SiteURL  any `json:"siteurl"`
	Lang     any `json:"lang"`
	Release  any `json:"release"`
}

// Whoami returns the authenticated user and site info.
func (c *Client) Whoami(ctx context.Context) (Profile, error) {
	res, err := c.Call(ctx, "core_webservice_get_site_info", nil)
	if err != nil {
		return Profile{}, err
	}
	info := jsonx.Obj(res)
	return Profile{
		UserID:   info["userid"],
		Username: info["username"],
		Fullname: info["fullname"],
		SiteURL:  info["siteurl"],
		Lang:     info["lang"],
		Release:  info["release"],
	}, nil
}

// ── courses ──────────────────────────────────────────────────────

type Course struct {
	ID        any `json:"id"`
	Shortname any `json:"shortname"`
	Fullname  any `json:"fullname"`
	StartDate any `json:"startdate"`
	Progress  any `json:"progress"`
	// Moodle counts every active enrolment, so teachers and TAs are included.
	Enrolled any `json:"enrolled"`
}

func (c *Client) enrolledCourses(ctx context.Context) ([]map[string]any, error) {
	userID, err := c.UserID(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.Call(ctx, "core_enrol_get_users_courses", Params{"userid": userID})
	if err != nil {
		return nil, err
	}
	var courses []map[string]any
	for _, item := range jsonx.Arr(res) {
		if m := jsonx.Obj(item); m != nil {
			courses = append(courses, m)
		}
	}
	return courses, nil
}

// visible reports whether Moodle shows the course; a missing flag means visible.
func visible(course map[string]any) bool {
	v, ok := course["visible"]
	return !ok || jsonx.Truthy(v)
}

// Courses lists enrolled courses, newest start date first.
func (c *Client) Courses(ctx context.Context, showHidden bool) ([]Course, Metadata, error) {
	courses, err := c.enrolledCourses(ctx)
	if err != nil {
		return nil, nil, err
	}
	rows := []Course{}
	for _, course := range courses {
		if !showHidden && !visible(course) {
			continue
		}
		rows = append(rows, Course{
			ID:        course["id"],
			Shortname: course["shortname"],
			Fullname:  course["fullname"],
			StartDate: course["startdate"],
			Progress:  course["progress"],
			Enrolled:  course["enrolledusercount"],
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return jsonx.Num(rows[i].StartDate) > jsonx.Num(rows[j].StartDate) })
	return rows, Metadata{"count": len(rows)}, nil
}

// courseIDs resolves an optional course filter. With no filter it returns every
// enrolled course, or only the visible ones when onlyVisible is set.
func (c *Client) courseIDs(ctx context.Context, courseID int64, onlyVisible bool) ([]int64, error) {
	if courseID != 0 {
		return []int64{courseID}, nil
	}
	courses, err := c.enrolledCourses(ctx)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for _, course := range courses {
		if onlyVisible && !visible(course) {
			continue
		}
		if id, ok := jsonx.Int(course["id"]); ok {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// ── assignments ──────────────────────────────────────────────────

type Attachment struct {
	Name any `json:"name"`
	URL  any `json:"url"`
}

func attachments(files any) []Attachment {
	out := []Attachment{}
	for _, item := range jsonx.Arr(files) {
		f := jsonx.Obj(item)
		out = append(out, Attachment{Name: jsonx.Get(f, "filename"), URL: jsonx.Get(f, "fileurl")})
	}
	return out
}

type Assignment struct {
	CourseID                 any          `json:"courseid"`
	CourseShortname          any          `json:"course_shortname"`
	AssignID                 any          `json:"assignid"`
	CMID                     any          `json:"cmid"`
	Name                     any          `json:"name"`
	DueDate                  any          `json:"duedate"`
	CutoffDate               any          `json:"cutoffdate"`
	AllowSubmissionsFromDate any          `json:"allowsubmissionsfromdate"`
	Intro                    string       `json:"intro"`
	Attachments              []Attachment `json:"attachments"`
	*AssignmentStatus
}

type AssignmentStatus struct {
	SubmissionStatus any `json:"submission_status"`
	GradingStatus    any `json:"grading_status"`
	TimeModified     any `json:"timemodified"`
}

// Assignments lists assignments of one course, or of every enrolled course when
// courseID is zero. withStatus adds one submission-status call per assignment.
func (c *Client) Assignments(ctx context.Context, courseID int64, withStatus bool) ([]Assignment, Metadata, error) {
	ids, err := c.courseIDs(ctx, courseID, false)
	if err != nil {
		return nil, nil, err
	}
	res, err := c.Call(ctx, "mod_assign_get_assignments", Params{"courseids": ids})
	if err != nil {
		return nil, nil, err
	}
	items := []Assignment{}
	for _, courseItem := range jsonx.Arr(jsonx.Get(jsonx.Obj(res), "courses")) {
		course := jsonx.Obj(courseItem)
		for _, assignItem := range jsonx.Arr(course["assignments"]) {
			a := jsonx.Obj(assignItem)
			items = append(items, Assignment{
				CourseID:                 course["id"],
				CourseShortname:          course["shortname"],
				AssignID:                 a["id"],
				CMID:                     a["cmid"],
				Name:                     a["name"],
				DueDate:                  jsonx.OrNil(a["duedate"]),
				CutoffDate:               jsonx.OrNil(a["cutoffdate"]),
				AllowSubmissionsFromDate: jsonx.OrNil(a["allowsubmissionsfromdate"]),
				Intro:                    stripHTML(jsonx.Str(a["intro"])),
				Attachments:              attachments(a["introattachments"]),
			})
		}
	}

	if withStatus {
		userID, err := c.UserID(ctx)
		if err != nil {
			return nil, nil, err
		}
		err = forEach(ctx, len(items), func(ctx context.Context, i int) error {
			s, err := c.Call(ctx, "mod_assign_get_submission_status", Params{"assignid": items[i].AssignID, "userid": userID})
			if err != nil {
				return err
			}
			last := jsonx.Obj(jsonx.Get(jsonx.Obj(s), "lastattempt"))
			sub := jsonx.Obj(jsonx.Get(last, "submission"))
			items[i].AssignmentStatus = &AssignmentStatus{
				SubmissionStatus: jsonx.Get(sub, "status"),
				GradingStatus:    gradingStatus(last, sub),
				TimeModified:     jsonx.Get(sub, "timemodified"),
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}

	sort.SliceStable(items, func(i, j int) bool { return jsonx.Num(items[i].DueDate) < jsonx.Num(items[j].DueDate) })
	return items, Metadata{"count": len(items), "with_status": withStatus}, nil
}

// gradingStatus reads the grading state, which Moodle reports on the attempt
// rather than on the submission.
func gradingStatus(last, sub map[string]any) any {
	return jsonx.First(jsonx.Get(last, "gradingstatus"), jsonx.Get(sub, "gradingstatus"))
}

// ── due ──────────────────────────────────────────────────────────

type DueEvent struct {
	Name       any `json:"name"`
	TimeSort   any `json:"timesort"`
	Course     any `json:"course"`
	CourseID   any `json:"courseid"`
	URL        any `json:"url"`
	ModuleName any `json:"modulename"`
	Instance   any `json:"instance"`
}

// Due lists upcoming action events (deadlines, quizzes, unsubmitted items).
func (c *Client) Due(ctx context.Context, days, limit int) ([]DueEvent, Metadata, error) {
	now := c.now().Unix()
	res, err := c.Call(ctx, "core_calendar_get_action_events_by_timesort", Params{
		"timesortfrom": now,
		"timesortto":   now + int64(days)*day,
		"limitnum":     limit,
	})
	if err != nil {
		return nil, nil, err
	}
	rows := []DueEvent{}
	for _, item := range jsonx.Arr(jsonx.Get(jsonx.Obj(res), "events")) {
		e := jsonx.Obj(item)
		course := jsonx.Obj(e["course"])
		rows = append(rows, DueEvent{
			Name:       e["name"],
			TimeSort:   e["timesort"],
			Course:     jsonx.Get(course, "shortname"),
			CourseID:   jsonx.Get(course, "id"),
			URL:        e["url"],
			ModuleName: e["modulename"],
			Instance:   e["instance"],
		})
	}
	return rows, Metadata{"count": len(rows), "days": days}, nil
}

// ── submission ───────────────────────────────────────────────────

type SubmittedFile struct {
	Name any `json:"name"`
	Size any `json:"size"`
	URL  any `json:"url"`
}

type Submission struct {
	AssignID      int64           `json:"assignid"`
	Status        any             `json:"status"`
	GradingStatus any             `json:"grading_status"`
	TimeCreated   any             `json:"timecreated"`
	TimeModified  any             `json:"timemodified"`
	CanEdit       any             `json:"can_edit"`
	CanSubmit     any             `json:"can_submit"`
	Graded        any             `json:"graded"`
	Files         []SubmittedFile `json:"files"`
	Text          *string         `json:"text"`
	Grade         any             `json:"grade"`
}

// Submission returns your submission state for one assignment.
func (c *Client) Submission(ctx context.Context, assignID int64) (Submission, error) {
	userID, err := c.UserID(ctx)
	if err != nil {
		return Submission{}, err
	}
	res, err := c.Call(ctx, "mod_assign_get_submission_status", Params{"assignid": assignID, "userid": userID})
	if err != nil {
		return Submission{}, err
	}
	s := jsonx.Obj(res)
	last := jsonx.Obj(jsonx.Get(s, "lastattempt"))
	sub := jsonx.Obj(jsonx.Get(last, "submission"))

	files := []SubmittedFile{}
	var text *string
	for _, pluginItem := range jsonx.Arr(jsonx.Get(sub, "plugins")) {
		plugin := jsonx.Obj(pluginItem)
		switch jsonx.Str(plugin["type"]) {
		case "file":
			for _, area := range jsonx.Arr(plugin["fileareas"]) {
				for _, fileItem := range jsonx.Arr(jsonx.Get(jsonx.Obj(area), "files")) {
					f := jsonx.Obj(fileItem)
					files = append(files, SubmittedFile{Name: f["filename"], Size: f["filesize"], URL: f["fileurl"]})
				}
			}
		case "onlinetext":
			for _, area := range jsonx.Arr(plugin["editorfields"]) {
				if raw := jsonx.Str(jsonx.Get(jsonx.Obj(area), "text")); raw != "" {
					stripped := stripHTML(raw)
					text = &stripped
				}
			}
		}
	}

	feedback := jsonx.Obj(jsonx.Get(s, "feedback"))
	return Submission{
		AssignID:      assignID,
		Status:        jsonx.Get(sub, "status"),
		GradingStatus: gradingStatus(last, sub),
		TimeCreated:   jsonx.Get(sub, "timecreated"),
		TimeModified:  jsonx.Get(sub, "timemodified"),
		CanEdit:       jsonx.Get(last, "canedit"),
		CanSubmit:     jsonx.Get(last, "cansubmit"),
		Graded:        jsonx.Get(last, "graded"),
		Files:         files,
		Text:          text,
		Grade:         jsonx.Get(jsonx.Obj(jsonx.Get(feedback, "grade")), "grade"),
	}, nil
}

// ── grades ───────────────────────────────────────────────────────

type GradeItem struct {
	CourseID   any    `json:"courseid"`
	Item       any    `json:"item"`
	ItemType   any    `json:"itemtype"`
	Grade      any    `json:"grade"`
	Max        any    `json:"max"`
	Percentage any    `json:"percentage"`
	Feedback   string `json:"feedback"`
}

// Grades lists gradebook items of one course, or of every enrolled course.
func (c *Client) Grades(ctx context.Context, courseID int64) ([]GradeItem, Metadata, error) {
	userID, err := c.UserID(ctx)
	if err != nil {
		return nil, nil, err
	}
	ids, err := c.courseIDs(ctx, courseID, false)
	if err != nil {
		return nil, nil, err
	}
	perCourse := make([][]GradeItem, len(ids))
	err = forEach(ctx, len(ids), func(ctx context.Context, i int) error {
		res, err := c.Call(ctx, "gradereport_user_get_grade_items", Params{"courseid": ids[i], "userid": userID})
		if err != nil {
			return err
		}
		for _, courseItem := range jsonx.Arr(jsonx.Get(jsonx.Obj(res), "usergrades")) {
			course := jsonx.Obj(courseItem)
			for _, gradeItem := range jsonx.Arr(course["gradeitems"]) {
				item := jsonx.Obj(gradeItem)
				perCourse[i] = append(perCourse[i], GradeItem{
					CourseID:   course["courseid"],
					Item:       jsonx.First(item["itemname"], item["itemtype"]),
					ItemType:   item["itemtype"],
					Grade:      item["graderaw"],
					Max:        item["grademax"],
					Percentage: item["percentageformatted"],
					Feedback:   stripHTML(jsonx.Str(item["feedback"])),
				})
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	out := []GradeItem{}
	for _, items := range perCourse {
		out = append(out, items...)
	}
	return out, Metadata{"count": len(out)}, nil
}

// ── content ──────────────────────────────────────────────────────

type ModuleFile struct {
	Name     any `json:"name"`
	Size     any `json:"size"`
	MIMEType any `json:"mimetype"`
	URL      any `json:"url"`
}

type Module struct {
	Section  any          `json:"section"`
	ModName  any          `json:"modname"`
	Name     any          `json:"name"`
	CMID     any          `json:"cmid"`
	Instance any          `json:"instance"`
	URL      any          `json:"url"`
	Files    []ModuleFile `json:"files"`
}

func (c *Client) courseContents(ctx context.Context, courseID int64) ([]map[string]any, error) {
	res, err := c.Call(ctx, "core_course_get_contents", Params{"courseid": courseID})
	if err != nil {
		return nil, err
	}
	var sections []map[string]any
	for _, item := range jsonx.Arr(res) {
		if m := jsonx.Obj(item); m != nil {
			sections = append(sections, m)
		}
	}
	return sections, nil
}

// Content returns a course outline: every activity with its section, plus the
// pluginfile URLs of the files it holds (input to Download).
func (c *Client) Content(ctx context.Context, courseID int64) ([]Module, Metadata, error) {
	sections, err := c.courseContents(ctx, courseID)
	if err != nil {
		return nil, nil, err
	}
	out := []Module{}
	for _, sec := range sections {
		for _, item := range jsonx.Arr(sec["modules"]) {
			m := jsonx.Obj(item)
			files := []ModuleFile{}
			for _, contentItem := range jsonx.Arr(m["contents"]) {
				f := jsonx.Obj(contentItem)
				if jsonx.Str(f["type"]) != "file" {
					continue
				}
				files = append(files, ModuleFile{Name: f["filename"], Size: f["filesize"], MIMEType: f["mimetype"], URL: f["fileurl"]})
			}
			out = append(out, Module{
				Section:  sec["name"],
				ModName:  m["modname"],
				Name:     m["name"],
				CMID:     m["id"],
				Instance: m["instance"],
				URL:      m["url"],
				Files:    files,
			})
		}
	}
	return out, Metadata{"count": len(out), "courseid": courseID}, nil
}

// ── announcements ────────────────────────────────────────────────

type Announcement struct {
	CourseID     any          `json:"courseid"`
	ForumID      any          `json:"forumid"`
	DiscussionID any          `json:"discussionid"`
	Subject      any          `json:"subject"`
	Author       any          `json:"author"`
	Created      any          `json:"created"`
	Pinned       bool         `json:"pinned"`
	Message      string       `json:"message"`
	Attachments  []Attachment `json:"attachments"`
	URL          string       `json:"url"`
}

// Announcements lists recent posts of each course's news forum, newest first.
func (c *Client) Announcements(ctx context.Context, courseID int64, days, limit int) ([]Announcement, Metadata, error) {
	ids, err := c.courseIDs(ctx, courseID, true)
	if err != nil {
		return nil, nil, err
	}
	rows := []Announcement{}
	if len(ids) == 0 {
		return rows, Metadata{"count": 0, "days": days}, nil
	}
	res, err := c.Call(ctx, "mod_forum_get_forums_by_courses", Params{"courseids": ids})
	if err != nil {
		return nil, nil, err
	}
	var forums []map[string]any
	for _, item := range jsonx.Arr(res) {
		if f := jsonx.Obj(item); f != nil && jsonx.Str(f["type"]) == "news" {
			forums = append(forums, f)
		}
	}

	since := c.now().Unix() - int64(days)*day
	perForum := make([][]Announcement, len(forums))
	err = forEach(ctx, len(forums), func(ctx context.Context, i int) error {
		f := forums[i]
		res, err := c.Call(ctx, "mod_forum_get_forum_discussions", Params{"forumid": f["id"], "sortorder": 1, "page": 0, "perpage": limit})
		if err != nil {
			return err
		}
		for _, item := range jsonx.Arr(jsonx.Get(jsonx.Obj(res), "discussions")) {
			x := jsonx.Obj(item)
			// Pinned posts sort first regardless of age, so filter rather than stop early.
			posted := jsonx.First(x["created"], x["timemodified"], 0)
			if jsonx.Num(posted) < float64(since) {
				continue
			}
			perForum[i] = append(perForum[i], Announcement{
				CourseID:     f["course"],
				ForumID:      f["id"],
				DiscussionID: x["discussion"],
				Subject:      jsonx.First(x["subject"], x["name"]),
				Author:       x["userfullname"],
				Created:      posted,
				Pinned:       jsonx.Truthy(x["pinned"]),
				Message:      htmlToText(jsonx.Str(x["message"])),
				Attachments:  attachments(x["attachments"]),
				URL:          fmt.Sprintf("%s/mod/forum/discuss.php?d=%s", c.base, jsonx.Str(x["discussion"])),
			})
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	for _, posts := range perForum {
		rows = append(rows, posts...)
	}
	sort.SliceStable(rows, func(i, j int) bool { return jsonx.Num(rows[i].Created) > jsonx.Num(rows[j].Created) })
	return rows, Metadata{"count": len(rows), "days": days}, nil
}

// ── discussion ───────────────────────────────────────────────────

type Post struct {
	PostID      any          `json:"postid"`
	ParentID    any          `json:"parentid"`
	Subject     any          `json:"subject"`
	Author      any          `json:"author"`
	Created     any          `json:"created"`
	Message     string       `json:"message"`
	Attachments []Attachment `json:"attachments"`
}

// Discussion returns every post of one forum discussion, oldest first.
func (c *Client) Discussion(ctx context.Context, discussionID int64) ([]Post, Metadata, error) {
	res, err := c.Call(ctx, "mod_forum_get_discussion_posts", Params{"discussionid": discussionID, "sortby": "created", "sortdirection": "ASC"})
	if err != nil {
		return nil, nil, err
	}
	rows := []Post{}
	for _, item := range jsonx.Arr(jsonx.Get(jsonx.Obj(res), "posts")) {
		x := jsonx.Obj(item)
		if jsonx.Truthy(x["isdeleted"]) {
			continue
		}
		rows = append(rows, Post{
			PostID:      x["id"],
			ParentID:    x["parentid"],
			Subject:     x["subject"],
			Author:      jsonx.Get(jsonx.Obj(x["author"]), "fullname"),
			Created:     x["timecreated"],
			Message:     htmlToText(jsonx.Str(x["message"])),
			Attachments: attachments(x["attachments"]),
		})
	}
	return rows, Metadata{"count": len(rows), "discussionid": discussionID}, nil
}

// ── updates ──────────────────────────────────────────────────────

type Update struct {
	CourseID    int64    `json:"courseid"`
	CMID        any      `json:"cmid"`
	ModName     any      `json:"modname"`
	Name        any      `json:"name"`
	URL         any      `json:"url"`
	Changes     []string `json:"changes"`
	TimeUpdated any      `json:"timeupdated"`
}

// Updates lists activities added or changed in the last days, newest first.
func (c *Client) Updates(ctx context.Context, courseID int64, days int) ([]Update, Metadata, error) {
	ids, err := c.courseIDs(ctx, courseID, true)
	if err != nil {
		return nil, nil, err
	}
	since := c.now().Unix() - int64(days)*day
	perCourse := make([][]Update, len(ids))
	err = forEach(ctx, len(ids), func(ctx context.Context, i int) error {
		cid := ids[i]
		res, err := c.Call(ctx, "core_course_get_updates_since", Params{"courseid": cid, "since": since})
		if err != nil {
			return err
		}
		var changed []map[string]any
		for _, item := range jsonx.Arr(jsonx.Get(jsonx.Obj(res), "instances")) {
			inst := jsonx.Obj(item)
			if jsonx.Str(inst["contextlevel"]) == "module" && jsonx.Truthy(inst["updates"]) {
				changed = append(changed, inst)
			}
		}
		if len(changed) == 0 {
			return nil
		}
		// The update feed only names module ids; the outline maps them to titles and URLs.
		sections, err := c.courseContents(ctx, cid)
		if err != nil {
			return err
		}
		modules := map[string]map[string]any{}
		for _, sec := range sections {
			for _, item := range jsonx.Arr(sec["modules"]) {
				m := jsonx.Obj(item)
				modules[jsonx.Str(m["id"])] = m
			}
		}
		for _, inst := range changed {
			m := modules[jsonx.Str(inst["id"])]
			changes := []string{}
			var latest float64
			for _, u := range jsonx.Arr(inst["updates"]) {
				update := jsonx.Obj(u)
				changes = append(changes, jsonx.Str(update["name"]))
				latest = max(latest, jsonx.Num(update["timeupdated"]))
			}
			var timeUpdated any
			if latest != 0 {
				timeUpdated = int64(latest)
			}
			perCourse[i] = append(perCourse[i], Update{
				CourseID:    cid,
				CMID:        inst["id"],
				ModName:     jsonx.Get(m, "modname"),
				Name:        jsonx.Get(m, "name"),
				URL:         jsonx.Get(m, "url"),
				Changes:     changes,
				TimeUpdated: timeUpdated,
			})
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	rows := []Update{}
	for _, updates := range perCourse {
		rows = append(rows, updates...)
	}
	sort.SliceStable(rows, func(i, j int) bool { return jsonx.Num(rows[i].TimeUpdated) > jsonx.Num(rows[j].TimeUpdated) })
	return rows, Metadata{"count": len(rows), "days": days}, nil
}

// ── notifications ────────────────────────────────────────────────

type Notification struct {
	ID        any    `json:"id"`
	Subject   any    `json:"subject"`
	Text      string `json:"text"`
	Name      any    `json:"name"`
	URL       any    `json:"url"`
	EventType any    `json:"eventtype"`
	Component any    `json:"component"`
	Created   any    `json:"created"`
	Read      bool   `json:"read"`
}

// Moodle appends this preferences link label to every message; it carries no content.
const preferencesLabel = "調整您的通知偏好設定"

// Notifications lists the notification bell, newest first. Reading does not
// mark anything as read.
func (c *Client) Notifications(ctx context.Context, limit int, onlyUnread bool) ([]Notification, Metadata, error) {
	userID, err := c.UserID(ctx)
	if err != nil {
		return nil, nil, err
	}
	res, err := c.Call(ctx, "message_popup_get_popup_notifications", Params{"useridto": userID, "newestfirst": 1, "limit": limit, "offset": 0})
	if err != nil {
		return nil, nil, err
	}
	body := jsonx.Obj(res)
	rows := []Notification{}
	for _, item := range jsonx.Arr(jsonx.Get(body, "notifications")) {
		n := jsonx.Obj(item)
		if jsonx.Truthy(n["deleted"]) {
			continue
		}
		read := jsonx.Truthy(n["read"])
		if onlyUnread && read {
			continue
		}
		text := htmlToText(jsonx.Str(jsonx.First(n["smallmessage"], n["text"], "")))
		rows = append(rows, Notification{
			ID:        n["id"],
			Subject:   n["subject"],
			Text:      strings.TrimSpace(strings.TrimSuffix(text, preferencesLabel)),
			Name:      n["contexturlname"],
			URL:       n["contexturl"],
			EventType: n["eventtype"],
			Component: n["component"],
			Created:   n["timecreated"],
			Read:      read,
		})
	}
	return rows, Metadata{"count": len(rows), "unreadcount": jsonx.Get(body, "unreadcount")}, nil
}

// ── quizzes ──────────────────────────────────────────────────────

type QuizAttempt struct {
	Attempt    any `json:"attempt"`
	State      any `json:"state"`
	TimeStart  any `json:"timestart"`
	TimeFinish any `json:"timefinish"`
	SumGrades  any `json:"sumgrades"`
}

type Quiz struct {
	CourseID        any            `json:"courseid"`
	QuizID          any            `json:"quizid"`
	CMID            any            `json:"cmid"`
	Name            any            `json:"name"`
	TimeOpen        any            `json:"timeopen"`
	TimeClose       any            `json:"timeclose"`
	TimeLimit       any            `json:"timelimit"`
	AttemptsAllowed any            `json:"attempts_allowed"`
	Grade           any            `json:"grade"`
	Intro           string         `json:"intro"`
	Attempts        *[]QuizAttempt `json:"attempts,omitempty"`
}

// Quizzes lists quizzes of one course, or of every visible enrolled course.
// withStatus adds one attempts call per quiz.
func (c *Client) Quizzes(ctx context.Context, courseID int64, withStatus bool) ([]Quiz, Metadata, error) {
	ids, err := c.courseIDs(ctx, courseID, true)
	if err != nil {
		return nil, nil, err
	}
	rows := []Quiz{}
	meta := Metadata{"with_status": withStatus}
	if len(ids) == 0 {
		meta["count"] = 0
		return rows, meta, nil
	}
	res, err := c.Call(ctx, "mod_quiz_get_quizzes_by_courses", Params{"courseids": ids})
	if err != nil {
		return nil, nil, err
	}
	for _, item := range jsonx.Arr(jsonx.Get(jsonx.Obj(res), "quizzes")) {
		q := jsonx.Obj(item)
		rows = append(rows, Quiz{
			CourseID:        q["course"],
			QuizID:          q["id"],
			CMID:            q["coursemodule"],
			Name:            q["name"],
			TimeOpen:        jsonx.OrNil(q["timeopen"]),
			TimeClose:       jsonx.OrNil(q["timeclose"]),
			TimeLimit:       jsonx.OrNil(q["timelimit"]),
			AttemptsAllowed: jsonx.OrNil(q["attempts"]),
			Grade:           q["grade"],
			Intro:           htmlToText(jsonx.Str(q["intro"])),
		})
	}

	if withStatus {
		err := forEach(ctx, len(rows), func(ctx context.Context, i int) error {
			res, err := c.Call(ctx, "mod_quiz_get_user_attempts", Params{"quizid": rows[i].QuizID, "status": "all"})
			if err != nil {
				return err
			}
			attempts := []QuizAttempt{}
			for _, item := range jsonx.Arr(jsonx.Get(jsonx.Obj(res), "attempts")) {
				x := jsonx.Obj(item)
				attempts = append(attempts, QuizAttempt{
					Attempt:    x["attempt"],
					State:      x["state"],
					TimeStart:  jsonx.OrNil(x["timestart"]),
					TimeFinish: jsonx.OrNil(x["timefinish"]),
					SumGrades:  x["sumgrades"],
				})
			}
			rows[i].Attempts = &attempts
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}

	sort.SliceStable(rows, func(i, j int) bool { return jsonx.Num(rows[i].TimeClose) < jsonx.Num(rows[j].TimeClose) })
	meta["count"] = len(rows)
	return rows, meta, nil
}
