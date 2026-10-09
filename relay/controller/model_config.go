package controller

import (
	"fmt"
	"net/http"

	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// modelConfigFor returns the effective per-model override block for the model
// this request resolved to. ok is false when nothing is configured, which keeps
// the untouched relay fast path in place.
func modelConfigFor(m *meta.Meta) (model.ModelConfig, bool) {
	if m == nil {
		return model.ModelConfig{}, false
	}
	return m.Config.GetModelConfig(m.ActualModelName)
}

// hasModelConfigOverrides reports whether any override applies to this request.
func hasModelConfigOverrides(m *meta.Meta) bool {
	_, ok := modelConfigFor(m)
	return ok
}

// applyModelConfigOverrides enforces and fills in the channel's per-model
// settings. Fill-in semantics are strictly "client wins": a value the client
// already sent is never replaced, and a field the admin left blank is never
// invented, so an unconfigured model keeps the upstream provider's defaults.
func applyModelConfigOverrides(m *meta.Meta, req *relaymodel.GeneralOpenAIRequest) *relaymodel.ErrorWithStatusCode {
	mc, ok := modelConfigFor(m)
	if !ok || req == nil {
		return nil
	}

	// Context window: reject before spending an upstream call when the admin
	// declared a hard limit and the request already exceeds it.
	if mc.ContextWindow > 0 && m.PromptTokens > mc.ContextWindow {
		return openai.ErrorWrapper(
			fmt.Errorf("模型 %s 的上下文上限为 %d tokens，当前请求约 %d tokens",
				m.ActualModelName, mc.ContextWindow, m.PromptTokens),
			"context_window_exceeded", http.StatusBadRequest)
	}

	// Max output tokens: only fill in when the client asked for nothing.
	if mc.MaxOutputTokens > 0 && req.MaxTokens == 0 && req.MaxCompletionTokens == nil {
		req.MaxTokens = mc.MaxOutputTokens
	}

	// reasoning_effort is intentionally left alone: the client's value passes
	// through to the upstream verbatim, and nothing is injected when absent.

	// Input modalities: refuse content the admin declared unsupported.
	if len(mc.InputModalities) > 0 {
		if bad := firstDisallowedModality(req, mc); bad != "" {
			return openai.ErrorWrapper(
				fmt.Errorf("模型 %s 不支持 %s 输入，允许的输入模态为 %v",
					m.ActualModelName, bad, mc.InputModalities),
				"unsupported_input_modality", http.StatusBadRequest)
		}
	}

	return nil
}

// firstDisallowedModality returns the first input modality present in the
// request that the override does not allow, or "" when the request is fine.
func firstDisallowedModality(req *relaymodel.GeneralOpenAIRequest, mc model.ModelConfig) string {
	for _, message := range req.Messages {
		for _, part := range message.ParseContent() {
			modality := modalityOfContentType(part.Type)
			if modality == "" {
				continue
			}
			if !mc.AllowsModality(modality) {
				return modality
			}
		}
	}
	return ""
}

// modalityOfContentType maps an OpenAI content-part type onto the coarse
// modality names admins configure. Unknown part types are not gated.
func modalityOfContentType(contentType string) string {
	switch contentType {
	case relaymodel.ContentTypeText:
		return "text"
	case relaymodel.ContentTypeImageURL:
		return "image"
	case relaymodel.ContentTypeInputAudio:
		return "audio"
	default:
		return ""
	}
}
