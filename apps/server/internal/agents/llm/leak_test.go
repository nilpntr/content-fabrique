package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The API key and the gateway token must not appear in anything the layer produces: returned errors, log lines,
// cassettes and ledger rows. The scenarios try hard: the upstream echoes the key in its error body, the transport's own
// error quotes it, and the request is refused by a cap.
func TestSecretsNeverLeak(t *testing.T) {
	secrets := []string{testKey, testToken}
	dir := t.TempDir()
	vars := map[string]string{"LLM_MODE": "record", "LLM_CASSETTE_DIR": dir, "CF_AIG_TOKEN": testToken, "ANTHROPIC_BASE_URL": gatewayURL}

	scenarios := map[string]func(rg *rig){
		"success": func(*rig) {},
		"401 echoing the key": func(rg *rig) {
			rg.api.messages = func(int, recordedCall) fakeReply {
				return errReply(401, "authentication_error", "bad key "+testKey+" / "+testToken, nil)
			}
		},
		"429 until the budget is spent": func(rg *rig) {
			rg.api.messages = func(int, recordedCall) fakeReply {
				return errReply(429, "rate_limit_error", "throttled "+testKey, map[string]string{"retry-after": "200"})
			}
		},
		"transport error quoting the key": nil, // set below
		"cap refusal": func(rg *rig) {
			rg.api.countTokens = func(int, recordedCall) fakeReply { return fakeReply{Status: 200, Body: `{"input_tokens": 9000000}`} }
		},
		"unpriced model": nil, // handled below
	}

	for name, setup := range scenarios {
		t.Run(name, func(t *testing.T) {
			rg := newRig(t, vars)
			ws := newWorkspaceID()
			req := simpleRequest()
			switch name {
			case "transport error quoting the key":
				rg2 := newRig(t, vars, func(o *FactoryOptions) {
					o.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
						return nil, fmt.Errorf("dial failed for %s with %s %s", r.URL, testKey, testToken)
					})}
				})
				rg = rg2
			case "unpriced model":
				req.Model = "claude-mystery-9"
			default:
				setup(rg)
			}
			c, _ := rg.client(t, ws, "editor")
			_, err := c.Messages(context.Background(), req)
			_, cerr := c.CountTokens(context.Background(), req)

			var all strings.Builder
			if err != nil {
				all.WriteString(err.Error() + "\n")
				fmt.Fprintf(&all, "%+v %#v\n", err, err)
			}
			if cerr != nil {
				all.WriteString(cerr.Error() + "\n")
			}
			all.WriteString(rg.logs.String())
			fmt.Fprintf(&all, "%+v\n", rg.ledger.snapshot())
			for _, content := range readCassettes(t, dir) {
				all.WriteString(content)
			}
			mustNotContain(t, "output", all.String(), secrets...)

			if name == "transport error quoting the key" && (err == nil || !strings.Contains(err.Error(), "dial failed")) {
				t.Errorf("the transport error was lost: %v", err)
			}
		})
	}
}

// Scrubbing keeps the error chain usable.
func TestScrubbedErrorsStillUnwrap(t *testing.T) {
	rg := newRig(t, nil, func(o *FactoryOptions) {
		o.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, context.Canceled })}
	})
	c, _ := rg.client(t, newWorkspaceID(), "editor")
	if _, err := c.Messages(context.Background(), simpleRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// The factory is where the key is read: a fresh read per client, so a rotated ANTHROPIC_API_KEY is picked up and no
// client holds a stale copy beyond its own lifetime.
func TestFactoryReadsTheKeyWhenItBuildsAClient(t *testing.T) {
	m := map[string]string{"CF_ENV": "local", "ANTHROPIC_API_KEY": "sk-ant-first"}
	rg := newRig(t, nil, func(o *FactoryOptions) {
		o.Getenv = func(k string) string { return m[k] }
		o.Keys = nil
	})
	ws := newWorkspaceID()
	for _, key := range []string{"sk-ant-first", "sk-ant-second"} {
		m["ANTHROPIC_API_KEY"] = key
		c, _ := rg.client(t, ws, "editor")
		if _, err := c.Messages(context.Background(), simpleRequest()); err != nil {
			t.Fatal(err)
		}
		calls := rg.api.messageCalls()
		if got := calls[len(calls)-1].Header.Get("X-Api-Key"); got != key {
			t.Errorf("sent key %q, want %q", got, key)
		}
	}
}
