package doctor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/nilpntr/content-fabrique/internal/agents/llm/routing"
	"github.com/nilpntr/content-fabrique/internal/telemetry"
)

// job is one service check. It runs concurrently with the others.
type job = func(ctx context.Context) []Result

func static(rs ...Result) job { return func(context.Context) []Result { return rs } }

func single(f func(ctx context.Context) Result) job {
	return func(ctx context.Context) []Result { return []Result{f(ctx)} }
}

// serviceJobs lists the service checks in report order.
func (r *run) serviceJobs() []job {
	var jobs []job
	jobs = append(jobs,
		r.postgres("postgres DATABASE_URL", "DATABASE_URL", false),
		r.postgres("postgres DATABASE_DIRECT_URL", "DATABASE_DIRECT_URL", true),
		r.storage(),
		r.cloudflare(),
	)
	jobs = append(jobs, r.anthropicJobs()...)
	jobs = append(jobs, r.gatewayJobs()...)
	jobs = append(jobs, r.cfRESTJobs()...)
	jobs = append(jobs, r.clerk(), r.sandbox(), r.observability())
	return jobs
}

// gate decides whether a service can be checked. When the service's primary variable is unset and not required, the
// service is skipped; when it or another prerequisite is missing or malformed, the service reports that instead of
// making a call that cannot work. ok is true when the call should go ahead.
func (r *run) gate(name, primary string, others ...string) (Result, bool) {
	if r.vars[primary] == "" {
		if v, known := r.byName[primary]; known && !r.required(v) {
			return skipped(name, r.notRequiredReason(v)), false
		}
	}
	if res, bad := r.problem("service", name, append([]string{primary}, others...)...); bad {
		return res, false
	}
	return Result{}, true
}

// ---- HTTP helpers ----------------------------------------------------------------------------------------------

// doBody makes one request and returns the (size-limited) body. A non-2xx status is an httpStatusError.
func (r *run) doBody(ctx context.Context, method, u string, hdr map[string]string, body []byte) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, httpStatusError{code: resp.StatusCode}
	}
	return data, nil
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// ---- Postgres --------------------------------------------------------------------------------------------------

// postgres connects, runs SELECT 1 and, for the direct URL, LISTEN (which PgBouncer-style poolers cannot do).
func (r *run) postgres(name, envName string, listen bool) job {
	if res, bad := r.problem("service", name, envName); bad {
		return static(res)
	}
	dsn := r.vars[envName]
	return single(func(ctx context.Context) Result {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			return failed(name, "bad format")
		}
		cfg.ConnectTimeout = 5 * time.Second
		// No prepared statements: the pooled URL may sit behind a transaction pooler.
		cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			return failed(name, classify(err))
		}
		defer func() {
			cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = conn.Close(cctx)
		}()
		var one int
		if err := conn.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
			return failed(name, "SELECT 1: "+classify(err))
		}
		if listen {
			if _, err := conn.Exec(ctx, "LISTEN cf_doctor"); err != nil {
				return failed(name, "LISTEN: "+classify(err))
			}
			return okResult(name, "SELECT 1 and LISTEN")
		}
		return okResult(name, "SELECT 1")
	})
}

// ---- Object storage --------------------------------------------------------------------------------------------

// storage puts, gets and deletes a one-byte object, which proves the endpoint, signing and credentials.
func (r *run) storage() job {
	const name = "object-storage"
	if res, bad := r.problem("service", name, "R2_ENDPOINT", "R2_BUCKET", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY"); bad {
		return static(res)
	}
	endpoint, bucket := r.getenv("R2_ENDPOINT"), r.getenv("R2_BUCKET")
	id, secret := r.getenv("R2_ACCESS_KEY_ID"), r.getenv("R2_SECRET_ACCESS_KEY")
	jurisdiction := r.getenv("R2_JURISDICTION")
	return single(func(ctx context.Context) Result {
		// Explicit options only: the SDK's default config chain would read AWS_* variables and files.
		region := "auto" // R2
		if jurisdiction == "" {
			region = "us-east-1" // MinIO
		}
		client := s3.New(s3.Options{
			Region:       region,
			BaseEndpoint: aws.String(endpoint),
			Credentials:  credentials.NewStaticCredentialsProvider(id, secret, ""),
			// MinIO has no bucket subdomains. Real R2 is addressed by the jurisdiction's endpoint.
			UsePathStyle:               jurisdiction == "",
			HTTPClient:                 r.client,
			RetryMaxAttempts:           1,
			RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
			ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		})
		var rnd [8]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return failed(name, "failed")
		}
		key := "doctor/" + hex.EncodeToString(rnd[:])

		if _, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader([]byte{'x'}), ContentLength: aws.Int64(1),
		}); err != nil {
			return failed(name, "put: "+classify(err))
		}
		// Always try to remove the object, even if the get failed.
		defer func() {
			cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = client.DeleteObject(cctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		}()
		got, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		if err != nil {
			return failed(name, "get: "+classify(err))
		}
		data, err := io.ReadAll(io.LimitReader(got.Body, 16))
		_ = got.Body.Close()
		if err != nil {
			return failed(name, "get: "+classify(err))
		}
		if len(data) != 1 {
			return failed(name, "get: unexpected size")
		}
		if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
			return failed(name, "delete: "+classify(err))
		}
		return okResult(name, "put, get and delete")
	})
}

// ---- Cloudflare ------------------------------------------------------------------------------------------------

// cloudflare verifies the account API token, then lists the EU R2 buckets and the AI Gateways. It runs when the
// token is required (staging, production) or set.
func (r *run) cloudflare() job {
	const name = "cloudflare token"
	if res, ok := r.gate(name, "CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID"); !ok {
		return static(res)
	}
	token := r.getenv("CLOUDFLARE_API_TOKEN")
	base := strings.TrimRight(r.ep.Cloudflare, "/") + "/accounts/" + url.PathEscape(r.getenv("CLOUDFLARE_ACCOUNT_ID"))
	return func(ctx context.Context) []Result {
		body, err := r.doBody(ctx, http.MethodGet, base+"/tokens/verify", bearer(token), nil)
		if err != nil {
			return []Result{failed(name, classify(err)), skipped("cloudflare r2 buckets", "token not verified"), skipped("cloudflare ai gateways", "token not verified")}
		}
		var v struct {
			Success bool `json:"success"`
			Result  struct {
				Status string `json:"status"`
			} `json:"result"`
		}
		verify := okResult(name, "token verify")
		if json.Unmarshal(body, &v) != nil || !v.Success || v.Result.Status != "active" {
			verify = failed(name, "token not active")
		}

		out := []Result{verify}
		list := func(label, path string, hdr map[string]string) Result {
			h := bearer(token)
			for k, val := range hdr {
				h[k] = val
			}
			if _, err := r.doBody(ctx, http.MethodGet, base+path, h, nil); err != nil {
				return failed(label, classify(err))
			}
			return okResult(label, "listed")
		}
		out = append(out,
			list("cloudflare r2 buckets", "/r2/buckets", map[string]string{"cf-r2-jurisdiction": "eu"}),
			list("cloudflare ai gateways", "/ai-gateway/gateways", nil),
		)
		return out
	}
}

// ---- Models: Anthropic, the AI Gateway and Cloudflare's REST path ----------------------------------------------

// profileModels returns the distinct native model IDs of the active ROUTING_PROFILE.
func (r *run) profileModels() ([]string, error) {
	if r.opts.RoutingFS == nil {
		return nil, fmt.Errorf("no routing config")
	}
	p, err := routing.LoadProfile(r.opts.RoutingFS, r.getenv("ROUTING_PROFILE"))
	if err != nil {
		return nil, err
	}
	return p.Models(), nil
}

// countTokens asks the Messages API how many tokens a tiny prompt takes for the model. It is free and generates
// nothing. The client is built with explicit options only, so the SDK's own environment defaults cannot change
// which endpoint or key is tested.
func (r *run) countTokens(ctx context.Context, baseURL, key, model string, hdr map[string]string) error {
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(key),
		option.WithBaseURL(baseURL),
		option.WithHTTPClient(r.client),
		option.WithMaxRetries(0),
	}
	for k, v := range hdr {
		opts = append(opts, option.WithHeader(k, v))
	}
	client := anthropic.NewClient(opts...)
	_, err := client.Messages.CountTokens(ctx, anthropic.MessageCountTokensParams{
		Model:    model,
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("ping"))},
	})
	return err
}

// anthropicJobs checks every model of the active profile by its native ID, always calling api.anthropic.com
// directly, whatever ANTHROPIC_BASE_URL says.
func (r *run) anthropicJobs() []job {
	const name = "anthropic"
	if res, ok := r.gate(name, "ANTHROPIC_API_KEY", "ROUTING_PROFILE"); !ok {
		return []job{static(res)}
	}
	models, err := r.profileModels()
	if err != nil {
		return []job{static(failed(name, "routing profile unreadable"))}
	}
	key := r.getenv("ANTHROPIC_API_KEY")
	var jobs []job
	for _, m := range models {
		jobs = append(jobs, single(func(ctx context.Context) Result {
			label := name + " " + m
			if err := r.countTokens(ctx, r.ep.Anthropic, key, m, nil); err != nil {
				return failed(label, classify(err))
			}
			return okResult(label, "count_tokens")
		}))
	}
	return jobs
}

// gatewayJobs repeats the model check through ANTHROPIC_BASE_URL (the Cloudflare AI Gateway) with the gateway token.
func (r *run) gatewayJobs() []job {
	const name = "ai-gateway"
	switch {
	case r.opts.Env == "ci" && r.vars["ANTHROPIC_API_KEY"] == "":
		return []job{static(skipped(name, ciNotUsedReason))}
	case r.vars["ANTHROPIC_BASE_URL"] == "":
		return []job{static(skipped(name, "ANTHROPIC_BASE_URL empty (direct calls)"))}
	}
	if res, bad := r.problem("service", name, "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "CF_AIG_TOKEN", "ROUTING_PROFILE"); bad {
		return []job{static(res)}
	}
	models, err := r.profileModels()
	if err != nil {
		return []job{static(failed(name, "routing profile unreadable"))}
	}
	key, base, token := r.getenv("ANTHROPIC_API_KEY"), r.getenv("ANTHROPIC_BASE_URL"), r.getenv("CF_AIG_TOKEN")
	var jobs []job
	for _, m := range models {
		jobs = append(jobs, single(func(ctx context.Context) Result {
			label := name + " " + m
			hdr := map[string]string{"cf-aig-authorization": "Bearer " + token}
			if err := r.countTokens(ctx, base, key, m, hdr); err != nil {
				return failed(label, classify(err))
			}
			return okResult(label, "count_tokens via gateway")
		}))
	}
	return jobs
}

// cfRESTJobs sends a one-token request per model by its Cloudflare name (anthropic/claude-opus-5.5), when
// CF_AI_API_TOKEN is set. The endpoint is Workers AI's OpenAI-compatible route; whether Cloudflare serves Claude 5.5
// there is unverified, which is what this check is for.
func (r *run) cfRESTJobs() []job {
	const name = "cloudflare-rest"
	if res, ok := r.gate(name, "CF_AI_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID", "ROUTING_PROFILE"); !ok {
		return []job{static(res)}
	}
	models, err := r.profileModels()
	var catalog routing.Catalog
	if err == nil {
		catalog, err = routing.LoadCatalog(r.opts.RoutingFS)
	}
	if err != nil {
		return []job{static(failed(name, "routing config unreadable"))}
	}
	token := r.getenv("CF_AI_API_TOKEN")
	endpoint := strings.TrimRight(r.ep.Cloudflare, "/") + "/accounts/" + url.PathEscape(r.getenv("CLOUDFLARE_ACCOUNT_ID")) + "/ai/v1/chat/completions"
	var jobs []job
	for _, m := range models {
		entry, known := catalog[m]
		label := name + " " + entry.Cloudflare
		if !known {
			jobs = append(jobs, static(failed(name+" "+m, "not in models.yaml")))
			continue
		}
		jobs = append(jobs, single(func(ctx context.Context) Result {
			body, _ := json.Marshal(map[string]any{
				"model":      entry.Cloudflare,
				"max_tokens": 1,
				"messages":   []map[string]string{{"role": "user", "content": "ping"}},
			})
			hdr := bearer(token)
			hdr["Content-Type"] = "application/json"
			if _, err := r.doBody(ctx, http.MethodPost, endpoint, hdr, body); err != nil {
				return failed(label, classify(err))
			}
			return okResult(label, "1-token request")
		}))
	}
	return jobs
}

// ---- Clerk, sandbox gateway, observability ---------------------------------------------------------------------

// clerk lists one user through the Backend API, which proves the secret key.
func (r *run) clerk() job {
	const name = "clerk"
	if res, ok := r.gate(name, "CLERK_SECRET_KEY"); !ok {
		return static(res)
	}
	key := r.getenv("CLERK_SECRET_KEY")
	return single(func(ctx context.Context) Result {
		if _, err := r.doBody(ctx, http.MethodGet, strings.TrimRight(r.ep.Clerk, "/")+"/v1/users?limit=1", bearer(key), nil); err != nil {
			return failed(name, classify(err))
		}
		return okResult(name, "backend API")
	})
}

// sandbox calls /healthz on the sandbox gateway Worker, from phase P4 on.
func (r *run) sandbox() job {
	const name = "sandbox-gateway"
	if r.opts.Phase < 4 {
		return static(skipped(name, "not checked before P4"))
	}
	if res, ok := r.gate(name, "SANDBOX_GATEWAY_URL", "SANDBOX_GATEWAY_TOKEN"); !ok {
		return static(res)
	}
	u := strings.TrimRight(r.getenv("SANDBOX_GATEWAY_URL"), "/") + "/healthz"
	token := r.getenv("SANDBOX_GATEWAY_TOKEN")
	return single(func(ctx context.Context) Result {
		if _, err := r.doBody(ctx, http.MethodGet, u, bearer(token), nil); err != nil {
			return failed(name, classify(err))
		}
		return okResult(name, "healthz")
	})
}

// observability sends one test span to the OTLP endpoint and expects a 2xx.
func (r *run) observability() job {
	const name = "observability"
	if res, ok := r.gate(name, "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_HEADERS"); !ok {
		return static(res)
	}
	endpoint := strings.TrimRight(r.getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), "/") + "/v1/traces"
	headers := parseOTLPHeaders(r.getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	return single(func(ctx context.Context) Result {
		body, err := testSpan()
		if err != nil {
			return failed(name, "failed")
		}
		headers["Content-Type"] = "application/x-protobuf"
		if _, err := r.doBody(ctx, http.MethodPost, endpoint, headers, body); err != nil {
			return failed(name, classify(err))
		}
		return okResult(name, "test span accepted")
	})
}

// parseOTLPHeaders reads OTEL_EXPORTER_OTLP_HEADERS: comma-separated key=value pairs, values percent-encoded.
func parseOTLPHeaders(s string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			continue
		}
		if dec, err := url.PathUnescape(strings.TrimSpace(v)); err == nil {
			v = dec
		}
		out[k] = strings.TrimSpace(v)
	}
	return out
}

// testSpan builds an OTLP/protobuf export request with one span named cfctl.doctor.
func testSpan() ([]byte, error) {
	var trace [16]byte
	var span [8]byte
	if _, err := rand.Read(trace[:]); err != nil {
		return nil, err
	}
	if _, err := rand.Read(span[:]); err != nil {
		return nil, err
	}
	now := uint64(time.Now().UnixNano()) //nolint:gosec // wall clock, always positive
	req := &collectortrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{
			Key:   "service.name",
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: telemetry.ServiceCfctl}},
		}}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			TraceId: trace[:], SpanId: span[:], Name: "cfctl.doctor", Kind: tracepb.Span_SPAN_KIND_INTERNAL,
			StartTimeUnixNano: now, EndTimeUnixNano: now + 1000,
		}}}},
	}}}
	return proto.Marshal(req)
}
