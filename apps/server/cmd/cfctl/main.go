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
  credits    show a workspace's credits, grant a top-up, adjust the balance (cfctl credits help)
  assets     import local media files into a workspace's library, development only (cfctl assets help)
  project    create a project for the editor, show a project's versions, development only (cfctl project help)
  render     render a timeline spec with ffmpeg, for local manual runs (cfctl render --help)
  audio      listen to a rendered video: loudness, silence, sync, density, defects, with no model, development only (cfctl audio help)
  music      analyse a song (tempo, beats, drop), extract the song of a video, a synthetic test track, development only (cfctl music help)
  sfx        gallery: render every sound effect to a web page to listen to, development only (cfctl sfx help)
  transcribe transcribe a 16 kHz WAV file with whisper.cpp, fetch the models (cfctl transcribe help)
  media      probe a file's metadata, benchmark the sandbox providers (cfctl media help)
  style      the creator style profile (P6-05): analyse reference videos, show the profile, development only (cfctl style help)
  eval       print the check_edit metrics of a stored editor timeline version, no model calls (cfctl eval help)
  agent      run an agent in this process and show a run's events and costs, development only (cfctl agent help)
  llm        check model access for a routing stage: count tokens, or send one tiny message (cfctl llm help)
  trace      replay a stored agent run as spans into Phoenix (or any OTLP endpoint), no model calls, development only (cfctl trace help)
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
	case "credits":
		return cmdCredits(args[1:], stdout, stderr, os.LookupEnv, defaultAgentDeps())
	case "assets":
		return cmdAssets(args[1:], stdout, stderr, os.LookupEnv)
	case "project":
		return cmdProject(args[1:], stdout, stderr, os.LookupEnv, defaultAgentDeps())
	case "render":
		return cmdRender(args[1:], stdout, stderr)
	case "audio":
		return cmdAudio(args[1:], stdout, stderr, os.LookupEnv, defaultAgentDeps())
	case "music":
		return cmdMusic(args[1:], stdout, stderr)
	case "sfx":
		return cmdSFX(args[1:], stdout, stderr)
	case "media":
		return cmdMedia(args[1:], stdout, stderr, defaultMediaDeps())
	case "transcribe":
		return cmdTranscribe(args[1:], stdout, stderr, os.LookupEnv)
	case "agent":
		return cmdAgent(args[1:], stdout, stderr, os.LookupEnv, defaultAgentDeps())
	case "style":
		return cmdStyle(args[1:], stdout, stderr, os.LookupEnv, defaultAgentDeps())
	case "eval":
		return cmdEval(args[1:], stdout, stderr, os.LookupEnv, defaultAgentDeps())
	case "llm":
		return cmdLLM(args[1:], stdout, stderr, os.LookupEnv, defaultLLMDeps())
	case "trace":
		return cmdTrace(args[1:], stdout, stderr, os.LookupEnv, defaultAgentDeps())
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
