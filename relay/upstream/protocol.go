// Package upstream names the wire protocol an upstream channel speaks, which is
// a different axis from the client protocol the caller used (§6.6). A channel
// may be reached by any client protocol the gateway accepts, but the body it
// receives and the response it returns are shaped by its own protocol, so the
// relay has to know both to decide whether a request can be forwarded as-is or
// has to be translated.
package upstream

import (
	"github.com/songquanpeng/one-api/relay/apitype"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

// Protocol is the wire protocol an upstream channel speaks.
type Protocol string

const (
	// OpenAIChat is the OpenAI Chat Completions API (/v1/chat/completions).
	OpenAIChat Protocol = "openai-chat"
	// OpenAIResponses is the OpenAI Responses API (/v1/responses).
	OpenAIResponses Protocol = "openai-responses"
	// AnthropicMessages is the Anthropic Messages API (/v1/messages).
	AnthropicMessages Protocol = "anthropic-messages"
	// Compatible is an OpenAI-chat-shaped upstream that must not be used for
	// same-protocol passthrough: some OpenAI-compatible servers accept the chat
	// body but do not faithfully echo back what they were sent.
	Compatible Protocol = "compatible"
	// Unsupported is the zero value: an upstream family this gateway does not
	// translate. Such a channel keeps working on /v1/chat/completions through
	// its own adaptor, and is rejected with a named error on /v1/responses and
	// /v1/messages rather than being guessed at.
	Unsupported Protocol = ""
)

// Resolve returns the wire protocol of a channel. An explicitly configured
// value wins; otherwise the protocol is derived from the api type; an api type
// outside the translated families resolves to Unsupported.
//
// configured is ChannelConfig.UpstreamProtocol and apiType is meta.APIType.
func Resolve(configured string, apiType int) Protocol {
	if configured != "" {
		return Protocol(configured)
	}
	switch apiType {
	case apitype.Anthropic, apitype.AwsClaude:
		// Bedrock's Claude models take the same Messages wire shape.
		return AnthropicMessages
	case apitype.OpenAI:
		return OpenAIChat
	default:
		return Unsupported
	}
}

// configurable lists every protocol name an admin may write into a channel
// config. The strings are persisted in channels.config, so they are a de-facto
// public interface: rename them only with a migration.
var configurable = []Protocol{OpenAIChat, OpenAIResponses, AnthropicMessages, Compatible}

// Valid reports whether name is a protocol an admin may configure. Save-time
// validation uses this so that a typo is rejected instead of silently resolving
// to openai-chat, which would take the translation path and drop the native
// fields (cache_control, thinking, hosted tools) that passthrough preserves.
func Valid(name string) bool {
	for _, p := range configurable {
		if Protocol(name) == p {
			return true
		}
	}
	return false
}

// canServeMatrix lists, per upstream protocol, the client protocols it can
// serve. Every translated family can serve every client protocol — directly
// when the two match, by translation otherwise — so every cell is currently
// true. It is written out rather than collapsed so that a family with narrower
// support can be added without touching the callers. A lookup for an unknown
// upstream protocol (Unsupported) misses the map and yields false.
var canServeMatrix = map[Protocol]map[Protocol]bool{
	OpenAIChat: {
		OpenAIChat: true, OpenAIResponses: true, AnthropicMessages: true,
	},
	OpenAIResponses: {
		OpenAIChat: true, OpenAIResponses: true, AnthropicMessages: true,
	},
	AnthropicMessages: {
		OpenAIChat: true, OpenAIResponses: true, AnthropicMessages: true,
	},
	Compatible: {
		OpenAIChat: true, OpenAIResponses: true, AnthropicMessages: true,
	},
}

// CanServe reports whether an upstream speaking up can serve a client speaking
// client.
func CanServe(client, up Protocol) bool {
	return canServeMatrix[up][client]
}

// Passthrough reports whether a request may be forwarded to the upstream
// without translation. That is exactly the same-protocol case, minus the
// families that must still be translated: Compatible is chat-shaped but not
// passthrough-safe, and Unsupported has no protocol to match.
func Passthrough(client, up Protocol) bool {
	return client == up && up != Compatible && up != Unsupported
}

// ClientProtocolFor maps a relay mode to the client protocol the caller is
// speaking. A mode outside the three protocol entry points yields Unsupported,
// so callers that only make sense for them can refuse explicitly.
func ClientProtocolFor(mode int) Protocol {
	switch mode {
	case relaymode.ChatCompletions:
		return OpenAIChat
	case relaymode.Responses:
		return OpenAIResponses
	case relaymode.AnthropicMessages:
		return AnthropicMessages
	default:
		return Unsupported
	}
}
