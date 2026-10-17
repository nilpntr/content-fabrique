// Command cfctl is the Content Fabrique admin CLI. Subcommands are added by later tasks.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/nilpntr/content-fabrique/internal/version"
)

const usage = `cfctl is the Content Fabrique admin CLI.

Usage:
  cfctl <command>

Commands:
  doctor     check the environment's variables and services (cfctl doctor --help)
  migrate    apply the database migrations as the schema-owning role (cfctl migrate help)
  plan       grant, revoke or show the plan of a person's workspace, until billing exists (cfctl plan help)
  render     render a timeline spec with ffmpeg, for local manual runs (cfctl render --help)
  media      probe a file's metadata, benchmark the sandbox providers (cfctl media help)
  env        init: create .env and generate secrets. lint: check variable reads against .env.example
  version    print the build version
  help       show this help
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one cfctl invocation and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "-version", "--version":
		_, _ = fmt.Fprintf(stdout, "cfctl %s\n", version.Get())
		return 0
	case "doctor":
		return cmdDoctor(args[1:], stdout, stderr, os.LookupEnv)
	case "env":
		return cmdEnv(args[1:], stdout, stderr)
	case "migrate":
		return cmdMigrate(args[1:], stdout, stderr, os.LookupEnv)
	case "plan":
		return cmdPlan(args[1:], stdout, stderr, os.LookupEnv)
	case "render":
		return cmdRender(args[1:], stdout, stderr)
	case "media":
		return cmdMedia(args[1:], stdout, stderr, defaultMediaDeps())
	case "help", "-h", "-help", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

// signalContext is the context every long-running subcommand uses: it ends on Ctrl-C or SIGTERM, so work in flight (a
// database call, a sandbox job) is cancelled cleanly. Call the returned function when done.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
