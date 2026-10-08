package protocol

import (
	"encoding/json"
	"strconv"
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
