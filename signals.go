package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Collections: the messages of a poll by what they are to you.
const (
	colMentions = "mentions"
	colDMs      = "dms"
	colThreads  = "threads"
	colOncall   = "oncall"
	colIgnored  = "ignored"
)

type collection struct{ ID, Title, Description string }

var collections = []collection{
	{colMentions, "Mentions", "messages that mention you"},
	{colDMs, "DMs", "direct messages to you"},
	{colThreads, "Threads", "replies in threads you watch"},
	{colOncall, "On-call", "activity in on-call channels (while on call) and channels you watch"},
	{colIgnored, "Ignored", "what an ignored thread, channel or user said"},
}

func collectionByID(id string) (collection, bool) {
	for _, c := range collections {
		if c.ID == id {
			return c, true
		}
	}
	return collection{}, false
}

// Collections are the classified messages by collection.
type Collections map[string][]Message

// collect classifies a poll's messages; messages that are nothing to you
// are in no collection.
func collect(p Poll, r Rules, workspace string) Collections {
	c := Collections{}
	for _, m := range p.Messages {
		m = classify(m, r)
		if m.Permalink == "" {
			m.Permalink = buildPermalink(workspace, m)
		}
		switch {
		case m.Kind == "":
			continue
		case m.Ignored:
			c[colIgnored] = append(c[colIgnored], m)
		case m.Kind == KindMention:
			c[colMentions] = append(c[colMentions], m)
		case m.Kind == KindDM:
			c[colDMs] = append(c[colDMs], m)
		case m.Kind == KindThread:
			c[colThreads] = append(c[colThreads], m)
		default:
			c[colOncall] = append(c[colOncall], m)
		}
	}
	return c
}

func (c Collections) find(key string) (Message, bool) {
	for _, col := range collections {
		for _, m := range c[col.ID] {
			if m.Key == key {
				return m, true
			}
		}
	}
	return Message{}, false
}

// Signals: what needs you since the last o-slack signals run, pushed into
// the mtm inbox one action item each: a new mention, DM, reply in a thread
// you watch, on-call channel message (while on call) or watched channel
// message. The first run records what is there as the baseline and sends
// nothing, so connecting o-slack does not flood the inbox. Ignored
// messages and your own send nothing but are recorded, so unignoring a
// thread replays nothing. A signal that fails to send is kept (pending)
// and sent first next run.

// Signal is one inbox item.
type Signal struct {
	Kind    string `json:"kind"`
	Key     string `json:"key"`
	URL     string `json:"url,omitempty"`
	Title   string `json:"title"`
	Why     string `json:"why"`
	Text    string `json:"text,omitempty"`
	Channel string `json:"channel,omitempty"`
	TS      string `json:"ts,omitempty"`
}

// signalOf is a classified message as a signal.
func signalOf(m Message) Signal {
	title := map[string]string{KindMention: "Mention", KindDM: "DM", KindThread: "Thread reply", KindOncall: "On-call", KindChannel: "Channel"}[m.Kind]
	if m.Kind == KindDM {
		title += " from " + m.Who()
	} else {
		title += ": " + m.Who() + " in " + m.Where()
	}
	return Signal{Kind: m.Kind, Key: m.Key, URL: m.Permalink, Title: title, Why: m.Reason, Text: clip(m.Text, 500), Channel: m.Channel, TS: m.TS}
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// seenFor is how long a seen key is kept: longer than any poll reads back.
const seenFor = 14 * 24 * time.Hour

// diffSignals compares a poll's classified messages with the last run
// (nil for none) and answers the signals (pending ones first) and the
// snapshot to keep when every one is delivered.
func diffSignals(prev *Snapshot, p Poll, r Rules, workspace string, now time.Time) ([]Signal, Snapshot) {
	next := Snapshot{At: p.At, Seen: map[string]string{}}
	if len(p.Errors) > 0 {
		// A read failed: the next run reads again from where this one began.
		next.At = p.Since
		if prev != nil && !prev.At.IsZero() && prev.At.Before(next.At) {
			next.At = prev.At
		}
	}
	var out []Signal
	sent := map[string]bool{}
	if prev != nil {
		for k, ts := range prev.Seen {
			if t, ok := tsTime(ts); !ok || now.Sub(t) < seenFor {
				next.Seen[k] = ts
			}
		}
		for _, s := range prev.Pending {
			if !sent[s.Key] {
				sent[s.Key] = true
				out = append(out, s)
			}
		}
	}
	// Backlog: a message older than the last run (less the overlap) is no
	// news, such as the earlier replies of a thread watched since.
	var backlog time.Time
	if prev != nil && !prev.At.IsZero() {
		backlog = prev.At.Add(-overlap)
	}
	for _, m := range p.Messages {
		_, was := next.Seen[m.Key]
		next.Seen[m.Key] = m.TS
		if prev == nil || was || sent[m.Key] {
			continue
		}
		if t, ok := tsTime(m.TS); ok && t.Before(backlog) {
			continue
		}
		c := classify(m, r)
		if c.Kind == "" || c.Ignored {
			continue
		}
		if c.Permalink == "" {
			c.Permalink = buildPermalink(workspace, c)
		}
		sent[c.Key] = true
		out = append(out, signalOf(c))
	}
	return out, next
}

// Inbox puts one signal in the mtm inbox.
type Inbox func(ctx context.Context, s Signal) error

// inboxCommand is the one place the inbox command is spelled (lane L1's
// contract): mtm action-item add --source slack --key KEY [--url URL]
// --title TITLE --why WHY [--text TEXT]. The bin is $O_SLACK_MTM (default
// mtm); $O_SLACK_MTM_ARGS adds words before the verb.
func inboxCommand(getenv func(string) string, s Signal) []string {
	bin := getenv("O_SLACK_MTM")
	if bin == "" {
		bin = "mtm"
	}
	argv := append([]string{bin}, strings.Fields(getenv("O_SLACK_MTM_ARGS"))...)
	argv = append(argv, "action-item", "add", "--source", "slack", "--key", s.Key)
	if s.URL != "" {
		argv = append(argv, "--url", s.URL)
	}
	argv = append(argv, "--title", s.Title, "--why", s.Why)
	if s.Text != "" {
		argv = append(argv, "--text", s.Text)
	}
	return argv
}

const inboxTimeout = 20 * time.Second

// mtmInbox runs inboxCommand.
func mtmInbox(ctx context.Context, s Signal) error {
	argv := inboxCommand(os.Getenv, s)
	ctx, cancel := context.WithTimeout(ctx, inboxTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		head := strings.Join(argv[:min(3, len(argv))], " ")
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%s: %w: %s", head, err, msg)
		}
		return fmt.Errorf("%s: %w", head, err)
	}
	return nil
}

// SignalRun is what one signals run did.
type SignalRun struct {
	DryRun  bool       `json:"dry_run"`
	First   bool       `json:"first"` // no earlier run: the baseline was recorded, nothing sent
	Server  string     `json:"server"`
	Signals []Signal   `json:"signals"`
	Sent    int        `json:"sent"`
	Failed  []string   `json:"failed,omitempty"`
	Command [][]string `json:"commands,omitempty"`
	Seen    int        `json:"seen"`
	Errors  []string   `json:"errors,omitempty"`
	Notes   []string   `json:"notes,omitempty"`
	Polled  time.Time  `json:"polled"`
	Since   time.Time  `json:"since"`
}

// overlap is how far before the last run each run reads again.
const overlap = 10 * time.Minute

// runSignals polls from the last run, diffs, and (unless dry) pushes each
// signal and keeps the snapshot of what was delivered.
func (t *Tool) runSignals(ctx context.Context, cfg Config, dry bool) (SignalRun, error) {
	var prev *Snapshot
	var snap Snapshot
	if ok, err := t.store.read(signalsFile, &snap); err != nil {
		return SignalRun{}, err
	} else if ok {
		prev = &snap
	}
	var since time.Time
	if prev != nil && !prev.At.IsZero() {
		since = prev.At.Add(-overlap)
		if lb := t.now().Add(-cfg.Lookback); since.Before(lb) && cfg.Lookback > 0 {
			since = lb
		}
	}
	p, st, err := t.current(ctx, cfg, true, since)
	if err != nil {
		return SignalRun{}, err
	}
	sigs, next := diffSignals(prev, p, cfg.rules(st, t.now()), cfg.Workspace, t.now())
	run := SignalRun{DryRun: dry, First: prev == nil, Server: p.Server, Signals: sigs, Seen: len(p.Messages), Errors: p.Errors, Notes: p.Notes, Polled: p.At, Since: p.Since}
	if run.Signals == nil {
		run.Signals = []Signal{}
	}
	if dry {
		for _, s := range sigs {
			run.Command = append(run.Command, inboxCommand(os.Getenv, s))
		}
		return run, nil
	}
	var errs []error
	for _, s := range sigs {
		if err := t.inbox(ctx, s); err != nil {
			run.Failed = append(run.Failed, s.Key+": "+err.Error())
			errs = append(errs, err)
			next.Pending = append(next.Pending, s)
			continue
		}
		run.Sent++
	}
	if err := t.store.locked(func() error { return t.store.write(signalsFile, next) }); err != nil {
		return run, err
	}
	if len(errs) > 0 {
		return run, fmt.Errorf("%d of %d signals not sent (kept for the next run): %w", len(errs), len(sigs), errors.Join(errs...))
	}
	return run, nil
}
