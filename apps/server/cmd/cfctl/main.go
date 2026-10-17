// Command cfctl is the Content Fabrique admin CLI. Subcommands are added by later tasks.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/nilpntr/content-fabrique/internal/version"
)

const usage = `cfctl is the Content Fabrique admin CLI.

Usage:
  cfctl <command>

Commands:
  doctor     check the environment's variables and services (cfctl doctor --help)
  migrate    apply the database migrations as the schema-owning role (cfctl migrate help)
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
	case "help", "-h", "-help", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "cfctl: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
