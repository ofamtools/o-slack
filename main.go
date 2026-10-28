// Command o-slack is the family's Slack tool: the Slack page (Mentions,
// DMs, Threads, On-call, Channels) and the signals it sends into the mtm
// inbox. It reaches Slack only through an MCP server (o-mcp's client and
// the server's own configured credentials), maps its operations to the
// server's tools through settings, and never writes to Slack.
//
//	o-slack [view] [--smoke]          the Slack page
//	o-slack message list|mentions|dms|threads|oncall|ignored|show|open|... [--target MSG]
//	o-slack channel list|show|open|history|watch|ignore|oncall|... [--target CHANNEL]
//	o-slack map check [--offline] [--schemas FILE]   the mapping against the server's tools
//	o-slack signals [--dry-run]       push new mentions, DMs, replies and on-call messages to the inbox
//	o-slack catalog                   the catalog a host (mtm) mounts
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/unixsuperhero/ochazuke"
	ocli "github.com/unixsuperhero/ochazuke/cli"
	"github.com/unixsuperhero/oden"
)

func main() {
	os.Exit(oden.Report(os.Stderr, progName(), Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)))
}

func progName() string {
	if parent := os.Getenv("ODEN_PARENT"); parent != "" {
		return parent + " slack"
	}
	return "o-slack"
}

// Main runs one invocation.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return run(newTool(stdin, stdout, stderr), args)
}

// run runs one invocation on t (tests pass a Tool with fakes).
func run(t *Tool, args []string) error {
	if err := t.newRuntime(true); err != nil {
		return err
	}
	app := oden.New("o-slack", args, oden.WithBuiltins(), oden.WithStdio(t.Stdin, t.Stdout, t.Stderr),
		oden.WithDefault(func(_ context.Context, args []string) error {
			if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
				return oden.ErrNotFound
			}
			return runTUI(t, args)
		}))
	ocli.Mount(app, t.Shell())
	app.Add(viewCommand(t), signalsCommand(t))
	app.HelpText += `
Page:
  o-slack (or o-slack view) opens the Slack page; --smoke renders one frame.
Mapping:
  o-slack map check [--offline] [--schemas FILE] [--format table] compares the tool mapping
  (slack.tools.OP, slack.args.OP, slack.results.OP) with the server's tools and prints the fixes.
Signals:
  o-slack signals [--dry-run] sends new mentions, DMs, thread replies and on-call messages to the
  mtm inbox (mtm action-item add --source slack --key KEY --url URL ...; $O_SLACK_MTM names another mtm).`
	return app.Run(context.Background())
}

// Shell is the NOUN VERB command line over the runtime.
func (t *Tool) Shell() *ocli.Shell {
	sh := ocli.New(t.r, "o-slack", t.Stdin, t.Stdout, t.Stderr)
	sh.Usage = ocli.DefaultUsage("o-slack")
	sh.Version = Version
	sh.Reveal = t.reveal
	return sh
}

// reveal makes a --target row exist: a channel (slack-channel/REF, #name,
// an ID starting C or G), a user (slack-user/ID, an ID starting U or W),
// else a message.
func (t *Tool) reveal(target string) error {
	item := func(noun string) error {
		return t.targets.SetItems([]ochazuke.ListItem{{ID: target, Label: target, Noun: noun, Data: target}})
	}
	switch {
	case strings.HasPrefix(target, channelNoun+"/"), strings.HasPrefix(target, "#"),
		looksLikeID(target) && target[0] != 'D':
		return item(channelNoun)
	case strings.HasPrefix(target, userNoun+"/"), len(target) > 2 && (target[0] == 'U' || target[0] == 'W') && strings.ToUpper(target) == target && !strings.Contains(target, "/"):
		return item(userNoun)
	}
	if _, _, _, err := parseMessageRef(target); err != nil {
		return err
	}
	return item(messageNoun)
}

// signalsCommand is o-slack signals.
func signalsCommand(t *Tool) oden.Command {
	return oden.Command{
		Name: "signals", Summary: "send new mentions, DMs, thread replies and on-call messages since the last run to the mtm inbox",
		Usage: "[--dry-run]", Source: "o-slack",
		Details: "Polls Slack once through the MCP server, compares with the last run (signals.json in the data directory) and runs " +
			"mtm action-item add --source slack --key KEY --url PERMALINK --title TITLE --why WHY for each signal. The first run records " +
			"the baseline and sends nothing. --dry-run prints the signals and the commands, sends nothing and keeps the last run as it was. " +
			"A signal that fails to send is kept and sent first next run. Ignored threads, channels and users send nothing.",
		Run: func(ctx context.Context, args []string) error {
			flags := flag.NewFlagSet("o-slack signals", flag.ContinueOnError)
			flags.SetOutput(t.Stderr)
			dry := flags.Bool("dry-run", false, "print what would be sent; send nothing, keep the last run")
			if err := flags.Parse(args); err != nil {
				if errors.Is(err, flag.ErrHelp) {
					return nil
				}
				return &oden.ExitError{Code: 2}
			}
			res, err := t.runSignals(ctx, t.config(), *dry)
			if res.Polled.IsZero() && err != nil {
				return err
			}
			enc := json.NewEncoder(t.Stdout)
			enc.SetIndent("", "  ")
			if eerr := enc.Encode(res); eerr != nil && err == nil {
				err = eerr
			}
			if err == nil && len(res.Errors) > 0 {
				fmt.Fprintf(t.Stderr, "o-slack signals: %d reads failed (the next run reads them again): %s\n", len(res.Errors), strings.Join(res.Errors, "; "))
			}
			return err
		},
	}
}
