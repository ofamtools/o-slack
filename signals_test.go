package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func sigKinds(sigs []Signal) []string {
	out := []string{}
	for _, s := range sigs {
		out = append(out, s.Kind+" "+s.Key)
	}
	sort.Strings(out)
	return out
}

func testRules() Rules {
	return Rules{Who: Who{Me: "miso", MeID: "U0ME"}, WatchDMs: true, Threads: map[string]bool{}, IgnThreads: map[string]bool{}}
}

func pollOf(at time.Time, ms ...Message) Poll {
	return Poll{At: at, Since: at.Add(-time.Hour), Messages: ms}
}

func newMsg(ch, ts, thread, user, text string) Message {
	m := Message{Channel: ch, TS: ts, ThreadTS: thread, User: user, Text: text, DM: isDMChannel(ch, "")}
	m.Key = messageKey(ch, ts, "", text)
	return m
}

func TestDiffSignals(t *testing.T) {
	r := testRules()
	mention := newMsg("C0ENG", "1793160000.000100", "", "U0ANN", "<@U0ME> look")
	p := pollOf(fixtureNow, mention)

	// First run: the baseline, nothing sent, everything seen.
	sigs, snap := diffSignals(nil, p, r, workspace, fixtureNow)
	if len(sigs) != 0 || snap.Seen[mention.Key] != mention.TS || !snap.At.Equal(fixtureNow) {
		t.Fatalf("first run %v %+v", sigKinds(sigs), snap)
	}
	// Nothing new: nothing sent.
	if again, _ := diffSignals(&snap, p, r, workspace, fixtureNow); len(again) != 0 {
		t.Fatalf("repeat %v", sigKinds(again))
	}
	// New: a DM, your own message (nothing), an ignored thread's mention
	// (nothing, but seen) and an old reply read for the first time (backlog).
	later := fixtureNow.Add(5 * time.Minute)
	dm := newMsg("D0BOB", tsOf(later.Add(-time.Minute)), "", "U0BOB", "lunch?")
	mine := newMsg("C0ENG", tsOf(later.Add(-50*time.Second)), "", "U0ME", "<@U0ME> self")
	ign := newMsg("C0ENG", tsOf(later.Add(-40*time.Second)), "1793150000.000100", "U0ANN", "<@U0ME> in there")
	old := newMsg("C0ENG", "1793163600.000200", "1793160000.000100", "U0BOB", "on it")
	r.IgnThreads["C0ENG/1793150000.000100"] = true
	r.Threads[mention.Key] = true
	sigs, next := diffSignals(&snap, pollOf(later, mention, dm, mine, ign, old), r, workspace, later)
	if got := sigKinds(sigs); !reflect.DeepEqual(got, []string{"dm " + dm.Key}) {
		t.Fatalf("second run %v", got)
	}
	s := sigs[0]
	if s.Title != "DM from U0BOB" || s.Why != "direct message from U0BOB" || s.URL != buildPermalink(workspace, dm) {
		t.Fatalf("dm signal %+v", s)
	}
	for _, m := range []Message{mine, ign, old} {
		if _, ok := next.Seen[m.Key]; !ok {
			t.Errorf("%s not recorded", m.Key)
		}
	}
	// Unignoring the thread replays nothing: its message was seen.
	delete(r.IgnThreads, "C0ENG/1793150000.000100")
	if again, _ := diffSignals(&next, pollOf(later, ign), r, workspace, later); len(again) != 0 {
		t.Fatalf("unignore replayed %v", sigKinds(again))
	}
	// Pending signals go first and once, even if the poll sees them again.
	next.Pending = []Signal{s}
	again, _ := diffSignals(&next, pollOf(later, dm), r, workspace, later)
	if len(again) != 1 || again[0].Key != dm.Key {
		t.Fatalf("pending %v", sigKinds(again))
	}
	// A failed read: the next run reads again from where this one began.
	failed := pollOf(later, dm)
	failed.Errors = []string{"mentions: boom"}
	if _, n := diffSignals(&next, failed, r, workspace, later); !n.At.Equal(failed.Since) {
		t.Fatalf("after a failed read the next run reads from %v, want %v", n.At, failed.Since)
	}
	// Seen keys older than seenFor are pruned.
	next.Seen["C0OLD/1700000000.000100"] = "1700000000.000100"
	if _, n := diffSignals(&next, pollOf(later), r, workspace, later); n.Seen["C0OLD/1700000000.000100"] != "" {
		t.Fatal("an old key was kept")
	}
}

func TestInboxCommand(t *testing.T) {
	s := Signal{Key: keyMention, URL: workspace + "/archives/C0ENG/p1793160000000100", Title: "Mention: ann in #eng", Why: "mentions you in #eng", Text: "look"}
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	want := []string{"mtm", "action-item", "add", "--source", "slack", "--key", keyMention, "--url", s.URL,
		"--title", "Mention: ann in #eng", "--why", "mentions you in #eng", "--text", "look"}
	if got := inboxCommand(get, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("command %q", got)
	}
	env["O_SLACK_MTM"], env["O_SLACK_MTM_ARGS"] = "/opt/mtm", "--data /tmp/x.db"
	if got := inboxCommand(get, s); got[0] != "/opt/mtm" || got[1] != "--data" || got[2] != "/tmp/x.db" || got[3] != "action-item" {
		t.Fatalf("command %q", got)
	}
	// No permalink: no --url.
	s.URL, s.Text = "", ""
	if got := strings.Join(inboxCommand(func(string) string { return "" }, s), " "); strings.Contains(got, "--url") || strings.Contains(got, "--text") {
		t.Fatalf("command %s", got)
	}
}

// TestMtmInboxRunsTheCommand runs the real seam against a fake mtm script.
func TestMtmInboxRunsTheCommand(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "mtm")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + log + "\n[ \"$FAIL\" = 1 ] && { echo 'no such verb' >&2; exit 3; }\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("O_SLACK_MTM", bin)
	t.Setenv("O_SLACK_MTM_ARGS", "")
	t.Setenv("FAIL", "")
	s := Signal{Key: keyDM, URL: workspace + "/archives/D0BOB/p1793164000000300", Title: "DM from bob", Why: "direct message from bob"}
	if err := mtmInbox(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	want := "action-item\nadd\n--source\nslack\n--key\n" + keyDM + "\n--url\n" + s.URL + "\n--title\nDM from bob\n--why\ndirect message from bob\n"
	if string(b) != want {
		t.Fatalf("argv:\n%s", b)
	}
	t.Setenv("FAIL", "1")
	if err := mtmInbox(context.Background(), s); err == nil || !strings.Contains(err.Error(), "no such verb") {
		t.Fatalf("failure: %v", err)
	}
}

func TestRunSignals(t *testing.T) {
	tool, fs, inbox := testTool(t, fakeOptions{})
	ctx := context.Background()
	set(t, tool, setOncallMode, OncallOn)
	set(t, tool, setOncallChannels, []string{"#ops-alerts"})

	// A dry run before any run: the baseline is not recorded, nothing sent.
	run, err := tool.runSignals(ctx, tool.config(), true)
	if err != nil || !run.DryRun || !run.First || len(run.Signals) != 0 || len(inbox.got) != 0 {
		t.Fatalf("dry run %+v %v", run, err)
	}
	if _, err := os.Stat(filepath.Join(tool.store.Dir, signalsFile)); !os.IsNotExist(err) {
		t.Fatal("a dry run wrote the snapshot")
	}
	// The first run: the baseline; the mention's thread is auto-watched.
	run, err = tool.runSignals(ctx, tool.config(), false)
	if err != nil || !run.First || run.Sent != 0 || run.Seen == 0 {
		t.Fatalf("first run %+v %v", run, err)
	}
	// The next run reads the watched thread; its old reply is backlog.
	tool.now = func() time.Time { return fixtureNow.Add(time.Minute) }
	if run, err = tool.runSignals(ctx, tool.config(), false); err != nil || len(run.Signals) != 0 {
		t.Fatalf("backlog run %v %v", sigKinds(run.Signals), err)
	}

	// New: a mention, a reply in the watched thread, a DM, an on-call page,
	// and a mention in a thread you ignored.
	later := fixtureNow.Add(5 * time.Minute)
	tool.now = func() time.Time { return later }
	ts := func(d time.Duration) string { return tsOf(fixtureNow.Add(d)) }
	fs.addMessage(map[string]any{"channel": "C0ENG", "ts": ts(60 * time.Second), "user": "U0ANN", "text": "<@U0ME> the tea is ready"})
	fs.addMessage(map[string]any{"channel": "C0ENG", "ts": ts(70 * time.Second), "thread_ts": "1793160000.000100", "user": "U0BOB", "text": "kettle fixed"})
	fs.addMessage(map[string]any{"channel": "D0BOB", "ts": ts(80 * time.Second), "user": "U0BOB", "text": "ping"})
	fs.addMessage(map[string]any{"channel": "C0OPS", "ts": ts(90 * time.Second), "user": "U0BOT", "text": "pager: kettle overheated"})
	fs.addMessage(map[string]any{"channel": "C0ENG", "ts": ts(100 * time.Second), "thread_ts": "1793166500.000600", "user": "U0ANN", "text": "<@U0ME> re your note"})
	tool.store.Change(func(st *State) error {
		st.IgnoredThreads["C0ENG/1793166500.000600"] = Mark{Ref: "C0ENG/1793166500.000600"}
		return nil
	})
	keyNew := func(d time.Duration, ch string) string { return ch + "/" + ts(d) }

	// The dry run shows the four and the commands; it changes nothing.
	out, err := sh(t, tool, "signals", "--dry-run")
	if err != nil || json.Unmarshal([]byte(out), &run) != nil || len(run.Signals) != 4 || len(run.Command) != 4 || len(inbox.got) != 0 {
		t.Fatalf("dry run %v %+v", err, run)
	}
	if c := run.Command[0]; c[0] != "mtm" || c[1] != "action-item" || c[4] != "slack" || c[5] != "--key" {
		t.Fatalf("dry-run command %q", c)
	}

	// The DM fails to send: kept for the next run.
	inbox.fail[keyNew(80*time.Second, "D0BOB")] = true
	run, err = tool.runSignals(ctx, tool.config(), false)
	want := []string{
		"dm " + keyNew(80*time.Second, "D0BOB"), "mention " + keyNew(60*time.Second, "C0ENG"),
		"oncall " + keyNew(90*time.Second, "C0OPS"), "thread " + keyNew(70*time.Second, "C0ENG"),
	}
	if err == nil || run.Sent != 3 || len(run.Failed) != 1 || !reflect.DeepEqual(sigKinds(run.Signals), want) {
		t.Fatalf("run %v %+v %v", sigKinds(run.Signals), run, err)
	}
	for _, s := range inbox.got {
		if s.Kind == KindOncall && (s.Why != "message in on-call channel #ops-alerts" || s.URL != workspace+"/archives/C0OPS/p"+strings.Replace(ts(90*time.Second), ".", "", 1)) {
			t.Fatalf("oncall signal %+v", s)
		}
		if s.Kind == KindThread && (!strings.Contains(s.URL, "thread_ts=1793160000.000100") || s.Title != "Thread reply: U0BOB in #eng") {
			t.Fatalf("thread signal %+v", s)
		}
	}
	delete(inbox.fail, keyNew(80*time.Second, "D0BOB"))
	run, err = tool.runSignals(ctx, tool.config(), false)
	if err != nil || run.Sent != 1 || run.Signals[0].Kind != KindDM {
		t.Fatalf("retry %+v %v", run, err)
	}
	dials := fs.dials
	if run, _ = tool.runSignals(ctx, tool.config(), false); len(run.Signals) != 0 {
		t.Fatalf("nothing new: %v", sigKinds(run.Signals))
	}
	// Every signals run polls: no cache.
	if fs.dials == dials {
		t.Fatal("signals used the cache")
	}
	if len(inbox.got) != 4 {
		t.Fatalf("inbox got %d", len(inbox.got))
	}
}

func TestPollCache(t *testing.T) {
	tool, fs, _ := testTool(t, fakeOptions{})
	ctx := context.Background()
	cfg := tool.config()
	cfg.AutoWatch = false
	if _, err := tool.collections(ctx, cfg, false); err != nil {
		t.Fatal(err)
	}
	tool.collections(ctx, cfg, false)
	if fs.dials != 1 {
		t.Fatalf("a fresh cache polled again: %d", fs.dials)
	}
	// A new watch the cached poll did not read: poll.
	tool.store.Change(func(st *State) error { st.Watched["C0RAND"] = Mark{Ref: "C0RAND"}; return nil })
	cs, _ := tool.collections(ctx, cfg, false)
	if fs.dials != 2 || len(cs[colOncall]) != 2 {
		t.Fatalf("watch did not poll: %d %+v", fs.dials, cs[colOncall])
	}
	// Older than poll.max-age: poll.
	tool.now = func() time.Time { return fixtureNow.Add(2 * time.Minute) }
	tool.collections(ctx, cfg, false)
	if fs.dials != 3 {
		t.Fatalf("a stale cache answered: %d", fs.dials)
	}
}

func TestProseSearch(t *testing.T) {
	// A gateway that answers the search in prose with permalinks.
	tool, _, _ := testTool(t, fakeOptions{prose: true})
	out, err := sh(t, tool, "message", "mentions")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids(rowsOf(t, messageNoun, out)), " "); got != messageNoun+"/"+keyMention {
		t.Fatalf("prose mentions %v", got)
	}
}
