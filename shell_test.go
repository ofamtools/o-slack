package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ofam.dev/o-mcp/client"
	ocli "ofam.dev/ochazuke/cli"
)

// sh runs one o-slack command line on a fresh Tool sharing tool's fakes and
// data directory, and answers its stdout.
func sh(t *testing.T, tool *Tool, args ...string) (string, error) {
	t.Helper()
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	err := run(tool.sub(strings.NewReader(""), out, errb), args)
	if err != nil {
		t.Logf("%v: %v\n%s", args, err, errb)
	}
	return out.String(), err
}

func rowsOf(t *testing.T, noun, out string) []ocli.Row {
	t.Helper()
	rows, err := ocli.DecodeRows(noun, []byte(out))
	if err != nil {
		t.Fatalf("rows: %v\n%s", err, out)
	}
	return rows
}

func ids(rows []ocli.Row) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func TestCatalog(t *testing.T) {
	tool, fs, _ := testTool(t, fakeOptions{})
	out, err := sh(t, tool, "catalog")
	if err != nil {
		t.Fatal(err)
	}
	var doc ocli.CatalogDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Name != "o-slack" || len(doc.Pages) != 1 || doc.Pages[0].ID != pageID || len(doc.Pages[0].Lists) != 5 ||
		doc.Pages[0].Lists[0].Source != "slack-message.mentions" || doc.Pages[0].Lists[4].Source != "slack-channel.list" {
		t.Fatalf("pages %+v", doc.Pages)
	}
	verbs := map[string]bool{}
	for _, n := range doc.AppNouns() {
		for _, v := range n.Verbs {
			verbs[n.Name+"."+v.Name] = true
		}
	}
	for _, want := range []string{
		"slack-message.list", "slack-message.mentions", "slack-message.dms", "slack-message.threads", "slack-message.oncall", "slack-message.ignored",
		"slack-message.show", "slack-message.open", "slack-message.copy-link", "slack-message.read-thread", "slack-message.watch-thread",
		"slack-message.unwatch-thread", "slack-message.ignore", "slack-message.unignore", "slack-message.author", "slack-message.channel",
		"slack-message.send-to-inbox", "slack-message.refresh",
		"slack-channel.list", "slack-channel.show", "slack-channel.open", "slack-channel.copy-link", "slack-channel.history",
		"slack-channel.watch", "slack-channel.unwatch", "slack-channel.ignore", "slack-channel.unignore", "slack-channel.oncall",
		"slack-channel.not-oncall", "slack-channel.send-to-inbox", "slack-channel.refresh",
		"slack-user.show", "slack-user.open", "slack-user.copy-id",
		"slack-signal.preview", "slack-signal.push", "slack-map.check", "slack-map.show", "slack-map.candidates",
	} {
		if !verbs[want] {
			t.Errorf("catalog lacks %s", want)
		}
	}
	// Each noun's row source is its list (ochazuke v0.31.0 prefers the
	// noun's own rows), though dms and history declare message rows too.
	for noun, rows := range map[string]string{messageNoun: messageNoun, channelNoun: channelNoun} {
		if verb, got := doc.RowSource(noun); verb != "list" || got != rows {
			t.Errorf("%s row source %q %q, want list %q", noun, verb, got, rows)
		}
	}
	if !verbRows(doc, channelNoun, "history", messageNoun) {
		t.Errorf("%s.history does not declare %s rows", channelNoun, messageNoun)
	}
	// No verb writes to Slack, and the catalog needs no server.
	for v := range verbs {
		if writeTool(strings.SplitN(v, ".", 2)[1]) && !strings.HasSuffix(v, "send-to-inbox") {
			t.Errorf("a verb looks like a write: %s", v)
		}
	}
	if fs.dials != 0 {
		t.Fatalf("catalog dialled the server %d times", fs.dials)
	}
}

func TestShellCollectionsAndMarks(t *testing.T) {
	tool, fs, inbox := testTool(t, fakeOptions{})
	out, err := sh(t, tool, "message", "mentions")
	if err != nil {
		t.Fatal(err)
	}
	// Your own <@U0ME> note is no mention.
	if got := strings.Join(ids(rowsOf(t, messageNoun, out)), " "); got != messageNoun+"/"+keyMention {
		t.Fatalf("mentions %v", got)
	}
	out, _ = sh(t, tool, "message", "dms")
	rows := rowsOf(t, messageNoun, out)
	if strings.Join(ids(rows), " ") != messageNoun+"/"+keyDM {
		t.Fatalf("dms %v", ids(rows))
	}
	if m := rows[0].Data.(map[string]any); m["reason"] != "direct message from bob" || m["permalink"] != workspace+"/archives/D0BOB/p1793164000000300" {
		t.Fatalf("dm row %+v", m)
	}
	// The mention auto-watched its thread: the next poll reads it and the
	// reply is a Threads row.
	st, _ := tool.store.State()
	if mk, ok := st.Threads[keyMention]; !ok || !mk.Auto {
		t.Fatalf("auto watch %+v", st.Threads)
	}
	out, _ = sh(t, tool, "message", "threads")
	if got := strings.Join(ids(rowsOf(t, messageNoun, out)), " "); got != messageNoun+"/"+keyReply {
		t.Fatalf("threads %v", got)
	}
	// Ignore the thread by its permalink: the reply moves to Ignored.
	if _, err := sh(t, tool, "message", "ignore", "--target", workspace+"/archives/C0ENG/p1793160000000100"); err != nil {
		t.Fatal(err)
	}
	out, _ = sh(t, tool, "message", "threads")
	if n := len(rowsOf(t, messageNoun, out)); n != 0 {
		t.Fatalf("threads after ignore %d", n)
	}
	// The mention is in the ignored thread too, and the thread is no longer read.
	out, _ = sh(t, tool, "message", "list", "--collection", "ignored", "--format", "table")
	if !strings.Contains(out, "[ignored]") || !strings.Contains(out, "look at the deploy") || strings.Contains(out, "timer is stuck") {
		t.Fatalf("ignored:\n%s", out)
	}
	out, _ = sh(t, tool, "message", "mentions")
	if n := len(rowsOf(t, messageNoun, out)); n != 0 {
		t.Fatalf("mentions after ignore %d", n)
	}
	sh(t, tool, "message", "unignore", "--target", keyMention)
	sh(t, tool, "message", "watch-thread", "--target", keyMention)
	out, _ = sh(t, tool, "message", "threads")
	if n := len(rowsOf(t, messageNoun, out)); n != 1 {
		t.Fatalf("threads after unignore %d", n)
	}
	// show, copy-link, open, read-thread, author.
	out, err = sh(t, tool, "message", "show", "--target", messageNoun+"/"+keyDM)
	var m Message
	if err != nil || json.Unmarshal([]byte(out), &m) != nil || m.Text != "lunch at noon?" || m.Kind != KindDM {
		t.Fatalf("show %v %+v", err, m)
	}
	out, _ = sh(t, tool, "message", "copy-link", "--target", keyReply)
	if !strings.Contains(out, "p1793163600000200?thread_ts=1793160000.000100") {
		t.Fatalf("copy-link %s", out)
	}
	out, _ = sh(t, tool, "message", "read-thread", "--target", keyReply)
	if got := ids(rowsOf(t, messageNoun, out)); len(got) != 2 {
		t.Fatalf("read-thread %v", got)
	}
	out, err = sh(t, tool, "message", "author", "--target", keyDM)
	if err != nil || !strings.Contains(out, "Bob Sencha") {
		t.Fatalf("author %v %s", err, out)
	}
	out, err = sh(t, tool, "user", "show", "--target", "U0ANN")
	if err != nil || !strings.Contains(out, "Grader") {
		t.Fatalf("user show %v %s", err, out)
	}
	// Send to inbox: the seam gets the message, its permalink and a reason.
	if _, err := sh(t, tool, "message", "send-to-inbox", "--target", keyMention, "check the timer"); err != nil {
		t.Fatal(err)
	}
	if len(inbox.got) != 1 || inbox.got[0].Key != keyMention || inbox.got[0].Why != "check the timer" ||
		inbox.got[0].URL != workspace+"/archives/C0ENG/p1793160000000100" {
		t.Fatalf("inbox %+v", inbox.got)
	}
	// A bad target is refused; nothing ever called the write tool.
	if _, err := sh(t, tool, "message", "watch-thread", "--target", "not-a-message"); err == nil {
		t.Fatal("a bad target was watched")
	}
	for _, c := range fs.called() {
		if c == "slack_send_message" {
			t.Fatal("o-slack called a write tool")
		}
	}
}

func TestShellChannelsAndOncall(t *testing.T) {
	tool, fs, _ := testTool(t, fakeOptions{})
	out, err := sh(t, tool, "channel", "list")
	if err != nil {
		t.Fatal(err)
	}
	// The DM conversation is no channel row.
	if got := strings.Join(ids(rowsOf(t, channelNoun, out)), " "); got != "slack-channel/C0ENG slack-channel/C0OPS slack-channel/C0RAND" {
		t.Fatalf("channels %v", got)
	}
	// Flag #ops-alerts on-call by name; off call it is listed but quiet.
	if _, err := sh(t, tool, "channel", "oncall", "--target", "#ops-alerts"); err != nil {
		t.Fatal(err)
	}
	st, _ := tool.store.State()
	if _, ok := st.OnCall["C0OPS"]; !ok {
		t.Fatalf("on-call mark %+v", st.OnCall)
	}
	out, _ = sh(t, tool, "channel", "list", "--format", "table")
	if !strings.Contains(out, "○ off") {
		t.Fatalf("off-call table:\n%s", out)
	}
	out, _ = sh(t, tool, "message", "oncall")
	if n := len(rowsOf(t, messageNoun, out)); n != 0 {
		t.Fatalf("oncall off call %d", n)
	}
	// On call: the channel's message and the @oncall tag elsewhere are rows.
	set(t, tool, setOncallMode, OncallOn)
	set(t, tool, setOncallTag, "@oncall")
	sh(t, tool, "channel", "watch", "--target", "C0RAND")
	out, _ = sh(t, tool, "message", "oncall")
	got := strings.Join(ids(rowsOf(t, messageNoun, out)), " ")
	for _, want := range []string{keyDeploy, keyCake, keyKettle} {
		if !strings.Contains(got, want) {
			t.Errorf("oncall lacks %s: %s", want, got)
		}
	}
	out, _ = sh(t, tool, "channel", "list", "--format", "table")
	if !strings.Contains(out, "● now") || !strings.Contains(out, "watched") {
		t.Fatalf("on-call table:\n%s", out)
	}
	// A channel only named in a setting is a row (record first).
	set(t, tool, setOncallChannels, []string{"#pager"})
	out, _ = sh(t, tool, "channel", "list")
	if !strings.Contains(strings.Join(ids(rowsOf(t, channelNoun, out)), " "), "slack-channel/pager") {
		t.Fatalf("named channel missing:\n%s", out)
	}
	out, _ = sh(t, tool, "channel", "not-oncall", "--target", "#pager")
	if !strings.Contains(out, "still in oncall.channels") {
		t.Fatalf("not-oncall %s", out)
	}
	// Ignore a channel; open it; history reads it.
	sh(t, tool, "channel", "ignore", "--target", "C0RAND")
	st, _ = tool.store.State()
	if _, w := st.Watched["C0RAND"]; w || len(st.Ignored) != 1 {
		t.Fatalf("ignore did not unwatch %+v", st)
	}
	out, _ = sh(t, tool, "channel", "open", "--target", "C0ENG")
	if !strings.Contains(out, workspace+"/archives/C0ENG") {
		t.Fatalf("open %s", out)
	}
	out, _ = sh(t, tool, "channel", "history", "--target", "#eng")
	if n := len(rowsOf(t, messageNoun, out)); n != 2 {
		t.Fatalf("history rows %d\n%s", n, out)
	}
	if fs.dials == 0 {
		t.Fatal("no dial")
	}
}

func TestNeedsSignInAndUnreachableSayWhatToDo(t *testing.T) {
	tool, _, _ := testTool(t, fakeOptions{})
	// An OAuth-only gateway entry: the server answers 401 to a request
	// without a credential. o-slack goes through o-mcp's real dialer.
	oauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://runlayer.example/.well-known/oauth-protected-resource"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer oauth.Close()
	tool.resolve = func(string, []string) (client.Server, error) {
		return client.NewServer("runlayer", map[string]any{"url": oauth.URL}), nil
	}
	// o-mcp's real dialer with a test home and keyring; a browser fails
	// the test (a poll never opens one).
	tool.dial = dialWith(client.Options{Home: t.TempDir(), Project: t.TempDir(), Getenv: func(string) string { return "" },
		Keyring: client.NewMemoryKeyring(), OpenURL: func(u string) error { t.Errorf("a browser was opened for %s", u); return nil }})
	_, err := sh(t, tool, "message", "mentions")
	if err == nil || !client.IsNeedsLogin(err) || !strings.Contains(err.Error(), "o-mcp login runlayer") || !strings.Contains(err.Error(), "never opens a browser") || strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("needs sign-in: %v", err)
	}
	// A configured credential the gateway rejects is unreachable.
	tool.resolve = func(string, []string) (client.Server, error) {
		return client.NewServer("runlayer", map[string]any{"url": oauth.URL, "headers": map[string]any{"Authorization": "Bearer wrong"}}), nil
	}
	_, err = sh(t, tool, "message", "mentions")
	if err == nil || !client.IsUnreachable(err) || !strings.Contains(err.Error(), "o-mcp test runlayer") || strings.Contains(err.Error(), "o-mcp login") {
		t.Fatalf("unreachable: %v", err)
	}
	// No server named and none of the candidates configured.
	tool.resolve = func(ref string, fb []string) (client.Server, error) { return findServer(nil, ref, fb) }
	if _, err := sh(t, tool, "message", "mentions"); err == nil || !strings.Contains(err.Error(), "runlayer, runlayer-plugin, slack") {
		t.Fatalf("no server: %v", err)
	}
	set(t, tool, setServer, "work-slack")
	if _, err := sh(t, tool, "message", "mentions"); err == nil || !strings.Contains(err.Error(), `slack.server "work-slack"`) {
		t.Fatalf("named server: %v", err)
	}
	// The fallbacks pick the first configured name.
	s, err := findServer([]client.Server{client.NewServer("slack", nil), client.NewServer("runlayer-plugin", nil)}, "", defaultFallbacks)
	if err != nil || s.Name != "runlayer-plugin" {
		t.Fatalf("fallback %v %v", s.Name, err)
	}
}

// verbRows reports whether noun.verb declares rows of the rows noun in doc.
func verbRows(doc ocli.CatalogDoc, noun, verb, rows string) bool {
	for _, n := range doc.AppNouns() {
		for _, v := range n.Verbs {
			if n.Name == noun && v.Name == verb {
				return v.Rows == rows
			}
		}
	}
	return false
}
