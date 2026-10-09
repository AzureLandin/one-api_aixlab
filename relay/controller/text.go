package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/helper"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/relay"
	"github.com/songquanpeng/one-api/relay/adaptor"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/apitype"
	"github.com/songquanpeng/one-api/relay/channeltype"
	"github.com/songquanpeng/one-api/relay/meta"
	"github.com/songquanpeng/one-api/relay/model"
	usagelog "github.com/songquanpeng/one-api/relay/usage"
)

func RelayTextHelper(c *gin.Context) *model.ErrorWithStatusCode {
	ctx := c.Request.Context()
	meta := meta.GetByContext(c)
	// get & validate textRequest
	textRequest, err := getAndValidateTextRequest(c, meta.Mode)
	if err != nil {
		logger.Errorf(ctx, "getAndValidateTextRequest failed: %s", err.Error())
		return openai.ErrorWrapper(err, "invalid_text_request", http.StatusBadRequest)
	}
	meta.IsStream = textRequest.Stream

	// map model name
	meta.OriginModelName = textRequest.Model
	textRequest.Model, _ = getMappedModelName(textRequest.Model, meta.ModelMapping)
	meta.ActualModelName = textRequest.Model
	// set system prompt if not empty
	systemPromptReset := setSystemPrompt(ctx, textRequest, meta.ForcedSystemPrompt)
	// estimate prompt tokens for usage fallback (no quota involved anymore)
	promptTokens := getPromptTokens(textRequest, meta.Mode)
	meta.PromptTokens = promptTokens

	// Per-model overrides configured on the channel: fill in provider defaults
	// only where the client was silent, and enforce declared hard limits.
	if bizErr := applyModelConfigOverrides(meta, textRequest); bizErr != nil {
		return bizErr
	}

	adaptor := relay.GetAdaptor(meta.APIType)
	if adaptor == nil {
		return openai.ErrorWrapper(fmt.Errorf("invalid api type: %d", meta.APIType), "invalid_api_type", http.StatusBadRequest)
	}
	adaptor.Init(meta)

	// get request body
	requestBody, err := getRequestBody(c, meta, textRequest, adaptor)
	if err != nil {
		return openai.ErrorWrapper(err, "convert_request_failed", http.StatusInternalServerError)
	}

	// do request
	resp, err := adaptor.DoRequest(c, meta, requestBody)
	if err != nil {
		logger.Errorf(ctx, "DoRequest failed: %s", err.Error())
		recordUsage(ctx, meta, textRequest.Model, nil, model.UsageStatusError)
		return openai.ErrorWrapper(err, "do_request_failed", http.StatusInternalServerError)
	}
	if isErrorHappened(meta, resp) {
		recordUsage(ctx, meta, textRequest.Model, nil, model.UsageStatusError)
		return RelayErrorHandler(resp)
	}

	// do response
	usage, respErr := adaptor.DoResponse(c, resp, meta)
	if respErr != nil {
		logger.Errorf(ctx, "respErr is not nil: %+v", respErr)
		recordUsage(ctx, meta, textRequest.Model, usage, model.UsageStatusError)
		return respErr
	}
	// legacy consumption log (quota-free) + usage fact
	go recordConsumeLog(ctx, usage, meta, textRequest.Model, systemPromptReset)
	go recordUsage(ctx, meta, textRequest.Model, usage, model.UsageStatusSuccess)
	return nil
}

// recordUsage persists the usage fact alongside the legacy quota path during
// the parallel-run phase (implementation doc §5.1 step 1-2). Recording failure
// must never overwrite the upstream response error.
//
// The model name is passed as a string rather than read off a normalized
// request: the same-protocol passthrough path never builds one, and inventing a
// GeneralOpenAIRequest just to carry a name for the record would mean
// normalizing a request that is deliberately forwarded untouched.
func recordUsage(ctx context.Context, meta *meta.Meta, modelName string, u *model.Usage, status string) {
	input := usagelog.RecordInput{
		RequestID: helper.GetRequestID(ctx),
		UserID:    meta.UserId,
		TokenID:   meta.TokenId,
		ChannelID: meta.ChannelId,
		ModelName: modelName,
		Stream:    meta.IsStream,
		Status:    status,
	}
	if u != nil {
		input.Usage = *u
	}
	if err := usagelog.Default().RecordOnce(ctx, input); err != nil {
		logger.Errorf(ctx, "usage record failed: %s", err.Error())
	}
}

func getRequestBody(c *gin.Context, meta *meta.Meta, textRequest *model.GeneralOpenAIRequest, adaptor adaptor.Adaptor) (io.Reader, error) {
	if !config.EnforceIncludeUsage &&
		meta.APIType == apitype.OpenAI &&
		meta.OriginModelName == meta.ActualModelName &&
		meta.ChannelType != channeltype.Baichuan &&
		meta.ForcedSystemPrompt == "" &&
		// A per-model override mutated textRequest, so the raw body can no
		// longer be forwarded untouched.
		!hasModelConfigOverrides(meta) {
		// no need to convert request for openai
		return c.Request.Body, nil
	}

	// get request body
	var requestBody io.Reader
	convertedRequest, err := adaptor.ConvertRequest(c, meta.Mode, textRequest)
	if err != nil {
		logger.Debugf(c.Request.Context(), "converted request failed: %s\n", err.Error())
		return nil, err
	}
	jsonData, err := json.Marshal(convertedRequest)
	if err != nil {
		logger.Debugf(c.Request.Context(), "converted request json_marshal_failed: %s\n", err.Error())
		return nil, err
	}
	logger.Debugf(c.Request.Context(), "converted request: \n%s", string(jsonData))
	requestBody = bytes.NewBuffer(jsonData)
	return requestBody, nil
}
