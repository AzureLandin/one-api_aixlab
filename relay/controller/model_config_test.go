package controller

import (
	"testing"

	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

func metaWithModelConfig(modelName string, promptTokens int, mc model.ModelConfig) *meta.Meta {
	return &meta.Meta{
		ActualModelName: modelName,
		PromptTokens:    promptTokens,
		Config: model.ChannelConfig{
			ModelConfigs: map[string]model.ModelConfig{modelName: mc},
		},
	}
}

func textRequest(prompt string) *relaymodel.GeneralOpenAIRequest {
	return &relaymodel.GeneralOpenAIRequest{
		Model: "m",
		Messages: []relaymodel.Message{
			{Role: "user", Content: prompt},
		},
	}
}

func imageRequest() *relaymodel.GeneralOpenAIRequest {
	return &relaymodel.GeneralOpenAIRequest{
		Model: "m",
		Messages: []relaymodel.Message{
			{Role: "user", Content: []any{
				map[string]any{
					"type": relaymodel.ContentTypeText,
					"text": "describe this",
				},
				map[string]any{
					"type":      relaymodel.ContentTypeImageURL,
					"image_url": map[string]any{"url": "https://example.com/a.png"},
				},
			}},
		},
	}
}

// An unconfigured model must stay completely untouched: this is the guarantee
// that the provider's own defaults remain in effect.
func TestModelConfigUnconfiguredIsNoOp(t *testing.T) {
	// The channel has an override, but only for a different model.
	m := &meta.Meta{
		ActualModelName: "unlisted-model",
		PromptTokens:    100,
		Config: model.ChannelConfig{
			ModelConfigs: map[string]model.ModelConfig{
				"some-other-model": {MaxOutputTokens: 4096},
			},
		},
	}
	req := textRequest("hi")

	if hasModelConfigOverrides(m) {
		t.Fatal("no override should apply to a model that has no entry")
	}
	if err := applyModelConfigOverrides(m, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.MaxTokens != 0 || req.ReasoningEffort != nil {
		t.Fatalf("request must be untouched, got max_tokens=%d effort=%v", req.MaxTokens, req.ReasoningEffort)
	}
}

// An entry present but carrying no effective setting counts as unconfigured.
func TestModelConfigZeroEntryIsNoOp(t *testing.T) {
	m := metaWithModelConfig("m", 100, model.ModelConfig{})
	if hasModelConfigOverrides(m) {
		t.Fatal("a zero-valued entry must not count as an override")
	}
}

// Blank fields fall back to the provider; only the configured field is applied.
func TestModelConfigFillsOnlyConfiguredFields(t *testing.T) {
	m := metaWithModelConfig("m", 100, model.ModelConfig{MaxOutputTokens: 2048})
	req := textRequest("hi")

	if !hasModelConfigOverrides(m) {
		t.Fatal("override should be detected")
	}
	if err := applyModelConfigOverrides(m, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.MaxTokens != 2048 {
		t.Fatalf("max_tokens want 2048, got %d", req.MaxTokens)
	}
	if req.ReasoningEffort != nil {
		t.Fatalf("reasoning_effort must stay unset so the provider default applies, got %v", *req.ReasoningEffort)
	}
}

// The client's explicit choice always wins over the channel default.
func TestModelConfigClientValueWins(t *testing.T) {
	m := metaWithModelConfig("m", 100, model.ModelConfig{
		MaxOutputTokens: 2048,
	})
	clientEffort := "high"
	req := textRequest("hi")
	req.MaxTokens = 512
	req.ReasoningEffort = &clientEffort

	if err := applyModelConfigOverrides(m, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.MaxTokens != 512 {
		t.Fatalf("client max_tokens must win, got %d", req.MaxTokens)
	}
	if req.ReasoningEffort == nil || *req.ReasoningEffort != "high" {
		t.Fatalf("client reasoning_effort must win, got %v", req.ReasoningEffort)
	}
}

// max_completion_tokens is the modern spelling of the same limit; when the
// client used it, max_tokens must not also be injected.
func TestModelConfigRespectsMaxCompletionTokens(t *testing.T) {
	m := metaWithModelConfig("m", 100, model.ModelConfig{MaxOutputTokens: 2048})
	clientMax := 300
	req := textRequest("hi")
	req.MaxCompletionTokens = &clientMax

	if err := applyModelConfigOverrides(m, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.MaxTokens != 0 {
		t.Fatalf("max_tokens must not be injected when client set max_completion_tokens, got %d", req.MaxTokens)
	}
}

// reasoning_effort is not a channel setting at all: whatever the client sent
// must survive the override pass untouched, and nothing may be invented when
// the client sent nothing.
func TestModelConfigLeavesReasoningEffortAlone(t *testing.T) {
	levels := []string{"minimal", "low", "medium", "high", "xhigh", "max", "ultra", "none", "dynamic"}

	for _, level := range levels {
		t.Run("client value preserved: "+level, func(t *testing.T) {
			// Configure everything else, to prove a configured model still
			// leaves reasoning_effort alone.
			m := metaWithModelConfig("m", 100, model.ModelConfig{MaxOutputTokens: 2048})
			req := textRequest("hi")
			clientLevel := level
			req.ReasoningEffort = &clientLevel

			if err := applyModelConfigOverrides(m, req); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if req.ReasoningEffort == nil || *req.ReasoningEffort != level {
				t.Fatalf("client reasoning_effort %q must pass through verbatim, got %v", level, req.ReasoningEffort)
			}
		})
	}

	t.Run("nothing injected when client silent", func(t *testing.T) {
		m := metaWithModelConfig("m", 100, model.ModelConfig{MaxOutputTokens: 2048})
		req := textRequest("hi")

		if err := applyModelConfigOverrides(m, req); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req.ReasoningEffort != nil {
			t.Fatalf("no reasoning_effort may be injected, got %q", *req.ReasoningEffort)
		}
	})
}

func TestModelConfigContextWindowEnforced(t *testing.T) {
	over := metaWithModelConfig("m", 9000, model.ModelConfig{ContextWindow: 8192})
	if err := applyModelConfigOverrides(over, textRequest("hi")); err == nil {
		t.Fatal("request above the declared context window must be rejected")
	}

	under := metaWithModelConfig("m", 1000, model.ModelConfig{ContextWindow: 8192})
	if err := applyModelConfigOverrides(under, textRequest("hi")); err != nil {
		t.Fatalf("request within the context window must pass, got %v", err)
	}
}

func TestModelConfigInputModalities(t *testing.T) {
	textOnly := metaWithModelConfig("m", 10, model.ModelConfig{InputModalities: []string{"text"}})
	if err := applyModelConfigOverrides(textOnly, imageRequest()); err == nil {
		t.Fatal("image input must be rejected when only text is allowed")
	}
	if err := applyModelConfigOverrides(textOnly, textRequest("hi")); err != nil {
		t.Fatalf("text input must pass, got %v", err)
	}

	withImage := metaWithModelConfig("m", 10, model.ModelConfig{InputModalities: []string{"text", "image"}})
	if err := applyModelConfigOverrides(withImage, imageRequest()); err != nil {
		t.Fatalf("image input must pass when allowed, got %v", err)
	}
}

// An empty whitelist means "no restriction", not "nothing allowed".
func TestModelConfigEmptyModalitiesMeansUnrestricted(t *testing.T) {
	m := metaWithModelConfig("m", 10, model.ModelConfig{MaxOutputTokens: 100})
	if err := applyModelConfigOverrides(m, imageRequest()); err != nil {
		t.Fatalf("empty modality list must not restrict input, got %v", err)
	}
}
