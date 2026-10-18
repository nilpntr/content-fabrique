package telemetry

import (
	"regexp"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

// What a span may carry (docs/design/observability-v1.md section 3).
//
// By default the spans of the agents carry metadata only: ids, models, token counts, costs, stop reasons, tool names, check ids and
// their pass or fail. The prompts, the model's answers, the tools' arguments and results and pictures of the footage are content:
// they are put on spans only when content capture is on, which OTEL_TRACE_CONTENT=1 turns on and only with CF_ENV=local
// (ContentAllowed). Staging and production export to Axiom, and must never start shipping a creator's brief or frames there.

var contentOn atomic.Bool

// SetContent turns content capture on or off for the process. Setup calls it from Options.TraceContent; tests call it directly.
func SetContent(on bool) { contentOn.Store(on) }

// Content says whether spans may carry content (messages, tool arguments and results, thumbnails).
func Content() bool { return contentOn.Load() }

// ContentAllowed is the one rule for OTEL_TRACE_CONTENT: "1" turns content capture on, and only in the local environment. Anything
// else, and any other environment, leaves it off.
func ContentAllowed(cfEnv, value string) bool {
	return strings.TrimSpace(value) == "1" && cfEnv == "local"
}

// Size caps of span attributes. A span is exported in one OTLP request with the others of its batch, so these keep a long agent run's
// spans small enough to export and to read: the text of one message part, a JSON value (tool arguments, a tool result, a run's
// input or output), and the thumbnails of one model call.
const (
	MaxTextBytes      = 16 << 10
	MaxValueBytes     = 32 << 10
	MaxThumbnails     = 32
	MaxThumbnailBytes = 8 << 10
)

// Cap cuts s to at most n bytes on a rune boundary and says how much was cut. It never returns invalid UTF-8.
func Cap(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "… [cut " + itoa(len(s)-cut) + " bytes]"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// presigned matches the query string of a URL that carries a signature or a credential: S3 and R2 presigned URLs (X-Amz-*), and the
// usual names of signed or tokenised links. The URL is kept up to the "?", so a span still says which object it was.
var presigned = regexp.MustCompile(`(?i)(https?://[^\s"'<>?]+)\?[^\s"'<>]*(x-amz-(signature|credential|security-token)|signature=|sig=|token=|access_token=|key=|expires=)[^\s"'<>]*`)

// bearer matches an authorization header value or a bearer token written in text.
var bearer = regexp.MustCompile(`(?i)\b(bearer|authorization:)\s+[A-Za-z0-9._~+/=-]{8,}`)

// apiKey matches the shapes of the keys this stack holds (Anthropic, Clerk, Stripe and webhook secrets), wherever they appear.
var apiKey = regexp.MustCompile(`\b(sk-ant-[A-Za-z0-9_-]{8,}|sk_(test|live)_[A-Za-z0-9]{8,}|rk_(test|live)_[A-Za-z0-9]{8,}|whsec_[A-Za-z0-9]{8,})`)

// Redact removes what must never leave the process in a span: the query string of a presigned URL, bearer tokens and API keys. It is
// applied to every text a span carries, content or not.
func Redact(s string) string {
	s = presigned.ReplaceAllString(s, "$1?[redacted]")
	s = bearer.ReplaceAllString(s, "$1 [redacted]")
	return apiKey.ReplaceAllString(s, "[redacted-key]")
}

// Clean is Redact and then Cap: what every text value of a span goes through.
func Clean(s string, n int) string { return Cap(Redact(s), n) }
