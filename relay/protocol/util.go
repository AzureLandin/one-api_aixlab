package protocol

import (
	"encoding/json"
	"strconv"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

func jsonStringOf(m map[string]json.RawMessage, key string) string {
	raw, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func anyOf(m map[string]json.RawMessage, key string) any {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

func itoa(i int) string {
	return strconv.Itoa(i)
}

// rawOf returns a raw fragment of an object, or nil when it is absent. Callers
// pass the result straight into an omitempty raw field, so "absent" stays
// absent rather than becoming the four bytes "null".
func rawOf(m map[string]json.RawMessage, key string) json.RawMessage {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	return raw
}

// jsonStringListOf reads a string array field, ignoring anything that is not
// one. A malformed filter list degrades to "no filter" here; the declaration it
// came from is still carried, so the loss policy sees the tool either way.
func jsonStringListOf(m map[string]json.RawMessage, key string) []string {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// hostedOf returns the request's neutral hosted set, creating it on first use.
//
// Parsers only EXTRACT — they have no channel, so they cannot know what the
// upstream speaks or what the channel's loss policy is. Everything that would
// previously have been refused at parse time is carried here instead, and the
// attach step decides what survives.
func hostedOf(req *relaymodel.GeneralOpenAIRequest) *relaymodel.HostedSet {
	if req.Hosted == nil {
		req.Hosted = &relaymodel.HostedSet{}
	}
	return req.Hosted
}
