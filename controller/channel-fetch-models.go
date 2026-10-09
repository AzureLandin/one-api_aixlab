package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/client"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/channeltype"
)

// fetchModelsRequest lets the admin probe an upstream before the channel is
// saved. When id is provided and key/base_url are omitted, the stored channel
// values are used instead, so the button also works on existing channels.
//
// ProxyURL is a pointer on purpose: nil means "the caller said nothing, use the
// stored channel proxy", while an explicit "" means "the admin cleared the proxy
// field, so probe directly". Without that distinction, clearing the field while
// editing would silently keep probing through the old proxy.
type fetchModelsRequest struct {
	Id       int     `json:"id"`
	Type     int     `json:"type"`
	BaseURL  string  `json:"base_url"`
	Key      string  `json:"key"`
	ProxyURL *string `json:"proxy_url"`
}

// upstreamModelList is the union shape of the OpenAI and Anthropic model list
// responses: both return {"data":[{"id":"..."}]}.
type upstreamModelList struct {
	Data []struct {
		Id string `json:"id"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// FetchChannelModels queries the upstream provider's model list so the admin
// can populate the channel's model whitelist without typing model names by
// hand. It never persists anything.
func FetchChannelModels(c *gin.Context) {
	ctx := c.Request.Context()
	req := fetchModelsRequest{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "请求参数解析失败：" + err.Error(),
		})
		return
	}

	// Fall back to the stored channel when the form did not supply credentials.
	storedProxy := ""
	if req.Id > 0 {
		channel, err := model.GetChannelById(req.Id, true)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "读取渠道失败：" + err.Error(),
			})
			return
		}
		if req.Type == 0 {
			req.Type = channel.Type
		}
		if strings.TrimSpace(req.BaseURL) == "" {
			req.BaseURL = channel.GetBaseURL()
		}
		if strings.TrimSpace(req.Key) == "" {
			req.Key = channel.Key
		}
		if cfg, cerr := channel.LoadConfig(); cerr == nil {
			storedProxy = cfg.ProxyURL
		}
	}

	proxyURL := resolveProbeProxy(req.ProxyURL, storedProxy)

	if req.Type == 0 {
		req.Type = channeltype.OpenAICompatible
	}
	if strings.TrimSpace(req.Key) == "" {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "请先填写 API 密钥，获取模型需要用它访问上游",
		})
		return
	}

	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		baseURL = channeltype.ChannelBaseURLs[req.Type]
	}
	if strings.TrimSpace(baseURL) == "" {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "该渠道类型没有默认地址，请先填写 Base URL",
		})
		return
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "https://" + baseURL
	}

	// A channel key may hold several newline separated upstream keys; probing
	// only needs the first one.
	key := strings.Split(strings.TrimSpace(req.Key), "\n")[0]
	key = strings.TrimSpace(key)

	fullURL, headers := buildModelsProbe(req.Type, baseURL, key)

	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(probeCtx, http.MethodGet, fullURL, nil)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "构造请求失败：" + err.Error(),
		})
		return
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Accept", "application/json")

	// Reuse the relay client selection so a channel proxy (including a local
	// proxy) is honored exactly as the real traffic would be.
	resp, err := client.RelayHTTPClient(proxyURL).Do(httpReq)
	if err != nil {
		logger.Errorf(ctx, "fetch models from %s failed: %s", fullURL, err.Error())
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": fmt.Sprintf("无法连接上游 %s：%s", fullURL, err.Error()),
		})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "读取上游响应失败：" + err.Error(),
		})
		return
	}

	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": fmt.Sprintf("上游返回 %d：%s", resp.StatusCode, truncateForMessage(string(body))),
		})
		return
	}

	var parsed upstreamModelList
	if err := json.Unmarshal(body, &parsed); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "上游响应不是合法的模型列表 JSON：" + truncateForMessage(string(body)),
		})
		return
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "上游返回错误：" + parsed.Error.Message,
		})
		return
	}

	seen := make(map[string]struct{}, len(parsed.Data))
	models := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		id := strings.TrimSpace(m.Id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	if len(models) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "上游返回的模型列表为空：" + truncateForMessage(string(body)),
		})
		return
	}
	sort.Strings(models)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    models,
		"count":   len(models),
	})
}

// resolveProbeProxy decides which proxy the model probe must use. An explicit
// form value always wins — including an explicit empty string, which means the
// admin cleared the field and wants a direct probe. Only when the caller was
// silent (nil) does an already-saved channel proxy still apply.
func resolveProbeProxy(formValue *string, storedProxy string) string {
	if formValue != nil {
		return strings.TrimSpace(*formValue)
	}
	return strings.TrimSpace(storedProxy)
}

// buildModelsProbe returns the upstream model-list URL plus the auth headers
// appropriate for the channel type. Only the two supported protocol families
// are handled explicitly; everything else is treated as OpenAI-compatible.
func buildModelsProbe(channelType int, baseURL string, key string) (string, map[string]string) {
	requestURL := "/v1/models"
	switch channelType {
	case channeltype.Anthropic:
		fullURL := strings.TrimSuffix(baseURL, "/") + requestURL
		return fullURL, map[string]string{
			"x-api-key":         key,
			"anthropic-version": "2023-06-01",
		}
	case channeltype.GeminiOpenAICompatible:
		fullURL := strings.TrimSuffix(baseURL, "/") + requestURL
		return fullURL, map[string]string{
			"Authorization": "Bearer " + key,
		}
	default:
		// OpenAI and OpenAI-compatible: reuse the shared join rule so a base URL
		// that already ends in /v1 does not become /v1/v1.
		fullURL := openai.GetFullRequestURL(baseURL, requestURL, channelType)
		return fullURL, map[string]string{
			"Authorization": "Bearer " + key,
		}
	}
}

func truncateForMessage(s string) string {
	s = strings.TrimSpace(s)
	const max = 300
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
