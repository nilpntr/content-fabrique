package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nilpntr/content-fabrique/internal/config"
	"github.com/nilpntr/content-fabrique/internal/doctor"
	"github.com/nilpntr/content-fabrique/internal/envfile"
	"github.com/nilpntr/content-fabrique/internal/jobs"
	"github.com/nilpntr/content-fabrique/internal/store"
)

const migrateUsage = `Usage:
  cfctl migrate [--env-file PATH] <command> [--to VERSION]

Applies the SQL migrations embedded in the binary (internal/store/migrations) to the database named by
DATABASE_MIGRATE_URL: a direct connection as the role that owns the schema. Nothing else reads that variable.
Each migration grants the app's group role (<database>_app, cf_app locally) its rights, so the app's own login role never needs DDL.

River's job-queue tables are migrated here too, as a second line that River versions itself ("river/001_..."): after the app
migrations on up, and after them when down removes everything. After every up the app's group role gets its rights on River's
tables (and only reads river_migration), so the worker and the api need no DDL either.

Commands:
  up        apply every pending migration, River's included (--to VERSION stops after that app version and leaves River alone)
  down      roll back the latest app migration (--to VERSION rolls back everything above it; --to 0, or a down that
            reaches 0, then also removes River's tables, after the app migrations are gone)
  redo      roll back the latest app migration and apply it again (River is not touched)
  status    list every migration, River's included, and whether it is applied
  version   print the latest applied app version (0 when none)

  --env-file  the env file; default .env at the repository root, and a relative name is taken from there too
              (task migrate runs in apps/server). A non-empty value in the file wins; the process
              environment fills a name the file lacks or leaves empty (the same rule as cfctl doctor).
              Staging: --env-file .env.staging
  --to        a target version of the app migrations, for up and down (River's line has its own numbers)
  --yes       down and redo drop tables and data, so they ask for this unless CF_ENV is set to local or ci, the
              database host is localhost, 127.0.0.1, ::1 or the compose service "postgres", and CF_ENV and
              DATABASE_MIGRATE_URL come from the same place (both the env file, or both the environment)
  --allow-production
              down and redo are refused when CF_ENV is production without this, and with it they still need --yes

Every run prints the target as host:port/database with CF_ENV, where CF_ENV and the URL came from, and the env file.
Never prints the URL or the password.
`

func cmdMigrate(args []string, stdout, stderr io.Writer, lookup lookupEnv) int {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, migrateUsage) }
	envFileArg := flags.String("env-file", "", "env file")
	toArg := flags.String("to", "", "target version")
	yes := flags.Bool("yes", false, "confirm down and redo on staging or production")
	allowProd := flags.Bool("allow-production", false, "allow down and redo on production")

	// Flags may come before or after the command: `migrate --env-file x down --to 0` and `migrate down --to 0`.
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	command := flags.Arg(0)
	if command == "" {
		_, _ = fmt.Fprint(stderr, migrateUsage)
		return 2
	}
	if command == "help" {
		_, _ = fmt.Fprint(stdout, migrateUsage)
		return 0
	}
	if err := flags.Parse(flags.Args()[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "cfctl migrate: unexpected argument %q\n", flags.Arg(0))
		return 2
	}

	var to *int64
	if *toArg != "" {
		v, err := strconv.ParseInt(*toArg, 10, 64)
		if err != nil || v < 0 {
			_, _ = fmt.Fprintf(stderr, "cfctl migrate: --to must be a version number (0 or more)\n")
			return 2
		}
		to = &v
	}
	switch command {
	case "up", "down":
	case "redo", "status", "version":
		if to != nil {
			_, _ = fmt.Fprintf(stderr, "cfctl migrate: --to applies to up and down only\n")
			return 2
		}
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl migrate: unknown command %q\n\n%s", command, migrateUsage)
		return 2
	}

	root, example, code := loadExample(stderr)
	if code != 0 {
		return code
	}
	explicit := *envFileArg != ""
	path := *envFileArg
	if !explicit {
		path = filepath.Join(root, ".env")
	} else if !filepath.IsAbs(path) {
		// `task migrate` runs in apps/server, so a relative name is taken from the repository root, where the env files live.
		path = filepath.Join(root, path)
	}
	file := map[string]string{}
	parsed, err := envfile.ParseFile(path)
	switch {
	case err == nil:
		file = parsed
	case errors.Is(err, fs.ErrNotExist) && !explicit:
		// process environment only
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl migrate: cannot read the env file %s\n", displayPath(root, path))
		return 2
	}
	vars := doctor.MergeEnv(example, file, lookup)

	// Everything printed below goes through the redactor, so a connection error that quotes a value cannot leak it.
	redactor := doctor.NewRedactor(example, vars)
	out, errOut := redactor.Writer(stdout), redactor.Writer(stderr)

	dsn := vars["DATABASE_MIGRATE_URL"]
	if dsn == "" {
		_, _ = fmt.Fprintf(errOut, "cfctl migrate: DATABASE_MIGRATE_URL is not set in %s or the environment\n", displayPath(root, path))
		return 2
	}
	if err := config.ValidatePostgresURL(dsn); err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl migrate: DATABASE_MIGRATE_URL: %v\n", err)
		return 2
	}
	target := describeTarget(dsn)

	// Rolling back drops tables and their data. It fails closed: without --yes it runs only when everything says
	// "this is my laptop" (see destructiveRefusal).
	cfEnv := vars["CF_ENV"]
	cfSource := sourceOf("CF_ENV", file, lookup)
	urlSource := sourceOf("DATABASE_MIGRATE_URL", file, lookup)
	if command == "down" || command == "redo" {
		if msg := destructiveRefusal(command, cfEnv, cfSource, urlSource, dsn, *yes, *allowProd); msg != "" {
			_, _ = fmt.Fprintf(errOut, "cfctl migrate: %s (env file %s)\n", msg, displayPath(root, path))
			return 2
		}
	}

	ctx, stop := signalContext()
	defer stop()

	migrator, err := store.NewMigrator(dsn)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl migrate: %v\n", err)
		return 1
	}
	defer func() { _ = migrator.Close() }()

	_, _ = fmt.Fprintf(out, "cfctl migrate %s on %s (CF_ENV=%s from %s, DATABASE_MIGRATE_URL from %s, env file %s)\n",
		command, target, cmp.Or(cfEnv, "(unset)"), cfSource, urlSource, displayPath(root, path))
	river := &lazyRiver{dsn: dsn}
	defer river.Close()
	if err := runMigrate(ctx, migrator, river.get, command, to, out); err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl migrate: %s failed: %v\n", command, err)
		return 1
	}
	return 0
}

// lazyRiver opens the River migrator on first use, so `version` and `redo` never connect for it.
type lazyRiver struct {
	dsn string
	m   *jobs.Migrator
}

func (l *lazyRiver) get(ctx context.Context) (*jobs.Migrator, error) {
	if l.m != nil {
		return l.m, nil
	}
	m, err := jobs.NewMigrator(ctx, l.dsn)
	if err != nil {
		return nil, err
	}
	l.m = m
	return m, nil
}

func (l *lazyRiver) Close() {
	if l.m != nil {
		l.m.Close()
	}
}

// riverName shows a River migration next to the app ones: "river/001_main_..." sorts after "0000N_...".
func riverName(version int, name string) string {
	return fmt.Sprintf("river/%03d_%s", version, strings.ReplaceAll(name, " ", "_"))
}

func printRiver(out io.Writer, rs []jobs.Result) {
	for _, r := range rs {
		_, _ = fmt.Fprintf(out, "  %-4s %s (%s)\n", r.Direction, riverName(r.Version, r.Name), r.Duration.Round(1e6))
	}
}

func printResults(out io.Writer, res []store.Result) {
	for _, r := range res {
		_, _ = fmt.Fprintf(out, "  %-4s %s (%s)\n", r.Direction, r.Name, r.Duration.Round(1e6))
	}
}

func runMigrate(ctx context.Context, m *store.Migrator, riverOf func(context.Context) (*jobs.Migrator, error), command string, to *int64, out io.Writer) error {
	var (
		res []store.Result
		err error
	)
	switch command {
	case "up":
		if to != nil {
			res, err = m.UpTo(ctx, *to)
			printResults(out, res)
			if err == nil && len(res) == 0 {
				_, _ = fmt.Fprintln(out, "  nothing to do")
			}
			_, _ = fmt.Fprintln(out, "  WARNING: River's tables were NOT migrated: --to stops the app migrations only. The api cannot create workspaces, and its /readyz fails, until you run `cfctl migrate up` without --to")
			return err
		}
		res, err = m.Up(ctx)
		printResults(out, res)
		if err != nil {
			return err
		}
		rm, err := riverOf(ctx)
		if err != nil {
			return err
		}
		rres, err := rm.Up(ctx)
		printRiver(out, rres)
		if err == nil && len(res) == 0 && len(rres) == 0 {
			_, _ = fmt.Fprintln(out, "  nothing to do")
		}
		return err
	case "down":
		// The app migrations go first. River's tables are dropped only once they have reached 0, so a goose down that
		// fails never leaves the queue gone while the app schema (and its api) remains. If River's own down fails after
		// that, the next `down` finds nothing applied and finishes the job.
		reachesZero := to != nil && *to == 0
		if to == nil {
			ss, err := m.Status(ctx)
			if err != nil {
				return err
			}
			applied := 0
			for _, s := range ss {
				if s.Applied {
					applied++
				}
			}
			reachesZero = applied <= 1
		}
		if to != nil {
			res, err = m.DownTo(ctx, *to)
		} else {
			res, err = m.Down(ctx)
		}
		printResults(out, res)
		if err != nil {
			return err
		}
		var rres []jobs.Result
		if reachesZero {
			rm, err := riverOf(ctx)
			if err != nil {
				return err
			}
			if rres, err = rm.Down(ctx); err != nil {
				printRiver(out, rres)
				return err
			}
			printRiver(out, rres)
		}
		if len(res) == 0 && len(rres) == 0 {
			_, _ = fmt.Fprintln(out, "  nothing to do")
		}
		return nil
	case "redo":
		res, err = m.Redo(ctx)
	case "version":
		v, err := m.Version(ctx)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(out, v)
		return nil
	case "status":
		ss, err := m.Status(ctx)
		if err != nil {
			return err
		}
		for _, s := range ss {
			state := "pending"
			if s.Applied {
				state = "applied " + s.AppliedAt.UTC().Format("2006-01-02 15:04:05Z")
			}
			_, _ = fmt.Fprintf(out, "  %-40s %s\n", s.Name, state)
		}
		rm, err := riverOf(ctx)
		if err != nil {
			return err
		}
		rs, err := rm.Status(ctx)
		if err != nil {
			return err
		}
		for _, s := range rs {
			state := "pending"
			if s.Applied {
				state = "applied"
			}
			_, _ = fmt.Fprintf(out, "  %-40s %s\n", riverName(s.Version, s.Name), state)
		}
		return nil
	}
	// Report what ran even when a later migration failed.
	printResults(out, res)
	if err == nil && len(res) == 0 {
		_, _ = fmt.Fprintln(out, "  nothing to do")
	}
	return err
}

// describeTarget names the database as host:port/name, without the user or the password. dsn must already have
// passed config.ValidatePostgresURL.
func describeTarget(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return "(unparsable)"
	}
	return u.Host + "/" + strings.TrimPrefix(u.Path, "/")
}
