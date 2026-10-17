package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/store"
)

// LoginVerifier is the part of the store that asks what the app login may do.
type LoginVerifier interface {
	Ping(ctx context.Context) error
	VerifyAppLogin(ctx context.Context) error
}

// strictLogin reports whether the environment must run on an unprivileged app login: staging and production, where
// the migrate/app role split exists. Locally, in ci and in bench one login does both on purpose.
func strictLogin(env string) bool { return env == config.EnvStaging || env == config.EnvProduction }

// StartupLoginCheck refuses to start when the app login is privileged. When Postgres cannot be asked (it is down),
// the process starts anyway and the readiness check keeps failing until the question is answered, because a database
// outage must not become a crash loop.
func StartupLoginCheck(ctx context.Context, v LoginVerifier, env string, logger *slog.Logger) error {
	if !strictLogin(env) {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := v.VerifyAppLogin(cctx)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrPrivilegedLogin):
		return fmt.Errorf("refusing to start in %s: %w. DATABASE_URL must be the app role (docs/06-setup-and-credentials.md, section 3.4)", env, err)
	default:
		logger.Warn("cannot verify the app login yet; /readyz fails until it can", "error", err)
		return nil
	}
}

// PostgresCheck is the "postgres" readiness check: a ping, and on staging and production also proof, once, that the
// login is unprivileged. A privileged login fails readiness with an error in the log, every time it is asked, so the
// instance never receives traffic.
func PostgresCheck(v LoginVerifier, env string, logger *slog.Logger) func(ctx context.Context) error {
	var verified atomic.Bool
	return func(ctx context.Context) error {
		if err := v.Ping(ctx); err != nil {
			return err
		}
		if !strictLogin(env) || verified.Load() {
			return nil
		}
		if err := v.VerifyAppLogin(ctx); err != nil {
			if errors.Is(err, store.ErrPrivilegedLogin) {
				logger.Error("DATABASE_URL login is privileged: refusing to serve (use the app role, docs/06 section 3.4)", "env", env, "error", err)
			}
			return err
		}
		verified.Store(true)
		return nil
	}
}
