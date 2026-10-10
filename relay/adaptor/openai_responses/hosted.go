package openai_responses

import (
	"encoding/json"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// Hosted response side: lifting the artifacts of upstream-executed tools out of
// a Responses answer into the neutral form, so a client protocol that has a
// vocabulary for them can put them back on the wire.
//
// It is a separate walk from decode() rather than a branch inside it. decode()
// answers "what does this answer say" for the chat envelope, and a hosted item
// says nothing there; this answers "what did the upstream DO", which is a
// different question with a different destination. Keeping them apart also
// keeps decode()'s contract — the chat envelope's text, reasoning and function
// calls — free of a fourth kind of output.

// HostedOutputOf returns the hosted artifacts of a Responses answer, or nil
// when it carries none.
//
// Unlike Claude, a Responses answer already spells an artifact as ONE item: a
// web_search_call carries its action and its results, an mcp_call its
// arguments and its output. So there is no merging to do here, only a
// translation of names.
func HostedOutputOf(resp *Response) *relaymodel.HostedOutput {
	if resp == nil || len(resp.Output) == 0 {
		return nil
	}
	out := &relaymodel.HostedOutput{}
	for _, item := range resp.Output {
		switch item.Type {
		case "web_search_call":
			out.Items = append(out.Items, relaymodel.HostedOutputItem{
				Kind:   relaymodel.HostedWebSearch,
				ID:     item.ID,
				CallID: item.ID,
				Status: hostedStatus(item.Status),
				Action: item.Action,
				// A web_search_call names the search it ran; the results themselves
				// are reported inline to the model, not as a field of this item, so
				// there is nothing to carry across in Results — and nothing is
				// invented to fill it.
				Results: nil,
			})
		case "mcp_call":
			out.Items = append(out.Items, relaymodel.HostedOutputItem{
				Kind:        relaymodel.HostedMCP,
				ID:          item.ID,
				CallID:      defaultString(item.CallId, item.ID),
				Name:        item.Name,
				ServerLabel: item.ServerLabel,
				Status:      hostedStatus(item.Status),
				Arguments:   argumentsRaw(item.Arguments),
				Output:      item.Output,
				Error:       hostedItemError(item.Error),
			})
		}
	}
	if len(out.Items) == 0 {
		return nil
	}
	return out
}

// hostedStatus defaults a missing status to completed: the answer arrived, so
// the call it describes is over unless the upstream said otherwise.
func hostedStatus(status string) string {
	if status == "" {
		return relaymodel.HostedStatusCompleted
	}
	return status
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// argumentsRaw normalizes an mcp_call's arguments, which the protocol carries
// as a JSON-encoded string, into the object the neutral form holds — the same
// normalization the parser applies on the way in, so a round trip through this
// gateway does not change the shape of what the client sees.
func argumentsRaw(args any) json.RawMessage {
	if args == nil {
		return nil
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if json.Valid([]byte(s)) {
			return json.RawMessage(s)
		}
		return nil
	}
	return raw
}

// hostedItemError reads a failed mcp_call's error object. The Responses shape is
// {"code":…,"message":…}; a string is accepted too, because that is what some
// compatible upstreams send.
func hostedItemError(raw json.RawMessage) *relaymodel.Error {
	if len(raw) == 0 {
		return nil
	}
	var obj struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && (obj.Message != "" || obj.Code != nil) {
		msg := obj.Message
		if msg == "" {
			if s, ok := obj.Code.(string); ok {
				msg = s
			}
		}
		return &relaymodel.Error{
			Message: msg,
			Type:    defaultString(obj.Type, "upstream_error"),
			Code:    obj.Code,
		}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return &relaymodel.Error{Message: s, Type: "upstream_error"}
	}
	return nil
}
