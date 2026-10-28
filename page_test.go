package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	ocli "github.com/unixsuperhero/ochazuke/cli"
)

// send delivers msg to the page and, as Bubble Tea would, every message
// the commands it returns produce (a command that does not answer within
// a second, such as a tick, is dropped). o-gh's page test.
func send(p *page, msg tea.Msg) {
	queue := []tea.Msg{msg}
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		if batch, ok := m.(tea.BatchMsg); ok {
			for _, c := range batch {
				queue = append(queue, run1(c)...)
			}
			continue
		}
		_, cmd := p.Update(m)
		queue = append(queue, run1(cmd)...)
	}
}

func run1(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case m := <-done:
		if m == nil {
			return nil
		}
		return []tea.Msg{m}
	case <-time.After(time.Second):
		return nil
	}
}

// press sends printable keys to the page and runs what they start.
func press(t *testing.T, p *page, keys ...rune) {
	t.Helper()
	// The keys go in first, their commands after: a chord's timeout must
	// not land between its keys.
	var cmds []tea.Cmd
	for _, k := range keys {
		_, cmd := p.Update(tea.KeyPressMsg{Code: k, Text: string(k)})
		cmds = append(cmds, cmd)
	}
	send(p, tea.BatchMsg(cmds))
	// The verb's Then marked the page dirty; the next message reloads it.
	send(p, tea.WindowSizeMsg{Width: 140, Height: 34})
}

func TestPageSmoke(t *testing.T) {
	tool, _, _ := testTool(t, fakeOptions{})
	tool.store.Change(func(st *State) error { st.OnCall["C0OPS"] = Mark{Ref: "C0OPS"}; return nil })
	out, err := sh(t, tool, "view", "--smoke", "--width", "140", "--height", "34")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Mentions (1)", "DMs (1)", "Threads (", "On-call (0)", "Channels (3)", "can you look at the deploy", "#eng", "○ off"} {
		if !strings.Contains(out, want) {
			t.Errorf("frame lacks %q:\n%s", want, out)
		}
	}
}

func TestPageKeys(t *testing.T) {
	tool, _, inbox := testTool(t, fakeOptions{})
	var copied []string
	tool.clip = func(s string) error { copied = append(copied, s); return nil }
	p, err := mountPage(tool)
	if err != nil {
		t.Fatal(err)
	}
	if err := ocli.Settle(p.t.r, nil); err != nil {
		t.Fatal(err)
	}
	send(p, tea.WindowSizeMsg{Width: 140, Height: 34})
	if n := p.v.List(listID(colMentions)).Len(); n != 1 {
		t.Fatalf("mentions rows %d", n)
	}
	// y y on the mention copies its permalink.
	press(t, p, 'y', 'y')
	if len(copied) != 1 || copied[0] != workspace+"/archives/C0ENG/p1793160000000100" {
		t.Fatalf("y y copied %v", copied)
	}
	// W stops the automatic watch of its thread; w watches it again (by hand).
	press(t, p, 'W')
	// The reload polled again; the thread stays unwatched.
	st, _ := tool.store.State()
	if _, ok := st.Threads[keyMention]; ok || st.Unwatched[keyMention].Ref == "" {
		t.Fatalf("W did not unwatch: %+v", st.Threads)
	}
	press(t, p, 'w')
	st, _ = tool.store.State()
	if mk, ok := st.Threads[keyMention]; !ok || mk.Auto {
		t.Fatalf("w did not watch: %+v", st.Threads)
	}
	// d d ignores it: it leaves Mentions.
	press(t, p, 'd', 'd')
	st, _ = tool.store.State()
	if _, ok := st.IgnoredThreads[keyMention]; !ok {
		t.Fatalf("d d did not ignore: %+v", st)
	}
	if n := p.v.List(listID(colMentions)).Len(); n != 0 {
		t.Fatalf("mentions after d d: %d", n)
	}
	// g i on the DM sends it to the inbox.
	if err := p.v.List(listID(colDMs)).Focus(); err != nil {
		t.Fatal(err)
	}
	press(t, p, 'g', 'i')
	if len(inbox.got) != 1 || inbox.got[0].Key != keyDM || inbox.got[0].Kind != KindDM {
		t.Fatalf("g i: %+v", inbox.got)
	}
	// o on a channel flags it on-call.
	if err := p.v.List(listID("channels")).Focus(); err != nil {
		t.Fatal(err)
	}
	press(t, p, 'o')
	st, _ = tool.store.State()
	if _, ok := st.OnCall["C0ENG"]; !ok {
		t.Fatalf("o did not flag on-call: %+v", st.OnCall)
	}
	if strings.Contains(p.m.View().Content, "panic") {
		t.Fatal("the page panicked")
	}
}
