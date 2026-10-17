// Package api holds the Go side of the OpenAPI contract (P0-08, D-14): the request and response models and the strict
// server interface that internal/auth implements. api.gen.go is generated from packages/schemas/openapi.yaml; do not edit it.
// Change the spec, run `task gen:api` and commit the result. CI fails when the generated code is out of date.
package api

//go:generate go tool oapi-codegen -config oapi-codegen.yaml ../../../../packages/schemas/openapi.yaml
