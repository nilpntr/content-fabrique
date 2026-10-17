package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/ids"
	"github.com/nilpntr/content-fabrique/internal/spend"
)

// ClientFactory hands out the Client for a workspace and a stage, with the label of who pays for its calls. Agents
// and tools hold a Client and never see a key (decision D-18, architecture 11a).
type ClientFactory interface {
	For(ctx context.Context, workspace ids.WorkspaceID, stage string) (Client, Billing, error)
}

// KeyResolver finds the API key that pays for a workspace's calls, and who that is. It is the seam P4-00b fills: a
// resolver that looks a BYOK workspace's key up in the vault (billed_to workspace_key) and falls back to the
// managed one. Implementations return a *NoKeyError when there is no key. The key is returned to the factory only.
type KeyResolver interface {
	ResolveKey(ctx context.Context, workspace ids.WorkspaceID) (key string, billing Billing, err error)
}

// managedKeys is the P4-00a resolver: every workspace is managed and uses ANTHROPIC_API_KEY. This is the one place
// that variable is read.
type managedKeys struct {
	getenv  func(string) string
	billing Billing
}

func (k managedKeys) ResolveKey(context.Context, ids.WorkspaceID) (string, Billing, error) {
	key := strings.TrimSpace(k.getenv("ANTHROPIC_API_KEY"))
	if key == "" {
		return "", k.billing, &NoKeyError{Variable: "ANTHROPIC_API_KEY"}
	}
	return key, k.billing, nil
}

// FactoryOptions builds a Factory.
type FactoryOptions struct {
	Settings Settings
	// Getenv reads ANTHROPIC_API_KEY and CF_AIG_TOKEN when a client is built (pass os.Getenv). Nothing else holds them.
	Getenv func(string) string
	Prices *PriceTable
	// Ledger is where the spend holds, the counters and the usage_ledger rows live (*store.Store): every live or recorded call
	// reserves its worst case there and settles into one row. Required unless Settings.Mode is replay.
	Ledger spend.Backend
	// Images resolves Image.Ref before a request is sent. Optional.
	Images ImageResolver
	// Keys replaces the managed key resolver (P4-00b). Optional.
	Keys KeyResolver
	// HTTPClient replaces the SDK's transport; tests pass a fake one. Optional.
	HTTPClient *http.Client
	Logger     *slog.Logger

	retry retryPolicy
	now   func() time.Time
}

// Factory is the ClientFactory of P4-00a.
type Factory struct {
	opts     FactoryOptions
	keys     KeyResolver
	limiters limiterRegistry
	gate     *spend.Gate
}

var _ ClientFactory = (*Factory)(nil)

// NewFactory checks the options and returns the factory.
func NewFactory(opts FactoryOptions) (*Factory, error) {
	if opts.Prices == nil {
		return nil, errors.New("llm: a price table is required")
	}
	if opts.Getenv == nil {
		return nil, errors.New("llm: Getenv is required")
	}
	if opts.Settings.Mode != ModeReplay && opts.Ledger == nil {
		return nil, errors.New("llm: a ledger is required: every call writes a usage_ledger row")
	}
	billing, err := BillingFor(opts.Settings.Env)
	if err != nil {
		return nil, err
	}
	// Fail closed (D-31): an unpriced paid provider is never allowed to run in staging or production, whatever the settings say.
	if opts.Settings.AllowUnpriced && !config.UnpricedAllowedIn(opts.Settings.Env) {
		return nil, errors.New("llm: LLM_ALLOW_UNPRICED is honoured only in local and ci")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.now == nil {
		opts.now = time.Now
	}
	keys := opts.Keys
	if keys == nil {
		keys = managedKeys{getenv: opts.Getenv, billing: billing}
	}
	f := &Factory{opts: opts, keys: keys}
	if opts.Ledger != nil {
		f.gate = spend.NewGate(opts.Ledger, spend.Options{Caps: opts.Settings.SpendCaps(), Now: opts.now, Logger: opts.Logger})
	}
	return f, nil
}

// For returns the Client for the workspace and stage, and who pays for its calls. It returns a *NoKeyError (errors.Is
// ErrNoKey) when LLM_MODE needs the API and no key is configured. LLM_MODE=replay needs no key.
func (f *Factory) For(ctx context.Context, workspace ids.WorkspaceID, stage string) (Client, Billing, error) {
	if !stageRE.MatchString(stage) {
		return nil, "", fmt.Errorf("llm: stage %q is not a plain name (lowercase letters, digits, dot, dash, underscore)", stage)
	}
	if workspace.IsZero() {
		return nil, "", errors.New("llm: a workspace is required")
	}
	s := f.opts.Settings

	var (
		key     string
		billing Billing
		inner   Client
	)
	if s.Mode == ModeReplay {
		billing, _ = BillingFor(s.Env)
	} else {
		var err error
		key, billing, err = f.keys.ResolveKey(ctx, workspace)
		if err != nil {
			var nk *NoKeyError
			if errors.As(err, &nk) {
				nk.Mode = s.Mode
			}
			return nil, "", err
		}
		sdk := newAnthropicClient(anthropicConfig{
			APIKey: key, BaseURL: s.BaseURL, GatewayToken: strings.TrimSpace(f.opts.Getenv("CF_AIG_TOKEN")),
			HTTPClient: f.opts.HTTPClient, Workspace: workspace.String(), Stage: stage,
		})
		inner = newLimited(sdk, f.limiters.forKey(key, s.Concurrency), f.opts.retry, f.opts.Logger)
	}
	if s.Mode != ModeLive {
		cc, err := newCassetteClient(inner, s.Mode, s.CassetteDir, stage, f.opts.Logger)
		if err != nil {
			return nil, "", err
		}
		inner = cc
	}
	return &metered{
		inner: inner, ws: workspace, stage: stage, billing: billing, mode: s.Mode,
		prices: f.opts.Prices, gate: f.gate, images: f.opts.Images,
		maxRequest: s.MaxRequestMicros, allowUnpriced: s.AllowUnpriced,
		now: f.opts.now, log: f.opts.Logger,
	}, billing, nil
}
