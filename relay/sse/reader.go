// Package sse provides a reusable Server-Sent Events reader/writer and the
// per-request streaming accumulator described in the modernization plan
// §7.1 (SSE reader) and §7.2 (event state).
//
// The reader never assumes that one underlying Read yields one complete JSON
// value: it follows the SSE framing (field lines, blank-line event boundary,
// multi-line data) and only hands complete event payloads upward.
package sse

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// Done is the OpenAI-style terminal payload.
const Done = "[DONE]"

// Event is one fully-delimited SSE event.
type Event struct {
	// Type is the value of the `event:` field, empty for the default type.
	Type string
	// Data is the joined `data:` payload (multiple data lines joined with \n).
	Data string
	// ID carries the optional `id:` field.
	ID string
}

// IsDone reports whether this event is the OpenAI [DONE] terminal marker.
func (e Event) IsDone() bool { return strings.TrimSpace(e.Data) == Done }

// ErrUpstreamEOF is returned when the upstream stream ended without an
// explicit terminal marker (network break / server closed connection).
var ErrUpstreamEOF = errors.New("sse: upstream ended without terminal marker")

// Reader parses an SSE byte stream into complete events.
type Reader struct {
	scanner *bufio.Scanner
	// sawEvent reports whether at least one data line was read, used to tell a
	// clean EOF apart from a mid-event break.
	sawEvent bool
}

// NewReader wraps r. It uses a growable buffer so a single JSON fragment
// larger than the default 64KB line limit is still handled.
func NewReader(r io.Reader) *Reader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	scanner.Split(bufio.ScanLines)
	return &Reader{scanner: scanner}
}

// Next returns the next complete event, or io.EOF at a clean stream end.
// Blank lines delimit events, so an event is assembled across any number of
// field lines until an empty line (or EOF) is hit.
func (rd *Reader) Next() (Event, error) {
	var (
		ev      Event
		dataBuf []string
		have    bool
	)
	for rd.scanner.Scan() {
		line := rd.scanner.Text()
		line = strings.TrimSuffix(line, "\r")

		// Comment / keep-alive line.
		if strings.HasPrefix(line, ":") {
			continue
		}
		// Event boundary.
		if line == "" {
			if have {
				rd.sawEvent = true
				return Event{Type: ev.Type, Data: strings.Join(dataBuf, "\n"), ID: ev.ID}, nil
			}
			continue
		}

		field, value := splitField(line)
		switch field {
		case "data":
			dataBuf = append(dataBuf, value)
			have = true
		case "event":
			ev.Type = value
			have = true
		case "id":
			ev.ID = value
			have = true
		case "retry":
			// retry is transport-level; ignore for relay purposes.
			have = true
		default:
			// Unknown field per SSE spec: ignore the line.
		}
	}
	if err := rd.scanner.Err(); err != nil {
		return Event{}, err
	}
	// Flush a trailing event that was not newline-terminated.
	if have {
		rd.sawEvent = true
		return Event{Type: ev.Type, Data: strings.Join(dataBuf, "\n"), ID: ev.ID}, nil
	}
	return Event{}, io.EOF
}

// splitField splits "field: value" into ("field","value"); a line with no
// colon is treated as a field with empty value. A single leading space after
// the colon is stripped, per the SSE spec, for every field value.
func splitField(line string) (string, string) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return line, ""
	}
	field := line[:idx]
	value := line[idx+1:]
	if strings.HasPrefix(value, " ") {
		value = value[1:]
	}
	return field, value
}
