package main

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	r := Rules{Who: Who{Me: "miso", MeID: "U0ME"}, WatchDMs: true, OncallChans: []string{"#ops-alerts"}, OncallTag: "@oncall",
		Watched: []string{"C0RAND"}, Threads: map[string]bool{"C0ENG/1.000100": true}, IgnThreads: map[string]bool{}}
	msg := func(ch, name, ts, thread, user, text string) Message {
		m := Message{Channel: ch, ChannelName: name, TS: ts, ThreadTS: thread, User: user, Text: text}
		m.DM = isDMChannel(ch, name)
		m.Key = messageKey(ch, ts, "", text)
		return m
	}
	cases := []struct {
		name   string
		m      Message
		active bool
		kind   string
		reason string
	}{
		{"mention by id", msg("C0ENG", "eng", "2.0", "", "U0ANN", "<@U0ME> look"), false, KindMention, "mentions you in #eng"},
		{"mention by handle", msg("C0ENG", "eng", "2.0", "", "U0ANN", "hey @Miso"), false, KindMention, "mentions you in #eng"},
		{"own message", msg("C0ENG", "eng", "2.0", "", "U0ME", "<@U0ME> note"), false, "", ""},
		{"dm", msg("D0BOB", "", "2.0", "", "U0BOB", "lunch?"), false, KindDM, "direct message from U0BOB"},
		{"watched thread reply", msg("C0ENG", "eng", "2.0", "1.000100", "U0BOB", "on it"), false, KindThread, "reply in a thread you watch in #eng"},
		{"unwatched thread reply", msg("C0ENG", "eng", "2.0", "1.5", "U0BOB", "on it"), false, "", ""},
		{"oncall channel, off call", msg("C0OPS", "ops-alerts", "2.0", "", "U0BOT", "deploy failed"), false, "", ""},
		{"oncall channel, on call", msg("C0OPS", "ops-alerts", "2.0", "", "U0BOT", "deploy failed"), true, KindOncall, "message in on-call channel #ops-alerts"},
		{"oncall tag", msg("C0ENG", "eng", "2.0", "", "U0BOB", "@oncall kettle"), true, KindOncall, "mentions @oncall"},
		{"watched channel", msg("C0RAND", "random", "2.0", "", "U0ANN", "cake"), false, KindChannel, "message in watched channel #random"},
	}
	for _, c := range cases {
		rr := r
		rr.OncallActive = c.active
		got := classify(c.m, rr)
		if got.Kind != c.kind || got.Reason != c.reason {
			t.Errorf("%s: %q %q", c.name, got.Kind, got.Reason)
		}
	}
	// Via: the mentions search matched though the text spells you otherwise.
	m := msg("C0ENG", "eng", "2.0", "", "U0ANN", "@Miso Shiru look")
	m.Via = []string{"mentions"}
	m.Text = "Miso Shiru look"
	if got := classify(m, r); got.Kind != KindMention || got.Reason != "matched your mentions search in #eng" {
		t.Errorf("via %+v", got)
	}
	// Ignoring: a thread, a channel (not for DMs or on-call), a user.
	r.IgnThreads["C0ENG/1.000100"] = true
	if got := classify(msg("C0ENG", "eng", "2.0", "1.000100", "U0BOB", "on it"), r); !got.Ignored || got.IgnoredBy != "thread" {
		t.Errorf("ignored thread %+v", got)
	}
	r.Ignored = []string{"eng"}
	if got := classify(msg("C0ENG", "eng", "3.0", "", "U0ANN", "<@U0ME>"), r); !got.Ignored || got.IgnoredBy != "channel" {
		t.Errorf("ignored channel %+v", got)
	}
	r.IgnoreUsers = []string{"U0BOB"}
	if got := classify(msg("D0BOB", "", "2.0", "", "U0BOB", "x"), r); !got.Ignored || got.IgnoredBy != "user U0BOB" {
		t.Errorf("ignored user %+v", got)
	}
	// Without watch-dms a DM is nothing.
	r.WatchDMs, r.IgnoreUsers = false, nil
	if got := classify(msg("D0ANN", "", "2.0", "", "U0ANN", "x"), r); got.Kind != "" {
		t.Errorf("dm without watch-dms %+v", got)
	}
}

func TestOncallWindow(t *testing.T) {
	loc := time.Local
	o := Oncall{Mode: OncallAuto, Start: "2026-10-26", Weeks: 1} // a Monday
	in := time.Date(2026, 10, 28, 12, 0, 0, 0, loc)
	if !o.Active(in) || o.Active(time.Date(2026, 11, 2, 0, 0, 1, 0, loc)) || o.Active(time.Date(2026, 10, 25, 23, 0, 0, 0, loc)) {
		t.Fatal("auto window")
	}
	o.Weeks = 2
	if !o.Active(time.Date(2026, 11, 3, 0, 0, 0, 0, loc)) {
		t.Fatal("two weeks")
	}
	if (Oncall{Mode: OncallOn}).Active(in) != true || (Oncall{Mode: OncallOff, Start: "2026-10-26"}).Active(in) || (Oncall{}).Active(in) {
		t.Fatal("modes")
	}
	if p := (Oncall{Mode: "maybe", Start: "2026-10-27", Weeks: 60}).Problems(); len(p) != 3 {
		t.Fatalf("problems %q", p)
	}
}

func TestMessageRefsAndPermalinks(t *testing.T) {
	for _, ref := range []string{"slack-message/C0ENG/1793160000.000100", "C0ENG/1793160000.000100", "https://teashop.slack.com/archives/C0ENG/p1793160000000100"} {
		ch, ts, _, err := parseMessageRef(ref)
		if err != nil || ch != "C0ENG" || ts != "1793160000.000100" {
			t.Errorf("%s: %s %s %v", ref, ch, ts, err)
		}
	}
	_, _, th, _ := parseMessageRef("https://teashop.slack.com/archives/C0ENG/p1793163600000200?thread_ts=1793160000.000100&cid=C0ENG")
	if th != "1793160000.000100" {
		t.Errorf("thread_ts %q", th)
	}
	for _, bad := range []string{"", "C0ENG", "C0ENG/notats", "a/b/c"} {
		if _, _, _, err := parseMessageRef(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	m := Message{Channel: "C0ENG", TS: "1793163600.000200", ThreadTS: "1793160000.000100"}
	if got := buildPermalink(workspace+"/", m); got != workspace+"/archives/C0ENG/p1793163600000200?thread_ts=1793160000.000100&cid=C0ENG" {
		t.Errorf("permalink %s", got)
	}
	if tt, ok := tsTime("1793167200.000000"); !ok || !tt.Equal(fixtureNow) || tsOf(fixtureNow) != "1793167200.000000" {
		t.Errorf("ts %v", tt)
	}
}
