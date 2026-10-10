package model

import "encoding/json"

type ResponseFormat struct {
	Type       string      `json:"type,omitempty"`
	JsonSchema *JSONSchema `json:"json_schema,omitempty"`
}

type JSONSchema struct {
	Description string                 `json:"description,omitempty"`
	Name        string                 `json:"name"`
	Schema      map[string]interface{} `json:"schema,omitempty"`
	Strict      *bool                  `json:"strict,omitempty"`
}

type Audio struct {
	Voice  string `json:"voice,omitempty"`
	Format string `json:"format,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// WebSearchOptions is the Chat Completions hosted-web-search toggle. Only the
// two sub-fields every chat upstream understands are modelled; the
// Claude-only knobs (max_uses, domain filters) are reported as losses at the
// point they are dropped rather than smuggled in here.
type WebSearchOptions struct {
	SearchContextSize string          `json:"search_context_size,omitempty"`
	UserLocation      json.RawMessage `json:"user_location,omitempty"`
}

type GeneralOpenAIRequest struct {
	// https://platform.openai.com/docs/api-reference/chat/create
	Messages            []Message       `json:"messages,omitempty"`
	Model               string          `json:"model,omitempty"`
	Store               *bool           `json:"store,omitempty"`
	ReasoningEffort     *string         `json:"reasoning_effort,omitempty"`
	Metadata            any             `json:"metadata,omitempty"`
	FrequencyPenalty    *float64        `json:"frequency_penalty,omitempty"`
	LogitBias           any             `json:"logit_bias,omitempty"`
	Logprobs            *bool           `json:"logprobs,omitempty"`
	TopLogprobs         *int            `json:"top_logprobs,omitempty"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
	N                   int             `json:"n,omitempty"`
	Modalities          []string        `json:"modalities,omitempty"`
	Prediction          any             `json:"prediction,omitempty"`
	Audio               *Audio          `json:"audio,omitempty"`
	PresencePenalty     *float64        `json:"presence_penalty,omitempty"`
	ResponseFormat      *ResponseFormat `json:"response_format,omitempty"`
	Seed                float64         `json:"seed,omitempty"`
	ServiceTier         *string         `json:"service_tier,omitempty"`
	Stop                any             `json:"stop,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
	StreamOptions       *StreamOptions  `json:"stream_options,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	TopK                int             `json:"top_k,omitempty"`
	Tools               []Tool          `json:"tools,omitempty"`
	ToolChoice          any             `json:"tool_choice,omitempty"`
	// WebSearchOptions is Chat Completions' hosted-web-search toggle. A chat
	// client's own value round-trips through json.Marshal untouched; the field
	// is also where a hosted web_search declaration that arrived on a client
	// protocol with no chat equivalent (Claude's web_search_20250305, Responses'
	// {"type":"web_search"}) is carried to a chat upstream. Chat has no hosted
	// ARTIFACT vocabulary, so this is the whole of what that direction can say.
	WebSearchOptions *WebSearchOptions `json:"web_search_options,omitempty"`
	ParallelTooCalls *bool             `json:"parallel_tool_calls,omitempty"`
	User             string            `json:"user,omitempty"`
	FunctionCall     any               `json:"function_call,omitempty"`
	Functions        any               `json:"functions,omitempty"`
	// https://platform.openai.com/docs/api-reference/embeddings/create
	Input          any    `json:"input,omitempty"`
	EncodingFormat string `json:"encoding_format,omitempty"`
	Dimensions     int    `json:"dimensions,omitempty"`
	// https://platform.openai.com/docs/api-reference/images/create
	Prompt  any     `json:"prompt,omitempty"`
	Quality *string `json:"quality,omitempty"`
	Size    string  `json:"size,omitempty"`
	Style   *string `json:"style,omitempty"`
	// Others
	Instruction string `json:"instruction,omitempty"`
	NumCtx      int    `json:"num_ctx,omitempty"`

	// Hosted carries the hosted-tool declarations, replayed history and the
	// forced tool choice that no OpenAI field can hold, in the neutral form the
	// upstream codec translates from. It is `json:"-"`: it must never reach an
	// upstream body, and it is read by the codec's RequestBody, not by the wire.
	Hosted *HostedSet `json:"-"`
}

func (r GeneralOpenAIRequest) ParseInput() []string {
	if r.Input == nil {
		return nil
	}
	var input []string
	switch r.Input.(type) {
	case string:
		input = []string{r.Input.(string)}
	case []any:
		input = make([]string, 0, len(r.Input.([]any)))
		for _, item := range r.Input.([]any) {
			if str, ok := item.(string); ok {
				input = append(input, str)
			}
		}
	}
	return input
}
