package sse

import (
	"encoding/json"
	"strings"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// ToolCallAcc accumulates one tool call's arguments across stream deltas.
// OpenAI chunk deltas carry an `index` per tool call and only send the id/name
// on the first fragment, so grouping is keyed by index (§7.1: "工具参数增量必须
// 按调用 ID 分组累加，结束时再解析/记录完整参数").
type ToolCallAcc struct {
	Index int
	ID    string
	Type  string
	Name  string
	Args  strings.Builder
}

// Arguments returns the fully accumulated JSON arguments string.
func (t *ToolCallAcc) Arguments() string { return t.Args.String() }

// StreamState is the per-request streaming accumulator described in §7.2. It
// operates on the canonical OpenAI chat.completion.chunk framing, which is the
// normalized intermediate representation every client-protocol renderer reads.
type StreamState struct {
	ResponseID string
	Model      string
	Created    int64

	Text strings.Builder

	toolByIndex map[int]*ToolCallAcc
	toolOrder   []*ToolCallAcc

	Usage         *relaymodel.Usage
	UsageReported bool

	// FinishReason is the last non-empty finish_reason observed.
	FinishReason string

	// UpstreamError is populated when the stream carries an error event
	// (§7.1 "上游 error event"); renderers must surface it, not swallow it.
	UpstreamError *relaymodel.Error
	HasError      bool

	// sawDone reports whether an explicit terminal marker was observed.
	sawDone bool
	// sawContent reports whether any content/delta was observed at all.
	sawContent bool
}

// NewState creates an empty accumulator.
func NewState() *StreamState {
	return &StreamState{toolByIndex: make(map[int]*ToolCallAcc)}
}

// Chunk is the minimal, index-aware view of an OpenAI chat.completion.chunk,
// the normalized intermediate every protocol stream renderer reads. It is
// exported so renderers reuse the same framing instead of re-declaring it.
type Chunk struct {
	Id      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role      string `json:"role"`
			Content   any    `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *relaymodel.Usage `json:"usage"`
	Error *relaymodel.Error `json:"error"`

	// Hosted carries one hosted-tool artifact transition — a web search or an MCP
	// call the UPSTREAM ran itself — for client protocols that have a vocabulary
	// for it. It is not part of any wire protocol: it is `json:"-"` so a chunk
	// that came off the wire can never carry one, and the codecs are the only
	// writers. A chunk with Hosted set carries no text or tool-call delta, and a
	// renderer that ignores the field simply never learns the artifact happened.
	Hosted *relaymodel.HostedStreamEvent `json:"-"`
}

// ParseChunk decodes one SSE data payload into a Chunk. Renderers use it to
// access per-chunk deltas; the state accumulator uses the same framing so the
// two views can never disagree.
func ParseChunk(data string) (Chunk, error) {
	var c Chunk
	err := json.Unmarshal([]byte(data), &c)
	return c, err
}

// TextDelta returns the content string carried by this chunk (may be empty).
func (c Chunk) TextDelta() string {
	var b strings.Builder
	for _, ch := range c.Choices {
		b.WriteString(asString(ch.Delta.Content))
	}
	return b.String()
}

// ToolDelta describes one tool-call argument fragment in a chunk.
type ToolDelta struct {
	Index     int
	ID        string
	Type      string
	Name      string
	Arguments string
}

// ToolDeltas returns every tool-call fragment in this chunk, in wire order.
func (c Chunk) ToolDeltas() []ToolDelta {
	var out []ToolDelta
	for _, ch := range c.Choices {
		for _, tc := range ch.Delta.ToolCalls {
			out = append(out, ToolDelta{
				Index:     tc.Index,
				ID:        tc.ID,
				Type:      tc.Type,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
	}
	return out
}

// FinishReasonOf returns the first non-empty finish_reason in the chunk.
func (c Chunk) FinishReasonOf() string {
	for _, ch := range c.Choices {
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			return *ch.FinishReason
		}
	}
	return ""
}

// Absorb feeds one SSE event payload into the state. A [DONE] marker flips the
// terminal flag; an empty-but-parseable chunk is a no-op. Malformed JSON is
// reported so callers can pass the raw line through without corrupting state.
// Chunk.Hosted is deliberately not absorbed: a hosted artifact is not part of
// the text/tool-call state this accumulator summarizes.
func (s *StreamState) Absorb(ev Event) error {
	if ev.IsDone() {
		s.sawDone = true
		return nil
	}
	if strings.TrimSpace(ev.Data) == "" {
		return nil
	}
	var chunk Chunk
	if err := json.Unmarshal([]byte(ev.Data), &chunk); err != nil {
		return err
	}
	if chunk.Error != nil && chunk.Error.Type != "" {
		s.UpstreamError = chunk.Error
		s.HasError = true
		return nil
	}
	if chunk.Id != "" {
		s.ResponseID = chunk.Id
	}
	if chunk.Model != "" {
		s.Model = chunk.Model
	}
	if chunk.Created != 0 {
		s.Created = chunk.Created
	}
	if chunk.Usage != nil {
		u := *chunk.Usage
		u.Source = relaymodel.UsageSourceReported
		s.Usage = &u
		s.UsageReported = true
	}
	for _, choice := range chunk.Choices {
		s.sawContent = true
		if txt := asString(choice.Delta.Content); txt != "" {
			s.Text.WriteString(txt)
		}
		for _, tc := range choice.Delta.ToolCalls {
			acc := s.toolByIndex[tc.Index]
			if acc == nil {
				acc = &ToolCallAcc{Index: tc.Index}
				s.toolByIndex[tc.Index] = acc
				s.toolOrder = append(s.toolOrder, acc)
			}
			if tc.ID != "" {
				acc.ID = tc.ID
			}
			if tc.Type != "" {
				acc.Type = tc.Type
			}
			if tc.Function.Name != "" {
				acc.Name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				acc.Args.WriteString(tc.Function.Arguments)
			}
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			s.FinishReason = *choice.FinishReason
		}
	}
	return nil
}

// ToolCalls returns the grouped tool calls in first-seen index order.
func (s *StreamState) ToolCalls() []*ToolCallAcc { return s.toolOrder } // SawDone reports whether an explicit terminal marker was seen.
func (s *StreamState) SawDone() bool             { return s.sawDone }

// SawContent reports whether any delta/content was observed.
func (s *StreamState) SawContent() bool { return s.sawContent }

// TextValue returns the accumulated assistant text.
func (s *StreamState) TextValue() string { return s.Text.String() }

// ResponseTextForUsage returns text plus every tool-call argument, so token
// estimation from a streamed response does not undercount tool payloads (the
// legacy handler only counted visible text).
func (s *StreamState) ResponseTextForUsage() string {
	var b strings.Builder
	b.WriteString(s.Text.String())
	for _, t := range s.toolOrder {
		b.WriteString(t.Arguments())
	}
	return b.String()
}

// TerminalStatus decides the usage_record status for a finished stream
// (§7.2/§7.3):
//   - upstream error event          -> error
//   - no terminal marker & aborted  -> partial (client disconnect / net break)
//   - otherwise                      -> success
func (s *StreamState) TerminalStatus(aborted bool) string {
	if s.HasError {
		return relaymodel.UsageStatusError
	}
	if aborted || !s.sawDone {
		return relaymodel.UsageStatusPartial
	}
	return relaymodel.UsageStatusSuccess
}

// FinalizeUsage produces the normalized usage for a stream per §7.2:
//   - reported usage wins;
//   - else estimated via counter(prompt + accumulated response text);
//   - if counter is nil (estimation disallowed) and nothing was reported,
//     return nil usage (status-only record). Never fabricate a zero cache hit.
func (s *StreamState) FinalizeUsage(promptTokens int, modelName string, counter func(text, model string) int) *relaymodel.Usage {
	if s.UsageReported && s.Usage != nil {
		u := *s.Usage
		relaymodel.NormalizeUsage(&u)
		return &u
	}
	if counter != nil {
		completion := counter(s.ResponseTextForUsage(), modelName)
		u := relaymodel.Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completion,
			TotalTokens:      promptTokens + completion,
			Source:           relaymodel.UsageSourceEstimated,
		}
		relaymodel.NormalizeUsage(&u)
		return &u
	}
	return nil
}
