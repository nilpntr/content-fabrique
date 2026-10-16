package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"

	"github.com/anthropics/anthropic-sdk-go"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/jackc/pgx/v5/pgconn"
)

// httpStatusError is a non-2xx answer from a plain HTTP call.
type httpStatusError struct{ code int }

func (e httpStatusError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

// httpReason is the category for an HTTP status.
func httpReason(code int) string {
	switch code {
	case 401, 403:
		return fmt.Sprintf("auth rejected (HTTP %d)", code)
	case 404:
		return "not found (HTTP 404)"
	}
	return fmt.Sprintf("HTTP %d", code)
}

// classify turns an error into a fixed category. It never returns err.Error(), because errors from drivers and
// SDKs can embed connection strings, URLs with credentials, or key fragments.
func classify(err error) string {
	var apiErr *anthropic.Error
	var statusErr httpStatusError
	var s3Err *awshttp.ResponseError
	var pgErr *pgconn.PgError
	var netErr net.Error
	var urlErr *url.Error
	var opErr *net.OpError
	var dnsErr *net.DNSError

	switch {
	case errors.As(err, &apiErr):
		return httpReason(apiErr.StatusCode)
	case errors.As(err, &statusErr):
		return httpReason(statusErr.code)
	case errors.As(err, &s3Err) && s3Err.HTTPStatusCode() > 0:
		return httpReason(s3Err.HTTPStatusCode())
	case errors.As(err, &pgErr):
		switch pgErr.Code {
		case "28P01", "28000":
			return "auth rejected"
		case "3D000":
			return "database not found"
		}
		return "database error"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case errors.As(err, &urlErr), errors.As(err, &opErr), errors.As(err, &dnsErr),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.As(err, &netErr):
		return "unreachable"
	}
	return "failed"
}
