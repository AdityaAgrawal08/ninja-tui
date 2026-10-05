// Command ninja-tui is a terminal front end for the ninja scam email
// classifier. It imports a folder of .eml files, sends each one over SMTP to
// a ninja server the operator runs separately, and displays the results ninja
// POSTs back to this process's webhook listener.
//
// ninja-tui never starts or manages ninja itself.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"ninja-tui/internal/store"
	"ninja-tui/internal/tui"
	"ninja-tui/internal/webhook"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cfg, code := parseFlags(args)
	if code >= 0 {
		return code
	}

	st := store.New()
	model := tui.New(tui.Config{
		Dir:      cfg.dir,
		SMTPAddr: cfg.smtpAddr,
		From:     cfg.from,
		To:       cfg.to,
		Store:    st,
	})

	prog := tea.NewProgram(model)

	// The webhook listener runs for the whole session. Results are forwarded
	// into the Bubble Tea loop with Program.Send; the model is only ever
	// mutated there.
	wh := webhook.New(cfg.webhookAddr)
	wh.SetHandler(func(res webhook.Result) {
		prog.Send(tui.ResultMsg{Result: res})
	})
	if err := wh.Start(); err != nil {
		model.SetWebhook("", err)
	} else {
		model.SetWebhook(wh.Addr(), nil)
	}

	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "ninja-tui:", err)
		model.Stop()
		shutdownWebhook(wh)
		return 1
	}
	model.Stop()
	shutdownWebhook(wh)
	return 0
}

type options struct {
	smtpAddr    string
	webhookAddr string
	from        string
	to          string
	dir         string
}

// parseFlags builds the options from flags, falling back to environment
// variables and finally defaults. When the returned exit code is >= 0 the
// process should exit with it (--help returns 0).
func parseFlags(args []string) (options, int) {
	var cfg options
	fs := flag.NewFlagSet("ninja-tui", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)

	smtpAddr := fs.String("smtp-addr",
		envDefault("127.0.0.1:2525", "NINJA_TUI_SMTP_ADDR", "SMTP_ADDR"),
		"ninja SMTP entrypoint (env SMTP_ADDR)")
	webhookAddr := fs.String("webhook-addr",
		envDefault("127.0.0.1:18080", "NINJA_TUI_WEBHOOK_ADDR", "WEBHOOK_ADDR"),
		"address for this TUI's result webhook listener (env WEBHOOK_ADDR)")
	from := fs.String("from",
		envDefault("tui@localhost", "NINJA_TUI_FROM"),
		"SMTP envelope sender")
	to := fs.String("to",
		envDefault("inbox@mx.local", "NINJA_TUI_TO"),
		"SMTP envelope recipient")

	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprint(out, `ninja-tui — terminal front end for the ninja scam email classifier

Imports a folder of .eml files, sends each one over SMTP to a ninja server
that you run yourself, and displays the analysis results ninja posts back.
ninja-tui never starts or manages ninja.

Usage:
  ninja-tui [flags] [dir]

  dir  folder to scan for .eml files (default ".")

Flags:
`)
		fs.PrintDefaults()
		fmt.Fprint(out, `
Environment:
  SMTP_ADDR, NINJA_TUI_SMTP_ADDR        same as --smtp-addr
  WEBHOOK_ADDR, NINJA_TUI_WEBHOOK_ADDR  same as --webhook-addr

Pairing with ninja (start it yourself, before pressing s):
  # ninja.yaml
  smtp:
    webhook_url: http://127.0.0.1:18080/hook

  # or set NINJA_SMTP_WEBHOOK_URL=http://127.0.0.1:18080/hook
  ninja smtp --config ninja.yaml
`)
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options{}, 0
		}
		return options{}, 2
	}

	cfg = options{
		smtpAddr:    *smtpAddr,
		webhookAddr: *webhookAddr,
		from:        *from,
		to:          *to,
		dir:         ".",
	}
	if fs.NArg() > 0 {
		cfg.dir = fs.Arg(0)
	}
	return cfg, -1
}

func envDefault(def string, names ...string) string {
	for _, n := range names {
		if v, ok := os.LookupEnv(n); ok && v != "" {
			return v
		}
	}
	return def
}

func shutdownWebhook(wh *webhook.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = wh.Shutdown(ctx)
}
