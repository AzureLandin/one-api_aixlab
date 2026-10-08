package protocol

import (
	"fmt"
	"net/http"

	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// ProtocolError is a structured, parameter-level failure raised while parsing
// or rendering a client protocol. Implementation doc §6.4/§6.5 requires that
// unsupported fields are NAMED and rejected, never silently dropped or down-
// graded ("不做静默降级").
type ProtocolError struct {
	Message    string
	Param      string
	Type       string
	Code       string
	StatusCode int
}

func (e *ProtocolError) Error() string { return e.Message }

func invalidField(field, format string, args ...any) *ProtocolError {
	return &ProtocolError{
		Message:    fmt.Sprintf(format, args...),
		Param:      field,
		Type:       "invalid_request_error",
		Code:       "invalid_parameter",
		StatusCode: http.StatusBadRequest,
	}
}

func unsupportedField(field, format string, args ...any) *ProtocolError {
	return &ProtocolError{
		Message:    fmt.Sprintf(format, args...),
		Param:      field,
		Type:       "invalid_request_error",
		Code:       "unsupported_parameter",
		StatusCode: http.StatusBadRequest,
	}
}

// ToRelayError converts the protocol error into the pipeline-wide error type.
func (e *ProtocolError) ToRelayError() *relaymodel.ErrorWithStatusCode {
	if e == nil {
		return nil
	}
	status := e.StatusCode
	if status == 0 {
		status = http.StatusBadRequest
	}
	return &relaymodel.ErrorWithStatusCode{
		Error: relaymodel.Error{
			Message: e.Message,
			Type:    e.Type,
			Param:   e.Param,
			Code:    e.Code,
		},
		StatusCode: status,
	}
}
