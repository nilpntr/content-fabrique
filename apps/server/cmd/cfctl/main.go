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
  version    print the build version
  help       show this help
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one cfctl invocation and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "-version", "--version":
		fmt.Fprintf(stdout, "cfctl %s\n", version.Get())
		return 0
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "cfctl: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
