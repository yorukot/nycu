package mcpserver

import (
	"context"
	"mime"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yorukot/nycu/internal/e3"
	"github.com/yorukot/nycu/internal/truncate"
)

// E3 is the owner's own Moodle account: a bounded private workspace, not the
// open internet, so openWorldHint is false (OpenAI's guidance for private data).
var e3Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPtr(false)}

const (
	defaultDueDays           = 14
	defaultDueLimit          = 50
	defaultAnnouncementDays  = 30
	defaultAnnouncementLimit = 20
	defaultUpdateDays        = 7
	defaultNotificationLimit = 20
)

type coursesInput struct {
	ShowHidden bool `json:"show_hidden,omitempty" jsonschema:"Include hidden/archived courses."`
}

type courseStatusInput struct {
	CourseID int64 `json:"courseid,omitempty" jsonschema:"Course ID. Omit for all courses."`
	Status   bool  `json:"status,omitempty" jsonschema:"Also fetch your submission status or attempts per item."`
}

type dueInput struct {
	Days  int `json:"days,omitempty" jsonschema:"Look-ahead days. Default: 14."`
	Limit int `json:"limit,omitempty" jsonschema:"Maximum events. Default: 50."`
}

type announcementsInput struct {
	CourseID int64 `json:"courseid,omitempty" jsonschema:"Course ID. Omit for all visible courses."`
	Days     int   `json:"days,omitempty" jsonschema:"Look-back days. Default: 30."`
	Limit    int   `json:"limit,omitempty" jsonschema:"Maximum posts per course. Default: 20."`
}

type discussionInput struct {
	DiscussionID int64 `json:"discussionid" jsonschema:"Discussion ID."`
}

type updatesInput struct {
	CourseID int64 `json:"courseid,omitempty" jsonschema:"Course ID. Omit for all visible courses."`
	Days     int   `json:"days,omitempty" jsonschema:"Look-back days. Default: 7."`
}

type notificationsInput struct {
	Limit  int  `json:"limit,omitempty" jsonschema:"Maximum notifications. Default: 20."`
	Unread bool `json:"unread,omitempty" jsonschema:"Only unread notifications."`
}

type submissionInput struct {
	AssignID int64 `json:"assignid" jsonschema:"Assignment ID."`
}

type courseInput struct {
	CourseID int64 `json:"courseid,omitempty" jsonschema:"Course ID. Omit for all courses."`
}

type requiredCourseInput struct {
	CourseID int64 `json:"courseid" jsonschema:"Course ID."`
}

type downloadInput struct {
	URL string `json:"url" jsonschema:"pluginfile.php URL."`
}

func orDefault(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

func (t *tools) registerE3(s *mcp.Server) {
	add(s, t, &mcp.Tool{
		Name:        "e3p_whoami",
		Description: "Show the authenticated E3 user and site info. Cheapest way to check whether the configured token is still valid.",
		Annotations: e3Annotations,
		Meta:        mcp.Meta{"openai/profile": true},
	}, "", func(ctx context.Context, _ noInput) (result, error) {
		profile, err := t.e3.Whoami(ctx)
		if err != nil {
			return result{}, err
		}
		return ok(profile, map[string]any{})
	})

	add(s, t, &mcp.Tool{
		Name:        "e3p_list_courses",
		Description: "List enrolled E3 courses, sorted newest first, with the enrolment count per course (teachers and TAs included). Past semesters are often hidden; pass show_hidden=true when a course seems missing.",
		Annotations: e3Annotations,
	}, "", func(ctx context.Context, in coursesInput) (result, error) {
		return wrap(t.e3.Courses(ctx, in.ShowHidden))
	})

	add(s, t, &mcp.Tool{
		Name:        "e3p_list_assignments",
		Description: "List assignments for one course or all enrolled courses. status=true costs one extra API call per assignment, so scope it with courseid.",
		Annotations: e3Annotations,
	}, "pass courseid to scope to one course, or drop status", func(ctx context.Context, in courseStatusInput) (result, error) {
		return wrap(t.e3.Assignments(ctx, in.CourseID, in.Status))
	})

	add(s, t, &mcp.Tool{
		Name:        "e3p_list_due",
		Description: "List upcoming E3 action events and deadlines. Shows only what E3 itself tracks, so deadlines announced only in class are absent.",
		Annotations: e3Annotations,
	}, "lower days or limit", func(ctx context.Context, in dueInput) (result, error) {
		return wrap(t.e3.Due(ctx, orDefault(in.Days, defaultDueDays), orDefault(in.Limit, defaultDueLimit)))
	})

	add(s, t, &mcp.Tool{
		Name:        "e3p_list_announcements",
		Description: "List recent posts from the announcements forum of one course or all visible enrolled courses, newest first, with the first post's full text and attachment URLs (input to e3p_download_file). Replies are in e3p_get_discussion.",
		Annotations: e3Annotations,
	}, "pass courseid to scope to one course, or lower days", func(ctx context.Context, in announcementsInput) (result, error) {
		return wrap(t.e3.Announcements(ctx, in.CourseID, orDefault(in.Days, defaultAnnouncementDays), orDefault(in.Limit, defaultAnnouncementLimit)))
	})

	add(s, t, &mcp.Tool{
		Name:        "e3p_get_discussion",
		Description: "Get every post in one forum discussion, oldest first: the opening post and all replies with parentid, full text and attachment URLs. discussionid comes from e3p_list_announcements.",
		Annotations: e3Annotations,
	}, "", func(ctx context.Context, in discussionInput) (result, error) {
		return wrap(t.e3.Discussion(ctx, in.DiscussionID))
	})

	add(s, t, &mcp.Tool{
		Name:        "e3p_list_updates",
		Description: "List course activities added or changed in the last N days (new files, edited assignment instructions, new forum posts), newest first. Costs two API calls per course, so scope it with courseid.",
		Annotations: e3Annotations,
	}, "pass courseid to scope to one course, or lower days", func(ctx context.Context, in updatesInput) (result, error) {
		return wrap(t.e3.Updates(ctx, in.CourseID, orDefault(in.Days, defaultUpdateDays)))
	})

	add(s, t, &mcp.Tool{
		Name:        "e3p_list_notifications",
		Description: "List the E3 notification bell (content changes, grading, forum replies, due reminders), newest first, with the unread count. Reading does not mark notifications as read.",
		Annotations: e3Annotations,
	}, "lower limit or pass unread", func(ctx context.Context, in notificationsInput) (result, error) {
		return wrap(t.e3.Notifications(ctx, orDefault(in.Limit, defaultNotificationLimit), in.Unread))
	})

	add(s, t, &mcp.Tool{
		Name:        "e3p_list_quizzes",
		Description: "List quizzes for one course or all visible enrolled courses, with open and close times. status=true costs one extra API call per quiz and adds your attempts.",
		Annotations: e3Annotations,
	}, "pass courseid to scope to one course, or drop status", func(ctx context.Context, in courseStatusInput) (result, error) {
		return wrap(t.e3.Quizzes(ctx, in.CourseID, in.Status))
	})

	add(s, t, &mcp.Tool{
		Name:        "e3p_get_submission",
		Description: "Get detailed submission status for one assignment: status, grading status, submitted files and online text, and the grade once released.",
		Annotations: e3Annotations,
	}, "", func(ctx context.Context, in submissionInput) (result, error) {
		submission, err := t.e3.Submission(ctx, in.AssignID)
		if err != nil {
			return result{}, err
		}
		return ok(submission, map[string]any{})
	})

	add(s, t, &mcp.Tool{
		Name:        "e3p_list_grades",
		Description: "List gradebook items for one course or all enrolled courses. Grades not released by the instructor read as empty rather than missing.",
		Annotations: e3Annotations,
	}, "pass courseid to scope to one course", func(ctx context.Context, in courseInput) (result, error) {
		return wrap(t.e3.Grades(ctx, in.CourseID))
	})

	add(s, t, &mcp.Tool{
		Name:        "e3p_get_content",
		Description: "Get a course outline: every activity with its section, and the files each one holds. The files' pluginfile.php URLs are the input to e3p_download_file. Whole-semester outlines are long and may be shortened.",
		Annotations: e3Annotations,
	}, "read the outline section by section", func(ctx context.Context, in requiredCourseInput) (result, error) {
		return wrap(t.e3.Content(ctx, in.CourseID))
	})

	t.registerDownload(s)
}

// wrap adapts a client call returning (data, metadata, error).
func wrap[T any](data T, metadata e3.Metadata, err error) (result, error) {
	if err != nil {
		return result{}, err
	}
	return ok(data, metadata)
}

// registerDownload adds e3p_download_file, which returns the file itself as an
// embedded resource next to the usual envelope.
func (t *tools) registerDownload(s *mcp.Server) {
	tool := &mcp.Tool{
		Name:         "e3p_download_file",
		Description:  "Download an E3 file by its pluginfile.php URL (from e3p_get_content, e3p_list_announcements or e3p_get_discussion) and return its contents as an embedded resource. Text files come back as text, other files as base64. A plain fetch of the same URL returns a login page instead of the file.",
		Annotations:  e3Annotations,
		OutputSchema: envelopeSchema,
	}
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in downloadInput) (*mcp.CallToolResult, Envelope, error) {
		file, err := t.e3.Download(ctx, in.URL)
		if err != nil {
			return &mcp.CallToolResult{IsError: true}, Envelope{Error: t.toolError(err)}, nil
		}

		resource := &mcp.ResourceContents{URI: file.URI, MIMEType: file.MIMEType}
		var report *truncate.Report
		if isText(file.MIMEType) && utf8.Valid(file.Body) {
			text, r := truncate.Apply(string(file.Body), "open the file on E3 for the full text", truncate.Limits{
				MaxStringChars: t.limits.MaxTotalChars,
				MaxTotalChars:  t.limits.MaxTotalChars,
			})
			resource.Text, report = text.(string), r
			if report != nil {
				report.Fields = []string{"content[0].resource.text"}
			}
		} else {
			resource.Blob = file.Body
		}

		env, err := t.envelope(file.Info(), map[string]any{}, "")
		if err != nil {
			return &mcp.CallToolResult{IsError: true}, Envelope{Error: t.toolError(err)}, nil
		}
		env.Truncation = report
		summary, _ := marshal(env)
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.EmbeddedResource{Resource: resource},
			&mcp.TextContent{Text: string(summary)},
		}}, env, nil
	})
}

func isText(mimeType string) bool {
	mediaType, _, err := mime.ParseMediaType(mimeType)
	if err != nil {
		return false
	}
	if strings.HasPrefix(mediaType, "text/") {
		return true
	}
	switch mediaType {
	case "application/json", "application/xml", "application/javascript", "application/x-sh", "application/x-python", "application/sql":
		return true
	}
	return strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml")
}
