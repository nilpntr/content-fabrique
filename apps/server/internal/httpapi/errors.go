package httpapi

import (
	"net/http"

	"github.com/nilpntr/content-fabrique/internal/api"
)

// The error body of every /v1 endpoint is the Error schema of the OpenAPI contract (api.Error):
//
//	{ "error": { "code": "no_active_workspace", "message": "..." } }
//
// code is stable and meant for programs (the web client branches on it); message is for people and may change. Neither
// ever contains request data, a token or a secret. The code is an api.ErrorCode, generated from the spec's enum, so a code
// the spec does not list does not compile.

// ErrorBody is the standard error body.
func ErrorBody(code api.ErrorCode, message string) api.Error {
	return api.Error{Error: api.ErrorDetail{Code: code, Message: message}}
}

// WriteJSON writes v as a JSON response. Responses of the API are never cached.
func WriteJSON(w http.ResponseWriter, status int, v any) { writeJSON(w, status, v) }

// WriteError writes the standard error body with the status.
func WriteError(w http.ResponseWriter, status int, code api.ErrorCode, message string) {
	writeJSON(w, status, ErrorBody(code, message))
}
