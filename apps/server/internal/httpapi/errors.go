package httpapi

import "net/http"

// The error body of every /v1 endpoint, until the OpenAPI contract of P0-08 takes it over:
//
//	{ "error": { "code": "no_active_workspace", "message": "..." } }
//
// code is stable and meant for programs (the web client branches on it); message is for people and may change.
// Neither ever contains request data, a token or a secret.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteJSON writes v as a JSON response. Responses of the API are never cached.
func WriteJSON(w http.ResponseWriter, status int, v any) { writeJSON(w, status, v) }

// WriteError writes the standard error body with the status.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}
