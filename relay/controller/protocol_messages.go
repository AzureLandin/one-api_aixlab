package controller

import (
	"strings"

	"github.com/gin-gonic/gin"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/protocol"
	"github.com/songquanpeng/one-api/relay/sse"
	"github.com/songquanpeng/one-api/relay/upstream"
)

var messagesEntry = &protocolEntry{
	Name:     "/v1/messages",
	Protocol: upstream.AnthropicMessages,
	Parse:    protocol.ParseMessages,
	Render:   protocol.RenderMessages,
	NewStream: func(w *sse.Writer, id, model string, promptUsage *relaymodel.Usage) protocolStream {
		// Re-key the shared stream id to the Anthropic msg_ namespace.
		msgID := "msg_" + strings.TrimPrefix(strings.TrimPrefix(id, "resp_"), "chatcmpl-")
		return protocol.NewMessagesStream(w, msgID, model, promptUsage)
	},
}

// RelayMessagesHelper serves the Anthropic Messages client protocol
// (/v1/messages). Covers system, content arrays, text/image blocks, tool_use,
// tool_result, stop_reason and usage (§6.5); streaming §7.
func RelayMessagesHelper(c *gin.Context) *relaymodel.ErrorWithStatusCode {
	return relayProtocolHelper(c, messagesEntry)
}
