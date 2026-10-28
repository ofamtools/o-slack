package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/unixsuperhero/o-mcp/client"
)

func emptyMap() MapSettings {
	return MapSettings{Tools: map[string]string{}, Args: map[string][]string{}, Results: map[string][]string{}}
}

// fakeTools lists a fake server's tools.
func fakeTools(t *testing.T, opt fakeOptions) []*mcp.Tool {
	t.Helper()
	fs := newFakeSlack(t, opt)
	conn, err := fs.dial(context.Background(), fakeServer)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tools, err := conn.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func TestResolveDefaults(t *testing.T) {
	// No server known: the first candidates, unverified.
	m := resolveAll(emptyMap(), nil)
	if om := m.Ops[opSearch]; om.Tool != "slack_search_public_and_private" || om.From != "default (unverified)" {
		t.Fatalf("search %+v", om)
	}
	if om := m.Ops[opThread]; om.Args[1].String() != "ts=message_ts" {
		t.Fatalf("thread args %v", om.Args)
	}
	// Against a server that lists only korotovsky's names, those are picked.
	tools := []*mcp.Tool{{Name: "conversations_search_messages"}, {Name: "conversations_replies"}}
	m = resolveAll(emptyMap(), tools)
	if om := m.Ops[opSearch]; om.Tool != "conversations_search_messages" || om.From != "candidate" || om.Args[0].Server != "search_query" {
		t.Fatalf("search %+v", om)
	}
	if om := m.Ops[opHistory]; om.Tool != "" || om.From != "none" {
		t.Fatalf("history %+v", om)
	}
	if _, err := m.Ops[opHistory].build(map[string]any{"channel": "C1"}); err == nil || !strings.Contains(err.Error(), "slack.tools.history") {
		t.Fatalf("unmapped build: %v", err)
	}
}

func TestResolveOverrides(t *testing.T) {
	ms := emptyMap()
	ms.Tools[opSearch] = "runlayer_slack_search"
	ms.Args[opSearch] = []string{"query=q", "limit=max", "sort:=\"timestamp\"", "nonsense", "user=u"}
	ms.Results[opSearch] = []string{"items=data.hits", "text=body.plain"}
	m := resolveAll(ms, nil)
	om := m.Ops[opSearch]
	if om.Tool != "runlayer_slack_search" || om.From != "settings" {
		t.Fatalf("tool %+v", om)
	}
	if len(m.Problems) != 2 || !strings.Contains(m.Problems[0], "nonsense") || !strings.Contains(m.Problems[1], `"user" is not a parameter`) {
		t.Fatalf("problems %q", m.Problems)
	}
	args, err := om.build(map[string]any{"query": "deploy", "limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"q": "deploy", "max": 20, "sort": "timestamp"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args %v", args)
	}
	// The result paths: overrides over the defaults.
	if !reflect.DeepEqual(om.Results["items"], []string{"data.hits"}) || !reflect.DeepEqual(om.Results["text"], []string{"body.plain"}) || len(om.Results["ts"]) < 2 {
		t.Fatalf("results %v", om.Results)
	}
	res := &mcp.CallToolResult{StructuredContent: map[string]any{"data": map[string]any{"hits": []any{
		map[string]any{"ts": "1.000100", "channel": map[string]any{"id": "C1", "name": "eng"}, "body": map[string]any{"plain": "hello"}, "permalink": "https://x.slack.com/archives/C1/p1000100"},
	}}}}
	recs, _, err := om.records(res)
	if err != nil || len(recs) != 1 || recs[0]["text"] != "hello" || recs[0]["channel"] != "C1" || recs[0]["channel_name"] != "eng" {
		t.Fatalf("records %v %v", recs, err)
	}
	// A required parameter no argument carries.
	ms.Args[opSearch] = []string{"limit=max"}
	if _, err := resolveAll(ms, nil).Ops[opSearch].build(map[string]any{"query": "x"}); err == nil || !strings.Contains(err.Error(), "carries query") {
		t.Fatalf("required: %v", err)
	}
}

func TestRecordsShapes(t *testing.T) {
	m := resolveAll(emptyMap(), nil)
	// One JSON text block, the list found depth first.
	res := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"ok":true,"messages":[{"ts":"2.5","user":"U1","text":"hi","thread_ts":"2.0"}]}`}}}
	recs, _, err := m.Ops[opThread].records(res)
	if err != nil || len(recs) != 1 || recs[0]["user"] != "U1" || recs[0]["thread_ts"] != "2.0" {
		t.Fatalf("thread %v %v", recs, err)
	}
	// A user wrapped in {"user": ...}.
	res = &mcp.CallToolResult{StructuredContent: map[string]any{"ok": true, "user": map[string]any{"id": "U1", "name": "bob", "profile": map[string]any{"title": "Steeper"}}}}
	recs, _, err = m.Ops[opUser].records(res)
	if err != nil || recs[0]["name"] != "bob" || recs[0]["title"] != "Steeper" {
		t.Fatalf("user %v %v", recs, err)
	}
	// A tool error is an error; prose is ErrProse with its text.
	if _, _, err := m.Ops[opSearch].records(&mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "not_authed"}}}); err == nil || !strings.Contains(err.Error(), "not_authed") {
		t.Fatalf("tool error %v", err)
	}
	text := "Found 2:\n\n**ann** in #eng: look\nhttps://teashop.slack.com/archives/C0ENG/p1793160000000100\n\n**bob**: reply <https://teashop.slack.com/archives/C0ENG/p1793163600000200?thread_ts=1793160000.000100&cid=C0ENG>\n\nno link here"
	_, got, err := m.Ops[opSearch].records(&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}})
	if err != ErrProse || got != text {
		t.Fatalf("prose %v", err)
	}
	p := proseRecords(text)
	if len(p) != 2 || p[0]["channel"] != "C0ENG" || p[0]["ts"] != "1793160000.000100" || p[1]["thread_ts"] != "1793160000.000100" || !strings.Contains(p[0]["text"], "look") {
		t.Fatalf("prose records %v", p)
	}
}

func checkOf(cs []Check, op string) Check {
	for _, c := range cs {
		if c.Op == op {
			return c
		}
	}
	return Check{}
}

func TestMapCheck(t *testing.T) {
	// The defaults against a server shaped like Slack's own: every operation
	// maps; the permalink one, which no candidate matches, is optional.
	checks, _ := checkMapping(emptyMap(), fakeTools(t, fakeOptions{}))
	for _, c := range checks {
		want := "ok"
		if c.Op == opPermalink {
			want = "optional"
		}
		if c.Status != want {
			t.Errorf("%s: %s %v", c.Op, c.Status, c.Problems)
		}
	}
	// A gateway that renamed one tool and one argument.
	opt := fakeOptions{rename: map[string]string{"slack_read_channel": "slack_channel_history"},
		argName: map[string]map[string]string{"slack_read_thread": {"message_ts": "thread_ts"}}}
	tools := fakeTools(t, opt)
	checks, _ = checkMapping(emptyMap(), tools)
	// Auto-pick finds no candidate for the renamed tool: unmapped, with the
	// look-alike as the fix.
	h := checkOf(checks, opHistory)
	if h.Status != "unmapped" || !strings.Contains(strings.Join(h.Problems, " "), "tools that look like it: slack_channel_history") ||
		len(h.Fix) != 1 || h.Fix[0] != "o-slack settings set slack.tools.history slack_channel_history" {
		t.Fatalf("history %+v", h)
	}
	// A tool named in settings the server does not list: missing, with the
	// listed candidate as the fix.
	named := emptyMap()
	named.Tools[opHistory] = "slack_read_channel"
	named.Tools[opSearch] = "runlayer_slack_search"
	nc, _ := checkMapping(named, tools)
	if c := checkOf(nc, opHistory); c.Status != "missing" || !strings.Contains(strings.Join(c.Problems, " "), "the server lists no tool slack_read_channel") ||
		len(c.Fix) != 1 || c.Fix[0] != "o-slack settings set slack.tools.history slack_channel_history" {
		t.Fatalf("named history %+v", c)
	}
	if c := checkOf(nc, opSearch); c.Status != "missing" || len(c.Fix) == 0 || c.Fix[0] != "o-slack settings set slack.tools.search slack_search_public_and_private" {
		t.Fatalf("named search %+v", c)
	}
	th := checkOf(checks, opThread)
	if th.Status != "args" || !strings.Contains(strings.Join(th.Problems, " "), "has no argument message_ts (for ts); did you mean thread_ts?") ||
		!strings.Contains(strings.Join(th.Problems, " "), "requires thread_ts") || len(th.Fix) != 1 ||
		th.Fix[0] != `o-slack settings set slack.args.thread '["channel=channel_id","ts=thread_ts","limit=limit"]'` {
		t.Fatalf("thread %+v", th)
	}
	// The fixes, applied as settings, make the check pass.
	ms := emptyMap()
	ms.Tools[opHistory] = "slack_channel_history"
	ms.Args[opHistory] = []string{"channel=channel_id", "oldest=oldest", "limit=limit"}
	ms.Args[opThread] = []string{"channel=channel_id", "ts=thread_ts", "limit=limit"}
	checks, _ = checkMapping(ms, tools)
	if c := checkOf(checks, opHistory); c.Status != "ok" {
		t.Fatalf("history after fix %+v", c)
	}
	if c := checkOf(checks, opThread); c.Status != "ok" {
		t.Fatalf("thread after fix %+v", c)
	}
	// A search tool the server lacks entirely, with nothing similar.
	checks, _ = checkMapping(emptyMap(), fakeTools(t, fakeOptions{drop: map[string]bool{"slack_search_public_and_private": true}}))
	if c := checkOf(checks, opSearch); c.Status != "unmapped" || len(c.Fix) != 0 {
		t.Fatalf("search %+v", c)
	}
	// Write tools are never suggested.
	if g := similarTools(ops[0], []*mcp.Tool{{Name: "slack_send_message"}, {Name: "slack_search_everything"}}); !reflect.DeepEqual(g, []string{"slack_search_everything"}) {
		t.Fatalf("similar %v", g)
	}
}

func TestMapCheckCommand(t *testing.T) {
	opt := fakeOptions{rename: map[string]string{"slack_read_channel": "slack_channel_history"},
		argName: map[string]map[string]string{"slack_read_thread": {"message_ts": "thread_ts"}}}
	tool, fs, _ := testTool(t, opt)
	// Live: through the server.
	out, err := sh(t, tool, "map", "check", "--format", "table")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"history", "unmapped", "slack.tools.history slack_channel_history", "did you mean thread_ts", "2 of 6 operations need a fix", "live tools/list of explicit:runlayer"} {
		if !strings.Contains(out, want) {
			t.Errorf("live check lacks %q:\n%s", want, out)
		}
	}
	// Offline: o-mcp's saved schemas of the same server.
	if _, err := sh(t, tool, "map", "check", "--offline"); err == nil || !strings.Contains(err.Error(), "o-mcp discover runlayer") {
		t.Fatalf("offline without schemas: %v", err)
	}
	snap := client.Snapshot{Server: fakeServer, Fingerprint: fakeServer.Fingerprint(), Tools: fakeTools(t, fakeOptions{})}
	if err := tool.schemas().Save(snap); err != nil {
		t.Fatal(err)
	}
	dials := fs.dials
	out, err = sh(t, tool, "map", "check", "--offline", "--format", "table")
	if err != nil || !strings.Contains(out, "every operation maps") || !strings.Contains(out, "saved schemas of explicit:runlayer") || fs.dials != dials {
		t.Fatalf("offline %v (dials %d -> %d):\n%s", err, dials, fs.dials, out)
	}
	// A schemas file: a bare tools list.
	b, _ := json.Marshal(fakeTools(t, opt))
	file := filepath.Join(t.TempDir(), "tools.json")
	os.WriteFile(file, b, 0o600)
	out, _ = sh(t, tool, "map", "check", "--schemas", file)
	rows := rowsOf(t, mapNoun, out)
	if len(rows) != 7 || rows[1].Cells[2] != "unmapped" || rows[2].Cells[2] != "args" {
		t.Fatalf("schemas file rows %+v", rows)
	}
	// Fix it with settings only; the check passes and a poll reads the thread.
	sh(t, tool, "settings", "set", "slack.tools.history", "slack_channel_history")
	if _, err := sh(t, tool, "settings", "set", "slack.args.thread", `["channel=channel_id","ts=thread_ts","limit=limit"]`); err != nil {
		t.Fatal(err)
	}
	// The renamed tool is no candidate, so its arguments are next: the check
	// says so, and its fix line is the second round.
	out, _ = sh(t, tool, "map", "check", "--format", "table")
	fix := `o-slack settings set slack.args.history '["channel=channel_id","oldest=oldest","limit=limit"]'`
	if !strings.Contains(out, "1 of 6 operations need a fix") || !strings.Contains(out, fix) {
		t.Fatalf("after the first fix:\n%s", out)
	}
	sh(t, tool, "settings", "set", "slack.args.history", `["channel=channel_id","oldest=oldest","limit=limit"]`)
	out, _ = sh(t, tool, "map", "check", "--format", "table")
	if !strings.Contains(out, "every operation maps") { // permalink, which has no tool, is optional
		t.Fatalf("after fix:\n%s", out)
	}
	out, err = sh(t, tool, "message", "read-thread", "--target", keyMention)
	if err != nil || len(rowsOf(t, messageNoun, out)) != 2 {
		t.Fatalf("read-thread after fix %v\n%s", err, out)
	}
	// map show names where each choice came from.
	out, _ = sh(t, tool, "map", "show", "--format", "table")
	if !strings.Contains(out, "slack_channel_history") || !strings.Contains(out, "settings") || !strings.Contains(out, "candidate") {
		t.Fatalf("show:\n%s", out)
	}
}
