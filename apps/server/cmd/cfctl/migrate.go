package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/nilpntr/content-fabrique/internal/doctor"
	"github.com/nilpntr/content-fabrique/internal/envfile"
	"github.com/nilpntr/content-fabrique/internal/store"
)

const migrateUsage = `Usage:
  cfctl migrate [--env-file PATH] <command> [--to VERSION]

Applies the SQL migrations embedded in the binary (internal/store/migrations) to the database named by
DATABASE_MIGRATE_URL: a direct connection as the role that owns the schema. Nothing else reads that variable.
Each migration grants the app role cf_app its rights, so the app's own login role never needs DDL.

Commands:
  up        apply every pending migration (--to VERSION stops after that version)
  down      roll back the latest migration (--to VERSION rolls back everything above it; --to 0 rolls back all)
  redo      roll back the latest migration and apply it again
  status    list every migration and whether it is applied
  version   print the latest applied version (0 when none)

  --env-file  the env file; default .env at the repository root, and a relative name is taken from there too
              (task migrate runs in apps/server). A non-empty value in the file wins; the process
              environment fills a name the file lacks or leaves empty (the same rule as cfctl doctor).
              Staging: --env-file .env.staging
  --to        a target version, for up and down

Never prints the URL or the password.
`

func cmdMigrate(args []string, stdout, stderr io.Writer, lookup lookupEnv) int {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stderr, migrateUsage) }
	envFileArg := flags.String("env-file", "", "env file")
	toArg := flags.String("to", "", "target version")

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
	target, err := describeTarget(dsn)
	if err != nil {
		_, _ = fmt.Fprintln(errOut, "cfctl migrate: DATABASE_MIGRATE_URL: must be a postgres:// or postgresql:// URL")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	migrator, err := store.NewMigrator(dsn)
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl migrate: %v\n", err)
		return 1
	}
	defer func() { _ = migrator.Close() }()

	_, _ = fmt.Fprintf(out, "cfctl migrate %s on %s\n", command, target)
	if err := runMigrate(ctx, migrator, command, to, out); err != nil {
		_, _ = fmt.Fprintf(errOut, "cfctl migrate: %s failed: %v\n", command, err)
		return 1
	}
	return 0
}

func runMigrate(ctx context.Context, m *store.Migrator, command string, to *int64, out io.Writer) error {
	var (
		res []store.Result
		err error
	)
	switch command {
	case "up":
		if to != nil {
			res, err = m.UpTo(ctx, *to)
		} else {
			res, err = m.Up(ctx)
		}
	case "down":
		if to != nil {
			res, err = m.DownTo(ctx, *to)
		} else {
			res, err = m.Down(ctx)
		}
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
		return nil
	}
	// Report what ran even when a later migration failed.
	for _, r := range res {
		_, _ = fmt.Fprintf(out, "  %-4s %s (%s)\n", r.Direction, r.Name, r.Duration.Round(1e6))
	}
	if err == nil && len(res) == 0 {
		_, _ = fmt.Fprintln(out, "  nothing to do")
	}
	return err
}

// describeTarget names the database as host:port/name, without the user or the password.
func describeTarget(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return "", errors.New("not a postgres URL")
	}
	return u.Host + "/" + strings.TrimPrefix(u.Path, "/"), nil
}
