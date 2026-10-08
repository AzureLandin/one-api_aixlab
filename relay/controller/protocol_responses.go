package controller

import (
	"github.com/gin-gonic/gin"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/protocol"
)

var responsesEntry = &protocolEntry{
	Name:   "/v1/responses",
	Parse:  protocol.ParseResponses,
	Stream: protocol.ResponsesStreamUnsupported,
	Render: protocol.RenderResponses,
}

// RelayResponsesHelper serves the OpenAI Responses client protocol
// (/v1/responses). First-version limits per implementation doc §6.4.
func RelayResponsesHelper(c *gin.Context) *relaymodel.ErrorWithStatusCode {
	return relayProtocolHelper(c, responsesEntry)
}
