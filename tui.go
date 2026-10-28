package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/unixsuperhero/ochazuke"
	ocli "github.com/unixsuperhero/ochazuke/cli"
	"github.com/unixsuperhero/oden"
)

// The Slack page on its own: o-slack mounts its declared data page over
// itself with ocli.MountPage, exactly as a host does, so the page o-slack
// shows is the page mtm shows. Each list loads from its source verb
// (slack-message.mentions, ...) run in this process; the message lists
// share one poll (the lock and the cache). A verb that changes the lists
// (watch, ignore, ...) reloads them, and the page reloads itself every
// poll.max-age.

// viewCommand is o-slack view.
func viewCommand(t *Tool) oden.Command {
	return oden.Command{
		Name: "view", Summary: "open the Slack page: Mentions, DMs, Threads, On-call and Channels beside the selected row",
		Usage: "[--smoke] [--width N] [--height N]", Source: "o-slack",
		Details: "o-slack with no command opens it too. Keys on a message: enter opens its permalink, y y copies it, w watches its thread, " +
			"W unwatches, d d ignores it, g t reads the thread, g i sends it to the inbox. On a channel: enter opens, w watches, d d ignores, " +
			"o flags it on-call, O drops the flag. : runs any verb; ? lists every key; q quits.",
		Run: func(_ context.Context, args []string) error { return runTUI(t, args) },
	}
}

// catalogDoc is t's own catalog, decoded as a host reads it.
func catalogDoc(t *Tool) (ocli.CatalogDoc, error) {
	b, err := ocli.Catalog(t.r, "o-slack", Version)
	if err != nil {
		return ocli.CatalogDoc{}, err
	}
	var doc ocli.CatalogDoc
	err = json.Unmarshal(b, &doc)
	return doc, err
}

// fetchInProcess runs NOUN VERB on a fresh Tool sharing t's fakes and
// answers its stdout, as a host's Fetch runs the bin.
func (t *Tool) fetchInProcess(ctx context.Context, noun, verb string) ([]byte, error) {
	var out, errb bytes.Buffer
	sub := t.sub(strings.NewReader(""), &out, &errb)
	if err := run(sub, []string{noun, verb}); err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out.Bytes(), nil
}

// page is the mounted Slack page.
type page struct {
	t     *Tool
	m     *ochazuke.Model
	v     *ocli.PageView
	dirty bool
	every time.Duration
}

// mountPage builds the page on a page runtime of its own (t keeps the
// shell's runtime).
func mountPage(shell *Tool) (*page, error) {
	t := shell.sub(shell.Stdin, shell.Stdout, shell.Stderr)
	if err := t.newRuntime(false); err != nil {
		return nil, err
	}
	doc, err := catalogDoc(t)
	if err != nil {
		return nil, err
	}
	m := ochazuke.NewModel(t.r)
	m.Title = "o-slack"
	v, err := ocli.MountPage(m, doc, slackPage(), ocli.PageConfig{Fetch: t.fetchInProcess})
	if err != nil {
		return nil, err
	}
	quit := ochazuke.Do("app.quit")
	quit.Instance, quit.Mode, quit.Keys = pageID, "normal", "q"
	if err := t.r.Bind(quit); err != nil {
		return nil, err
	}
	if err := t.r.OpenPage(pageID); err != nil {
		return nil, err
	}
	// The page opens on Mentions (a declared page's own focus stays the page).
	if err := v.List(listID(colMentions)).Focus(); err != nil {
		return nil, err
	}
	p := &page{t: t, m: m, v: v, every: t.config().MaxAge}
	if p.every < 10*time.Second {
		p.every = 10 * time.Second
	}
	t.changed = func() { p.dirty = true }
	return p, v.Reload()
}

// runTUI parses the page's flags and runs it (or renders one frame).
func runTUI(shell *Tool, args []string) error {
	flags := flag.NewFlagSet("o-slack view", flag.ContinueOnError)
	flags.SetOutput(shell.Stderr)
	smoke := flags.Bool("smoke", false, "load the lists, render one frame to stdout and exit")
	width := flags.Int("width", 120, "--smoke frame width")
	height := flags.Int("height", 32, "--smoke frame height")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return &oden.ExitError{Code: 2}
	}
	if flags.NArg() > 0 {
		return &oden.ExitError{Code: 2}
	}
	p, err := mountPage(shell)
	if err != nil {
		return err
	}
	if *smoke {
		if err := ocli.Settle(p.t.r, nil); err != nil {
			p.t.r.UI().Status(err.Error())
		}
		p.m.Width, p.m.Height = *width, *height
		_, err := fmt.Fprintln(shell.Stdout, strings.TrimRight(ansi.Strip(p.m.View().Content), "\n"))
		return err
	}
	_, err = tea.NewProgram(p).Run()
	return err
}

// reloadMsg is the page's own reload tick.
type reloadMsg struct{}

func (p *page) tick() tea.Cmd {
	return tea.Tick(p.every, func(time.Time) tea.Msg { return reloadMsg{} })
}

func (p *page) Init() tea.Cmd { return tea.Batch(p.m.Init(), p.t.r.PendingCommands(), p.tick()) }

func (p *page) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(reloadMsg); ok {
		if err := p.v.Reload(); err != nil {
			p.t.r.UI().Status(err.Error())
		}
		return p, tea.Batch(p.t.r.PendingCommands(), p.tick())
	}
	_, cmd := p.m.Update(msg)
	if p.t.r.Quit {
		return p, cmd
	}
	if p.dirty {
		p.dirty = false
		if err := p.v.Reload(); err != nil {
			p.t.r.UI().Status(err.Error())
		}
		cmd = tea.Batch(cmd, p.t.r.PendingCommands())
	}
	return p, cmd
}

func (p *page) View() tea.View { return p.m.View() }
