// Package mcpserver exposes the E3 and timetable clients as MCP tools.
//
// Every tool returns the same envelope as structured content:
//
//	success: {"data": ..., "metadata": {...}}
//	failure: {"error": {"message", "why", "hint"}} with isError set
//
// The failure path is part of the output schema on purpose. Clients validate
// structuredContent against the declared schema even when isError is set, so
// an unmodelled error envelope would surface as a protocol error instead of a
// readable message.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yorukot/nycu/internal/e3"
	"github.com/yorukot/nycu/internal/jsonx"
	"github.com/yorukot/nycu/internal/timetable"
	"github.com/yorukot/nycu/internal/toolerr"
	"github.com/yorukot/nycu/internal/truncate"
)

// Instructions are sent to clients on initialize. ChatGPT reads only the first
// 512 characters reliably, so the opening paragraph stands on its own.
const Instructions = `NYCU coursework tools. e3p_* tools read the owner's E3 (Moodle) account: courses, assignments, deadlines, announcements, grades, quizzes, notifications and files. timetable_* tools search the public NYCU timetable for class days, periods and rooms. A cos_id is the part after the dot in an E3 course shortname (1151.535702 is acysem 1151, cos_id 535702). Times are Unix seconds; the campus is in Asia/Taipei.

Files: pass a pluginfile.php URL from e3p_get_content, e3p_list_announcements or e3p_get_discussion to e3p_download_file. Large results are shortened; the _truncation field and the marker in the text say how to narrow the call.`

// Envelope is the structured content of every tool result.
type Envelope struct {
	Data       any              `json:"data,omitempty"`
	Metadata   map[string]any   `json:"metadata,omitempty"`
	Error      *toolerr.Error   `json:"error,omitempty"`
	Truncation *truncate.Report `json:"_truncation,omitempty"`
}

var envelopeSchema = &jsonschema.Schema{
	Type: "object",
	Properties: map[string]*jsonschema.Schema{
		"data":     {Description: "Present on success."},
		"metadata": {Type: "object", Description: "Context about the result, such as counts and the applied defaults."},
		"error": {
			Type:        "object",
			Description: "Present instead of data when the call failed.",
			Properties: map[string]*jsonschema.Schema{
				"message": {Type: "string"},
				"why":     {Type: "string", Description: "What actually went wrong underneath."},
				"hint":    {Type: "string", Description: "The next action to take."},
			},
			Required: []string{"message"},
		},
		"_truncation": {
			Type:        "object",
			Description: "Present only when output was shortened to fit the caller's context budget.",
			Properties: map[string]*jsonschema.Schema{
				"fields":         {Type: "array", Items: &jsonschema.Schema{Type: "string"}, Description: "Paths of the string leaves that were shortened."},
				"original_chars": {Type: "integer", Description: "Serialized size before truncation."},
				"limit":          {Type: "integer", Description: "Per-string character cap applied."},
			},
		},
	},
}

// Options configures the server.
type Options struct {
	E3        *e3.Client
	Timetable *timetable.Client
	Limits    truncate.Limits
	Version   string
}

// New builds the MCP server with all tools registered.
func New(opts Options) *mcp.Server {
	if opts.Limits == (truncate.Limits{}) {
		opts.Limits = truncate.DefaultLimits()
	}
	server := mcp.NewServer(
		&mcp.Implementation{Name: "nycu", Title: "NYCU", Version: opts.Version},
		&mcp.ServerOptions{Instructions: Instructions, Capabilities: &mcp.ServerCapabilities{}},
	)
	t := &tools{e3: opts.E3, timetable: opts.Timetable, limits: opts.Limits}
	if opts.E3 != nil {
		t.secrets = append(t.secrets, opts.E3.Token())
	}
	t.registerE3(server)
	t.registerTimetable(server)
	return server
}

type tools struct {
	e3        *e3.Client
	timetable *timetable.Client
	limits    truncate.Limits
	// secrets are scrubbed from every result as a last line of defence.
	secrets []string
}

// result is what a tool body returns on success.
type result struct {
	data     any
	metadata map[string]any
}

func ok(data any, metadata map[string]any) (result, error) {
	return result{data: data, metadata: metadata}, nil
}

// add registers a tool whose body returns data and metadata. hint is appended
// to the truncation marker and names how to narrow the call.
func add[In any](s *mcp.Server, t *tools, tool *mcp.Tool, hint string, body func(context.Context, In) (result, error)) {
	tool.OutputSchema = envelopeSchema
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Envelope, error) {
		res, err := body(ctx, in)
		if err != nil {
			return &mcp.CallToolResult{IsError: true}, Envelope{Error: t.toolError(err)}, nil
		}
		env, err := t.envelope(res.data, res.metadata, hint)
		if err != nil {
			return &mcp.CallToolResult{IsError: true}, Envelope{Error: t.toolError(err)}, nil
		}
		return nil, env, nil
	})
}

// envelope serialises data and metadata, scrubs secrets and shortens long strings.
func (t *tools) envelope(data any, metadata map[string]any, hint string) (Envelope, error) {
	raw, err := marshal(map[string]any{"data": data, "metadata": metadata})
	if err != nil {
		return Envelope{}, toolerr.New("internal error", "could not encode the result", "")
	}
	for _, secret := range t.secrets {
		if secret != "" {
			raw = bytes.ReplaceAll(raw, []byte(secret), []byte("[redacted]"))
		}
	}
	decoded, err := jsonx.Decode(raw)
	if err != nil {
		return Envelope{}, toolerr.New("internal error", "could not encode the result", "")
	}
	shortened, report := truncate.Apply(decoded, hint, t.limits)
	body := jsonx.Obj(shortened)
	env := Envelope{Data: body["data"], Truncation: report}
	if meta := jsonx.Obj(body["metadata"]); meta != nil {
		env.Metadata = meta
	}
	if env.Data == nil {
		// Keep the "data present on success" contract even for a null payload.
		env.Data = []any{}
	}
	return env, nil
}

// toolError converts any error into the reported shape without leaking secrets.
func (t *tools) toolError(err error) *toolerr.Error {
	var te *toolerr.Error
	if !errors.As(err, &te) {
		te = toolerr.New("internal error", err.Error(), "")
	}
	return &toolerr.Error{Message: t.scrub(te.Message), Why: t.scrub(te.Why), Hint: t.scrub(te.Hint)}
}

func (t *tools) scrub(s string) string {
	for _, secret := range t.secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return s
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func boolPtr(b bool) *bool { return &b }

// inputSchema infers the schema of In and lets the caller refine it.
func inputSchema[In any](refine func(*jsonschema.Schema)) *jsonschema.Schema {
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(err)
	}
	if refine != nil {
		refine(schema)
	}
	return schema
}
