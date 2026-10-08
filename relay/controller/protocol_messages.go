package controller

import (
	"github.com/gin-gonic/gin"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/protocol"
)

var messagesEntry = &protocolEntry{
	Name:   "/v1/messages",
	Parse:  protocol.ParseMessages,
	Stream: protocol.MessagesStreamUnsupported,
	Render: protocol.RenderMessages,
}

// RelayMessagesHelper serves the Anthropic Messages client protocol
// (/v1/messages). Covers system, content arrays, text/image blocks, tool_use,
// tool_result, stop_reason and usage (§6.5). Streaming lands in stage 4.
func RelayMessagesHelper(c *gin.Context) *relaymodel.ErrorWithStatusCode {
	return relayProtocolHelper(c, messagesEntry)
}
