package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/config"
)

// source says where a setting came from. Names that are in the env file with a value come from there; the process
// environment fills the rest (the merge rule of cfctl doctor).
type source string

const (
	fromFile    source = "the env file"
	fromProcess source = "the process environment"
	unset       source = "nowhere"
)

func sourceOf(name string, file map[string]string, lookup lookupEnv) source {
	if file[name] != "" {
		return fromFile
	}
	if v, ok := lookup(name); ok && v != "" {
		return fromProcess
	}
	if _, ok := file[name]; ok {
		return fromFile
	}
	return unset
}

// composeService is the host name of the Postgres container inside the compose network.
const composeService = "postgres"

// isLocalHost reports whether a database host is this machine (or the compose Postgres): the only places where
// dropping tables is a development convenience rather than an incident.
func isLocalHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", composeService:
		return true
	}
	return false
}

// destructiveRefusal decides whether `down` or `redo` may run. It returns "" to allow, else the reason to print.
//
// It fails closed. Without --yes the command runs only if CF_ENV is explicitly local or ci, the database is on this
// machine (or the compose service), and CF_ENV and DATABASE_MIGRATE_URL come from the same place. A missing CF_ENV is
// not "local": a shell that exported a production URL over a clean checkout must not get a free pass. CF_ENV and the
// URL from different places (say a staging URL in the shell and CF_ENV=local in .env) are the classic mix-up.
// Production is refused outright without --allow-production, and with it still needs --yes.
func destructiveRefusal(command, cfEnv string, cfSource, urlSource source, dsn string, yes, allowProd bool) string {
	if cfEnv == config.EnvProduction && !allowProd {
		return fmt.Sprintf("refusing to %s with CF_ENV=production. Pass --allow-production and --yes if you mean it", command)
	}
	if yes {
		return ""
	}
	var why []string
	switch {
	case cfSource == unset:
		why = append(why, "CF_ENV is not set")
	case cfEnv != config.EnvLocal && cfEnv != config.EnvCI:
		why = append(why, "CF_ENV="+cfEnv+" is not local or ci")
	}
	if u, err := url.Parse(dsn); err != nil || !isLocalHost(u.Hostname()) {
		why = append(why, "the database host is not this machine or the compose service")
	}
	if cfSource != urlSource {
		why = append(why, fmt.Sprintf("CF_ENV comes from %s but DATABASE_MIGRATE_URL from %s", cfSource, urlSource))
	}
	if len(why) == 0 {
		return ""
	}
	return fmt.Sprintf("refusing to %s without --yes (it drops tables and their data): %s", command, strings.Join(why, "; "))
}
