// Package toolerr defines the error a tool reports to the model.
//
// Every failure a caller can act on carries three parts: what failed, why it
// failed underneath, and what to try next. The MCP layer serialises these into
// the {"error": {"message", "why", "hint"}} envelope.
package toolerr

// Error is a failure meant for the model. Message, Why and Hint must never
// contain a token, a token URL or a password.
type Error struct {
	Message string `json:"message"`
	Why     string `json:"why,omitempty"`
	Hint    string `json:"hint,omitempty"`
}

// New builds an Error. Empty why and hint are omitted from the output.
func New(message, why, hint string) *Error {
	return &Error{Message: message, Why: why, Hint: hint}
}

func (e *Error) Error() string {
	if e.Why == "" {
		return e.Message
	}
	return e.Message + ": " + e.Why
}
