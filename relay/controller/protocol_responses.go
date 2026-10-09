package controller

import (
	"github.com/gin-gonic/gin"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/protocol"
	"github.com/songquanpeng/one-api/relay/sse"
	"github.com/songquanpeng/one-api/relay/upstream"
)

var responsesEntry = &protocolEntry{
	Name:     "/v1/responses",
	Protocol: upstream.OpenAIResponses,
	Parse:    protocol.ParseResponses,
	Render:   protocol.RenderResponses,
	NewStream: func(w *sse.Writer, id, model string, promptUsage *relaymodel.Usage) protocolStream {
		return protocol.NewResponsesStream(w, id, model)
	},
}

// RelayResponsesHelper serves the OpenAI Responses client protocol
// (/v1/responses). Limits per implementation doc §6.4; streaming §7.
func RelayResponsesHelper(c *gin.Context) *relaymodel.ErrorWithStatusCode {
	return relayProtocolHelper(c, responsesEntry)
}
