package model

type Tool struct {
	Id   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"` // when splicing claude tools stream messages, it is empty
	// Index is the per-choice position of one tool call inside a streamed
	// message. OpenAI stream deltas carry it so a client can tell parallel tool
	// calls apart while their argument fragments arrive interleaved; requests
	// never set it, so it stays out of the request wire format.
	Index    *int     `json:"index,omitempty"`
	Function Function `json:"function"`
}

type Function struct {
	Description string `json:"description,omitempty"`
	Name        string `json:"name,omitempty"`       // when splicing claude tools stream messages, it is empty
	Parameters  any    `json:"parameters,omitempty"` // request
	Arguments   any    `json:"arguments,omitempty"`  // response
	// Strict asks the upstream to validate the generated arguments against the
	// declared schema. It has to survive the request re-marshal in
	// relay/controller/text.go, otherwise a strict function schema silently
	// degrades to a best-effort one.
	Strict *bool `json:"strict,omitempty"`
}
