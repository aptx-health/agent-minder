package controlapi

import "fmt"

type ErrorCode string

const (
	ErrorInvalidCursor       ErrorCode = "invalid_cursor"
	ErrorInvalidLimit        ErrorCode = "invalid_limit"
	ErrorNotFound            ErrorCode = "not_found"
	ErrorRateLimited         ErrorCode = "rate_limited"
	ErrorProviderUnavailable ErrorCode = "provider_unavailable"
	ErrorInternal            ErrorCode = "internal_error"
)

type ErrorDetail struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

type ErrorEnvelope struct {
	Error ErrorDetail `json:"error"`
}

// ContractError carries safe client text. Internal failures must be logged
// separately and must never be placed in Message.
type ContractError struct {
	Code    ErrorCode
	Message string
}

func (e *ContractError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

func NewErrorEnvelope(code ErrorCode, message string) ErrorEnvelope {
	return ErrorEnvelope{Error: ErrorDetail{Code: code, Message: message}}
}
