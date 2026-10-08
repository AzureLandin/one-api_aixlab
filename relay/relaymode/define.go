package relaymode

const (
	Unknown = iota
	ChatCompletions
	Completions
	Embeddings
	Moderations
	ImagesGenerations
	Edits
	AudioSpeech
	AudioTranscription
	AudioTranslation
	// Proxy is a special relay mode for proxying requests to custom upstream
	Proxy
	// Responses is the OpenAI Responses API (/v1/responses) client protocol
	// entry (implementation doc §6.4).
	Responses
	// AnthropicMessages is the Anthropic Messages API (/v1/messages) client
	// protocol entry (implementation doc §6.5).
	AnthropicMessages
)
