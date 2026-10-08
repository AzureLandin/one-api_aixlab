package sse

import (
	"io"
	"strings"
	"testing"
)

func collect(t *testing.T, r io.Reader) ([]Event, error) {
	t.Helper()
	rd := NewReader(r)
	var evs []Event
	for {
		ev, err := rd.Next()
		if err == io.EOF {
			return evs, nil
		}
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
}

func TestReader_MultiLineDataAndDone(t *testing.T) {
	in := "data: line1\ndata: line2\n\ndata: [DONE]\n\n"
	evs, err := collect(t, strings.NewReader(in))
	if err != nil {
		t.Fatalf("collect: %s", err)
	}
	if len(evs) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(evs), evs)
	}
	if evs[0].Data != "line1\nline2" {
		t.Fatalf("multi-line data not joined: %q", evs[0].Data)
	}
	if !evs[1].IsDone() {
		t.Fatalf("terminal marker not recognized: %+v", evs[1])
	}
}

func TestReader_EventType(t *testing.T) {
	in := "event: response.created\ndata: {\"id\":\"x\"}\n\n"
	evs, _ := collect(t, strings.NewReader(in))
	if len(evs) != 1 || evs[0].Type != "response.created" {
		t.Fatalf("event type not parsed: %+v", evs)
	}
}

func TestReader_CommentKeepAlive(t *testing.T) {
	in := ": keep-alive\ndata: hello\n\n"
	evs, _ := collect(t, strings.NewReader(in))
	if len(evs) != 1 || evs[0].Data != "hello" {
		t.Fatalf("comment line should be ignored: %+v", evs)
	}
}

func TestReader_TrailingEventWithoutBlankLine(t *testing.T) {
	// JSON fragment that ends at EOF without a trailing blank line.
	in := "data: {\"a\":1}"
	evs, err := collect(t, strings.NewReader(in))
	if err != nil {
		t.Fatalf("unexpected err: %s", err)
	}
	if len(evs) != 1 || evs[0].Data != `{"a":1}` {
		t.Fatalf("trailing event not flushed: %+v", evs)
	}
}

func TestReader_JSONAcrossEventBoundaries(t *testing.T) {
	// Each SSE event is a complete JSON; the reader must not assume one Read
	// equals one JSON. We feed a stream of chunk events.
	in := "data: {\"c\":1}\n\ndata: {\"c\":2}\n\ndata: [DONE]\n\n"
	evs, _ := collect(t, strings.NewReader(in))
	if len(evs) != 3 {
		t.Fatalf("want 3 events got %d", len(evs))
	}
}

func TestWriter_EventFraming(t *testing.T) {
	var buf strings.Builder
	w := NewWriter(&buf)
	if err := w.Event("response.output_text.delta", map[string]any{"delta": "hi"}); err != nil {
		t.Fatalf("write: %s", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "event: response.output_text.delta\n") {
		t.Fatalf("missing event line: %q", out)
	}
	if !strings.Contains(out, "data: {\"delta\":\"hi\"}") {
		t.Fatalf("missing data line: %q", out)
	}
	if !strings.HasSuffix(out, "\n\n") {
		t.Fatalf("events must end with blank line: %q", out)
	}
}

func TestWriter_MultiLineDataFraming(t *testing.T) {
	var buf strings.Builder
	w := NewWriter(&buf)
	_ = w.Event("x", "a\nb")
	if buf.String() != "event: x\ndata: a\ndata: b\n\n" {
		t.Fatalf("multi-line data framing wrong: %q", buf.String())
	}
}
