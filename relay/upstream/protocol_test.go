package upstream

import (
	"testing"

	"github.com/songquanpeng/one-api/relay/apitype"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

func TestResolveConfiguredWins(t *testing.T) {
	// An explicit value overrides the derivation, including when it contradicts
	// the api type: an OpenAI-typed channel may be pointed at a
	// Responses-speaking endpoint, and an Anthropic-typed one at a chat gateway.
	cases := []struct {
		configured string
		apiType    int
		want       Protocol
	}{
		{"openai-responses", apitype.OpenAI, OpenAIResponses},
		{"anthropic-messages", apitype.OpenAI, AnthropicMessages},
		{"openai-chat", apitype.Anthropic, OpenAIChat},
		{"compatible", apitype.OpenAI, Compatible},
	}
	for _, tc := range cases {
		if got := Resolve(tc.configured, tc.apiType); got != tc.want {
			t.Errorf("Resolve(%q, %d) = %q, want %q", tc.configured, tc.apiType, got, tc.want)
		}
	}
}

func TestResolveDerivesFromAPIType(t *testing.T) {
	cases := []struct {
		apiType int
		want    Protocol
	}{
		{apitype.OpenAI, OpenAIChat},
		{apitype.Anthropic, AnthropicMessages},
		// Bedrock's Claude models take the same Messages wire shape.
		{apitype.AwsClaude, AnthropicMessages},
	}
	for _, tc := range cases {
		if got := Resolve("", tc.apiType); got != tc.want {
			t.Errorf("Resolve(\"\", %d) = %q, want %q", tc.apiType, got, tc.want)
		}
	}
}

func TestResolveUntranslatedFamilyIsUnsupported(t *testing.T) {
	// These families have their own adaptors for the chat path but no
	// translation to the other two client protocols; resolving them to
	// openai-chat would claim a capability the gateway does not have.
	for _, apiType := range []int{apitype.Gemini, apitype.Baidu, apitype.Coze, apitype.VertexAI, apitype.Cohere} {
		if got := Resolve("", apiType); got != Unsupported {
			t.Errorf("Resolve(\"\", %d) = %q, want Unsupported", apiType, got)
		}
	}
}

func TestValid(t *testing.T) {
	for _, name := range []string{"openai-chat", "openai-responses", "anthropic-messages", "compatible"} {
		if !Valid(name) {
			t.Errorf("Valid(%q) = false, want true", name)
		}
	}
	// A near-miss must not pass: silently resolving a typo to openai-chat would
	// take the translation path and drop native fields without any error.
	for _, name := range []string{"", "openai", "OpenAI-Chat", "openai_chat", "responses", "anthropic"} {
		if Valid(name) {
			t.Errorf("Valid(%q) = true, want false", name)
		}
	}
}

func TestCanServeCoversEveryTranslatedPair(t *testing.T) {
	clients := []Protocol{OpenAIChat, OpenAIResponses, AnthropicMessages}
	ups := []Protocol{OpenAIChat, OpenAIResponses, AnthropicMessages, Compatible}
	for _, up := range ups {
		for _, client := range clients {
			if !CanServe(client, up) {
				t.Errorf("CanServe(%q, %q) = false, want true", client, up)
			}
		}
	}
}

func TestCanServeRefusesUntranslatedUpstreamAndUnknownClient(t *testing.T) {
	if CanServe(OpenAIResponses, Unsupported) {
		t.Error("CanServe(OpenAIResponses, Unsupported) = true, want false")
	}
	// An upstream claims nothing about a client protocol that is not one of the
	// three entry points.
	if CanServe(Unsupported, OpenAIChat) {
		t.Error("CanServe(Unsupported, OpenAIChat) = true, want false")
	}
}

func TestPassthroughOnlyForExactTranslatedMatch(t *testing.T) {
	passthrough := []struct{ client, up Protocol }{
		{OpenAIChat, OpenAIChat},
		{OpenAIResponses, OpenAIResponses},
		{AnthropicMessages, AnthropicMessages},
	}
	for _, tc := range passthrough {
		if !Passthrough(tc.client, tc.up) {
			t.Errorf("Passthrough(%q, %q) = false, want true", tc.client, tc.up)
		}
	}

	// Compatible is chat-shaped but explicitly excluded from passthrough, and a
	// cross-protocol pair has to be translated.
	notPassthrough := []struct{ client, up Protocol }{
		{OpenAIChat, Compatible},
		{AnthropicMessages, OpenAIResponses},
		{OpenAIResponses, AnthropicMessages},
		{OpenAIChat, Unsupported},
		{Unsupported, Unsupported},
	}
	for _, tc := range notPassthrough {
		if Passthrough(tc.client, tc.up) {
			t.Errorf("Passthrough(%q, %q) = true, want false", tc.client, tc.up)
		}
	}
}

func TestClientProtocolFor(t *testing.T) {
	cases := []struct {
		mode int
		want Protocol
	}{
		{relaymode.ChatCompletions, OpenAIChat},
		{relaymode.Responses, OpenAIResponses},
		{relaymode.AnthropicMessages, AnthropicMessages},
		{relaymode.Embeddings, Unsupported},
		{relaymode.Unknown, Unsupported},
	}
	for _, tc := range cases {
		if got := ClientProtocolFor(tc.mode); got != tc.want {
			t.Errorf("ClientProtocolFor(%d) = %q, want %q", tc.mode, got, tc.want)
		}
	}
}
