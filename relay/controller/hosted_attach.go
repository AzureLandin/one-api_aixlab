package controller

import (
	"github.com/songquanpeng/one-api/common/logger"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// Hosted-tool attachment, request direction.
//
// Only the Chat Completions target is attached here. The Responses and
// Anthropic bodies are built by their own converters (ChatToResponsesRequest,
// anthropic.ConvertRequest), and those converters are where a hosted turn can
// be replayed at the RIGHT POSITION — only they know where a message boundary
// lands in their own wire shape, and a hosted turn always sits between two
// messages. So they read req.Hosted themselves and record what they could not
// carry on the same set; this file owns the one target that has no converter
// (a chat body is `json.Marshal(req)`), plus the logging every path shares.

// attachChat folds a hosted set into a Chat Completions request.
//
// Chat Completions can express exactly one thing hosted — the
// web_search_options toggle — and has no artifact vocabulary at all, so
// everything else, every replayed hosted turn included, is a recorded loss
// rather than a silent one.
func attachChat(req *relaymodel.GeneralOpenAIRequest) {
	set := req.Hosted
	if set == nil {
		return
	}
	for _, def := range set.Definitions {
		if def.Kind != relaymodel.HostedWebSearch {
			set.AddLoss(relaymodel.UnsupportedHostedLoss("OpenAI Chat Completions", def))
			continue
		}
		if req.WebSearchOptions != nil {
			// The client declared the toggle itself and also (through a hosted
			// declaration) asked for it again. The client's own field wins, as
			// it does for every other field this gateway fills in.
			set.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagDuplicateHostedWebSearch, def.Path,
				"客户端已自带 web_search_options，忽略重复的托管搜索声明 %s", relaymodel.DescribeHosted(def)))
			continue
		}
		opts := &relaymodel.WebSearchOptions{}
		if def.WebSearch != nil {
			opts.SearchContextSize = def.WebSearch.SearchContextSize
			opts.UserLocation = def.WebSearch.UserLocation
			if def.WebSearch.MaxUses != nil {
				set.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagUnsupportedSearchLimit, def.Path,
					"OpenAI Chat Completions 无法表达搜索次数上限 max_uses=%d", *def.WebSearch.MaxUses))
			}
			if len(def.WebSearch.AllowedDomains) > 0 || len(def.WebSearch.BlockedDomains) > 0 {
				set.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagUnsupportedDomainFilter, def.Path,
					"OpenAI Chat Completions 无法表达搜索域名过滤（allowed/blocked_domains）"))
			}
		}
		req.WebSearchOptions = opts
	}
	if set.ForcedChoice != nil {
		// Chat has no way to force a hosted tool: the toggle asks the model to
		// consider searching, it does not require it.
		set.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagForcedChoiceDowngraded, set.ForcedChoice.Path,
			"OpenAI Chat Completions 无法强制使用托管工具 %s，已降级为自动选择",
			relaymodel.DescribeHosted(*set.ForcedChoice)))
	}
	if len(set.History) > 0 {
		// A server-tool record has nowhere to go in a chat transcript: the
		// protocol has no item type for it, and folding it into a message would
		// invent a turn the client did not write.
		set.AddLoss(relaymodel.NewHostedLoss(relaymodel.DiagUnsupportedHostedHistory, "input",
			"OpenAI Chat Completions 无处回放 %d 条托管工具历史（内置工具的搜索结果/调用记录）", len(set.History)))
	}
}

// logHostedLosses writes one line per drop, naming the channel so an operator
// can find which route lost what. The token "hosted_tool_loss" is what the
// smoke test greps for; the code is the same string the request-phase 400
// carries under a safe/strict policy, so the two are greppable together.
func logHostedLosses(channelId int, set *relaymodel.HostedSet) {
	for _, d := range set.LossOf() {
		logger.SysWarnf("channel %d hosted_tool_loss: code=%s path=%s %s",
			channelId, d.Code, d.Path, d.Message)
	}
}
