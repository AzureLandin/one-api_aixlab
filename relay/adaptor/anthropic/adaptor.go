package anthropic

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/relay/adaptor"
	"github.com/songquanpeng/one-api/relay/meta"
	"github.com/songquanpeng/one-api/relay/model"
)

type Adaptor struct {
}

func (a *Adaptor) Init(meta *meta.Meta) {

}

func (a *Adaptor) GetRequestURL(meta *meta.Meta) (string, error) {
	return fmt.Sprintf("%s/v1/messages", meta.BaseURL), nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Request, meta *meta.Meta) error {
	adaptor.SetupCommonRequestHeader(c, req, meta)
	req.Header.Set("x-api-key", meta.APIKey)
	anthropicVersion := c.Request.Header.Get("anthropic-version")
	if anthropicVersion == "" {
		anthropicVersion = "2023-06-01"
	}
	req.Header.Set("anthropic-version", anthropicVersion)

	// anthropic-beta is a comma-separated set of opt-ins, and a client reaching
	// this channel through the same-protocol passthrough brings its own (computer
	// use, 1M context, ...). The channel's flags are merged into whatever the
	// client sent: overwriting would silently drop the client's opt-in, which the
	// upstream reads as "not requested" — a wrong answer rather than an error.
	betas := mergeAnthropicBeta(c.Request.Header.Get("anthropic-beta"), "messages-2023-12-15")
	// https://x.com/alexalbert__/status/1812921642143900036
	// claude-3-5-sonnet can support 8k context
	if strings.HasPrefix(meta.ActualModelName, "claude-3-5-sonnet") {
		betas = mergeAnthropicBeta(betas, "max-tokens-3-5-sonnet-2024-07-15")
	}
	req.Header.Set("anthropic-beta", betas)

	return nil
}

// mergeAnthropicBeta appends flags to a comma-separated anthropic-beta value,
// dropping duplicates and empty entries so the header stays canonical.
func mergeAnthropicBeta(clientValue string, flags ...string) string {
	seen := make(map[string]bool)
	var out []string
	appendFlag := func(flag string) {
		flag = strings.TrimSpace(flag)
		if flag == "" || seen[flag] {
			return
		}
		seen[flag] = true
		out = append(out, flag)
	}
	for _, part := range strings.Split(clientValue, ",") {
		appendFlag(part)
	}
	for _, flag := range flags {
		appendFlag(flag)
	}
	return strings.Join(out, ",")
}

func (a *Adaptor) ConvertRequest(c *gin.Context, relayMode int, request *model.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	return ConvertRequest(*request), nil
}

func (a *Adaptor) ConvertImageRequest(request *model.ImageRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	return request, nil
}

func (a *Adaptor) DoRequest(c *gin.Context, meta *meta.Meta, requestBody io.Reader) (*http.Response, error) {
	return adaptor.DoRequestHelper(a, c, meta, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *meta.Meta) (usage *model.Usage, err *model.ErrorWithStatusCode) {
	if meta.IsStream {
		err, usage = StreamHandler(c, resp)
	} else {
		err, usage = Handler(c, resp, meta.PromptTokens, meta.ActualModelName)
	}
	return
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return "anthropic"
}
