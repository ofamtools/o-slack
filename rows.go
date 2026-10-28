package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	ocli "github.com/unixsuperhero/ochazuke/cli"
)

// A message row: id slack-message/CHANNEL/TS (what --target takes), label
// "WHO: text", the message as its record, one cell per messageColumns
// column. A channel row: id slack-channel/REF.

func messageRow(m Message, now time.Time) ocli.Row {
	return ocli.Row{Noun: messageNoun, ID: messageNoun + "/" + m.Key, Label: m.Who() + ": " + clip(oneLine(m.Text), 60), Data: m, Cells: messageCells(m, now)}
}

func messageCells(m Message, now time.Time) []string {
	kind := m.Kind
	if kind == "" {
		kind = "-"
	}
	text := oneLine(m.Text)
	switch {
	case m.Ignored:
		text = "[ignored] " + text
	case m.Watching && m.Kind != KindThread:
		text = "[watching] " + text
	}
	if m.Reply() {
		text = "↳ " + text
	}
	return []string{kind, m.Where(), m.Who(), text, age(now, m.TS)}
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func channelRow(c Channel) ocli.Row {
	name := c.Name
	if name == "" {
		name = c.ID
	}
	oncall := "-"
	switch {
	case c.OnCall && c.Active:
		oncall = "● now"
	case c.OnCall:
		oncall = "○ off"
	}
	mark := "-"
	switch {
	case c.Watched:
		mark = "watched"
	case c.Ignored:
		mark = "ignored"
	}
	topic := c.Topic
	if !c.Read {
		topic = "(not read yet) " + topic
	}
	return ocli.Row{Noun: channelNoun, ID: channelNoun + "/" + c.Ref(), Label: "#" + name, Data: c, Cells: []string{"#" + name, oncall, mark, oneLine(topic)}}
}

// checkRows are map check's answer: one row per operation, its problems
// and the settings lines that fix them, then a summary row.
func checkRows(checks []Check, problems []string, source string, tools int) []ocli.Row {
	out := []ocli.Row{}
	bad := 0
	for _, c := range checks {
		if c.Status != "ok" && c.Status != "optional" {
			bad++
		}
		var cell []string
		cell = append(cell, c.Problems...)
		for _, f := range c.Fix {
			cell = append(cell, "fix: "+f)
		}
		if len(cell) == 0 {
			cell = []string{strings.Join(c.Args, " ")}
		}
		out = append(out, ocli.Row{Noun: mapNoun, ID: mapNoun + "/" + c.Op, Label: c.Op + " " + c.Status, Data: c,
			Cells: []string{c.Op, c.Tool, c.Status, strings.Join(cell, "; ")}})
	}
	summary := fmt.Sprintf("%d of %d operations need a fix; %d tools from %s", bad, len(checks), tools, source)
	if bad == 0 {
		summary = fmt.Sprintf("every operation maps; %d tools from %s", tools, source)
	}
	if len(problems) > 0 {
		summary += "; settings: " + strings.Join(problems, "; ")
	}
	out = append(out, ocli.Row{Noun: mapNoun, ID: mapNoun + "/summary", Label: "summary",
		Data:  map[string]any{"needs_fix": bad, "operations": len(checks), "tools": tools, "source": source, "setting_problems": problems},
		Cells: []string{"summary", "", map[bool]string{true: "ok", false: "fix"}[bad == 0 && len(problems) == 0], summary}})
	return out
}

// age is how long ago a Slack ts was: now, 5m, 3h, 2d, 6w.
func age(now time.Time, ts string) string {
	t, ok := tsTime(ts)
	if !ok {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return fmt.Sprintf("%dw", int(d.Hours()/24/7))
}

// openURL opens an http(s) URL with $O_SLACK_OPEN, else open or xdg-open
// (o-gh's openURL).
func openURL(ctx context.Context, url string) error {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return fmt.Errorf("refusing to open a non-http URL: %q", url)
	}
	var argv []string
	switch {
	case os.Getenv("O_SLACK_OPEN") != "":
		argv = strings.Fields(os.Getenv("O_SLACK_OPEN"))
	case runtime.GOOS == "darwin":
		argv = []string{"open"}
	case runtime.GOOS == "linux":
		argv = []string{"xdg-open"}
	default:
		return fmt.Errorf("opening URLs is unsupported on %s", runtime.GOOS)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, argv[0], append(argv[1:], url)...).CombinedOutput(); err != nil {
		return fmt.Errorf("open %s: %w: %s", url, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// copyToClipboard writes text to the clipboard ($O_SLACK_CLIPBOARD, else
// pbcopy, wl-copy, xclip or xsel).
func copyToClipboard(text string) error {
	var argv []string
	if cmd := os.Getenv("O_SLACK_CLIPBOARD"); cmd != "" {
		argv = strings.Fields(cmd)
	} else {
		cands := [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
		if runtime.GOOS == "darwin" {
			cands = append([][]string{{"pbcopy"}}, cands...)
		}
		for _, c := range cands {
			if _, err := exec.LookPath(c[0]); err == nil {
				argv = c
				break
			}
		}
	}
	if len(argv) == 0 {
		return errors.New("no clipboard command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
