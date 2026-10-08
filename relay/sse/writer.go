package sse

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// asString coerces a JSON value that may be a string or null into a string.
// (OpenAI stream deltas send content as string|null.)
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// FlushWriter is an http.ResponseWriter that can also flush; gin's writer and
// http.ResponseController both satisfy this.
type FlushWriter interface {
	http.ResponseWriter
	http.Flusher
}

// Writer emits typed SSE events ("event:" + "data:" lines) so Responses /
// Messages streams have explicit event types rather than one non-standard
// concatenated string (§6.4). It is safe to reuse across a single response.
type Writer struct {
	w       io.Writer
	flusher http.Flusher
}

// NewWriter wraps an io.Writer. If fw also implements http.Flusher, each event
// is flushed immediately (needed for streaming latency).
func NewWriter(w io.Writer) *Writer {
	sw := &Writer{w: w}
	if f, ok := w.(http.Flusher); ok {
		sw.flusher = f
	}
	return sw
}

// Event writes one typed event. data may be a struct/map (JSON-encoded) or a
// raw string. A [DONE] payload should be written via Done().
func (sw *Writer) Event(eventType string, data any) error {
	payload, err := encodeData(data)
	if err != nil {
		return err
	}
	var b strings.Builder
	if eventType != "" {
		fmt.Fprintf(&b, "event: %s\n", eventType)
	}
	// Multi-line data must each be prefixed with "data:" per the SSE spec.
	for _, line := range strings.Split(payload, "\n") {
		fmt.Fprintf(&b, "data: %s\n", line)
	}
	b.WriteString("\n")
	if _, err := io.WriteString(sw.w, b.String()); err != nil {
		return err
	}
	if sw.flusher != nil {
		sw.flusher.Flush()
	}
	return nil
}

// Raw writes an already-framed SSE block verbatim (used for OpenAI-style
// pass-through chunks).
func (sw *Writer) Raw(s string) error {
	if !strings.HasSuffix(s, "\n\n") {
		if strings.HasSuffix(s, "\n") {
			s += "\n"
		} else {
			s += "\n\n"
		}
	}
	if _, err := io.WriteString(sw.w, s); err != nil {
		return err
	}
	if sw.flusher != nil {
		sw.flusher.Flush()
	}
	return nil
}

// Done writes the OpenAI-style terminal marker.
func (sw *Writer) Done() error {
	return sw.Raw("data: [DONE]\n\n")
}

func encodeData(data any) (string, error) {
	switch v := data.(type) {
	case string:
		return v, nil
	case nil:
		return "", nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}

// SetEventStreamHeaders configures the response for SSE delivery.
func SetEventStreamHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
}
