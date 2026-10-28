package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/unixsuperhero/o-mcp/client"
)

// The tests' Slack: fakeSlack is a go-sdk MCP server, served in memory,
// whose tools look like Slack's own MCP server (slack_search_public_and_
// private, slack_read_channel, slack_read_thread, slack_search_channels,
// slack_read_user_profile, and a write tool, slack_send_message, that
// o-slack must never call). Its data is testdata/slack.json. Options
// rename or drop tools and arguments and answer prose, so a test can play
// a server whose names differ from the defaults. No test reaches Slack or
// a real MCP server.

var fixtureNow = time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)

const workspace = "https://teashop.slack.com"

type fixture struct {
	Channels []map[string]any `json:"channels"`
	Users    []map[string]any `json:"users"`
	Messages []map[string]any `json:"messages"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	b, err := os.ReadFile("testdata/slack.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

type fakeOptions struct {
	rename  map[string]string            // tool name -> name the server lists it under
	drop    map[string]bool              // tools the server does not list
	argName map[string]map[string]string // tool -> its argument -> the name the server uses
	prose   bool                         // search answers prose (with permalinks)
}

type fakeSlack struct {
	mu    sync.Mutex
	f     fixture
	opt   fakeOptions
	calls []string // tool names called, in order
	dials int
	fail  error // the next dials fail
}

func newFakeSlack(t *testing.T, opt fakeOptions) *fakeSlack {
	return &fakeSlack{f: loadFixture(t), opt: opt}
}

func (fs *fakeSlack) channel(id string) map[string]any {
	for _, c := range fs.f.Channels {
		if c["id"] == id {
			return c
		}
	}
	return map[string]any{"id": id}
}

func (fs *fakeSlack) user(id string) map[string]any {
	for _, u := range fs.f.Users {
		if u["id"] == id {
			return u
		}
	}
	return nil
}

func (fs *fakeSlack) addMessage(m map[string]any) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.f.Messages = append(fs.f.Messages, m)
}

func str(v any) string { s, _ := v.(string); return s }

func permalinkOf(m map[string]any) string {
	u := workspace + "/archives/" + str(m["channel"]) + "/p" + strings.Replace(str(m["ts"]), ".", "", 1)
	if th := str(m["thread_ts"]); th != "" && th != str(m["ts"]) {
		u += "?thread_ts=" + th + "&cid=" + str(m["channel"])
	}
	return u
}

// server is a fresh go-sdk server over the fake's data.
func (fs *fakeSlack) server() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "fake-slack", Version: "1.0.0"}, &mcp.ServerOptions{PageSize: 3})
	add := func(name string, args []string, required []string, h func(a map[string]any) (*mcp.CallToolResult, error)) {
		if fs.opt.drop[name] {
			return
		}
		listed := name
		if r, ok := fs.opt.rename[name]; ok {
			listed = r
		}
		ren := fs.opt.argName[name]
		props := map[string]any{}
		for _, a := range args {
			if r, ok := ren[a]; ok {
				a = r
			}
			props[a] = map[string]any{"type": "string"}
		}
		var req []string
		for _, r := range required {
			if n, ok := ren[r]; ok {
				r = n
			}
			req = append(req, r)
		}
		schema := map[string]any{"type": "object", "properties": props, "required": req}
		s.AddTool(&mcp.Tool{Name: listed, Description: "fake " + name, InputSchema: schema}, func(ctx context.Context, cr *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var in map[string]any
			_ = json.Unmarshal(cr.Params.Arguments, &in)
			fs.mu.Lock()
			fs.calls = append(fs.calls, listed)
			fs.mu.Unlock()
			// Read arguments under the server's own names.
			a := map[string]any{}
			for _, k := range args {
				n := k
				if r, ok := ren[k]; ok {
					n = r
				}
				if v, ok := in[n]; ok {
					a[k] = v
				}
			}
			for _, r := range required {
				if _, ok := a[r]; !ok {
					return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "missing required argument " + r}}}, nil
				}
			}
			fs.mu.Lock()
			defer fs.mu.Unlock()
			return h(a)
		})
	}
	jsonText := func(v any) *mcp.CallToolResult {
		b, _ := json.Marshal(v)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
	}
	add("slack_search_public_and_private", []string{"query", "limit", "sort"}, []string{"query"}, func(a map[string]any) (*mcp.CallToolResult, error) {
		q := str(a["query"])
		var matches []any
		var prose []string
		for _, m := range fs.f.Messages {
			hit := strings.Contains(q, "<@U0ME>") && strings.Contains(str(m["text"]), "<@U0ME>") ||
				strings.Contains(q, "to:me") && strings.HasPrefix(str(m["channel"]), "D")
			if !hit {
				continue
			}
			ch := fs.channel(str(m["channel"]))
			u := fs.user(str(m["user"]))
			match := map[string]any{"ts": m["ts"], "text": m["text"], "user": m["user"], "username": u["name"],
				"channel": map[string]any{"id": ch["id"], "name": ch["name"], "is_im": ch["is_im"] == true}, "permalink": permalinkOf(m)}
			if th := str(m["thread_ts"]); th != "" {
				match["thread_ts"] = th
			}
			matches = append(matches, match)
			prose = append(prose, fmt.Sprintf("**%s** in #%s: %s\n%s", u["name"], ch["name"], m["text"], permalinkOf(m)))
		}
		if fs.opt.prose {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Found " + strconv.Itoa(len(prose)) + " messages:\n\n" + strings.Join(prose, "\n\n")}}}, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "results"}},
			StructuredContent: map[string]any{"ok": true, "messages": map[string]any{"total": len(matches), "matches": matches}}}, nil
	})
	add("slack_read_channel", []string{"channel_id", "oldest", "limit"}, []string{"channel_id"}, func(a map[string]any) (*mcp.CallToolResult, error) {
		oldest, _ := strconv.ParseFloat(str(a["oldest"]), 64)
		out := []any{}
		for _, m := range fs.f.Messages {
			ts, _ := strconv.ParseFloat(str(m["ts"]), 64)
			if m["channel"] == a["channel_id"] && ts > oldest && (str(m["thread_ts"]) == "" || m["thread_ts"] == m["ts"]) {
				out = append(out, map[string]any{"ts": m["ts"], "user": m["user"], "text": m["text"], "reply_count": m["reply_count"]})
			}
		}
		return jsonText(map[string]any{"ok": true, "messages": out}), nil
	})
	add("slack_read_thread", []string{"channel_id", "message_ts", "limit"}, []string{"channel_id", "message_ts"}, func(a map[string]any) (*mcp.CallToolResult, error) {
		out := []any{}
		for _, m := range fs.f.Messages {
			if m["channel"] == a["channel_id"] && (m["ts"] == a["message_ts"] || m["thread_ts"] == a["message_ts"]) {
				msg := map[string]any{"ts": m["ts"], "user": m["user"], "text": m["text"]}
				if th := str(m["thread_ts"]); th != "" {
					msg["thread_ts"] = th
				}
				out = append(out, msg)
			}
		}
		return jsonText(map[string]any{"ok": true, "messages": out}), nil
	})
	add("slack_search_channels", []string{"query", "limit"}, nil, func(a map[string]any) (*mcp.CallToolResult, error) {
		return jsonText(map[string]any{"channels": fs.f.Channels}), nil
	})
	add("slack_read_user_profile", []string{"user_id"}, []string{"user_id"}, func(a map[string]any) (*mcp.CallToolResult, error) {
		u := fs.user(str(a["user_id"]))
		if u == nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "user_not_found"}}}, nil
		}
		return jsonText(map[string]any{"ok": true, "user": u}), nil
	})
	add("slack_send_message", []string{"channel_id", "text"}, []string{"channel_id", "text"}, func(a map[string]any) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "o-slack must never write"}}}, nil
	})
	return s
}

// dial is the Tool's Dialer: a fresh in-memory session with the fake.
func (fs *fakeSlack) dial(ctx context.Context, srv client.Server) (Conn, error) {
	fs.mu.Lock()
	fs.dials++
	fail := fs.fail
	fs.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	ct, st := mcp.NewInMemoryTransports()
	go fs.server().Run(context.Background(), st)
	return client.ConnectTransport(ctx, srv, ct, client.Options{Timeout: 5 * time.Second})
}

func (fs *fakeSlack) called() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]string{}, fs.calls...)
}

// fakeServer is the resolved server: one client.Server named runlayer.
var fakeServer = client.NewServer("runlayer", map[string]any{"url": "https://runlayer.example/mcp/slack", "headers": map[string]any{"Authorization": "Bearer ${RUNLAYER_TOKEN}"}})

func fakeResolve(ref string, fallbacks []string) (client.Server, error) {
	return findServer([]client.Server{fakeServer}, ref, fallbacks)
}

// inboxLog records what a fake inbox received.
type inboxLog struct {
	mu   sync.Mutex
	got  []Signal
	fail map[string]bool // keys whose delivery fails
}

func (l *inboxLog) push(_ context.Context, s Signal) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail[s.Key] {
		return fmt.Errorf("inbox refused %s", s.Key)
	}
	l.got = append(l.got, s)
	return nil
}

// testTool is a Tool on temp data, config and o-mcp directories with the
// fake Slack, the fake inbox and settings for "miso".
func testTool(t *testing.T, opt fakeOptions) (*Tool, *fakeSlack, *inboxLog) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("O_SLACK_DATA", "")
	fs, inbox := newFakeSlack(t, opt), &inboxLog{fail: map[string]bool{}}
	mcpDir := t.TempDir()
	opened := []string{}
	tool := &Tool{store: Store{Dir: t.TempDir()}, resolve: fakeResolve, dial: fs.dial,
		schemas: func() client.Store { return client.Store{Dir: mcpDir} }, inbox: inbox.push,
		now:    func() time.Time { return fixtureNow },
		opener: func(_ context.Context, u string) error { opened = append(opened, u); return nil },
		clip:   func(string) error { return nil },
		Stdin:  strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if err := tool.newRuntime(true); err != nil {
		t.Fatal(err)
	}
	set(t, tool, setMe, "miso")
	set(t, tool, setMeID, "U0ME")
	set(t, tool, setWorkspace, workspace)
	return tool, fs, inbox
}

// set changes a setting in the tool's settings file.
func set(t *testing.T, tool *Tool, name string, v any) {
	t.Helper()
	if err := tool.r.Settings().Set(name, v); err != nil {
		t.Fatalf("set %s: %v", name, err)
	}
}

const (
	keyMention = "C0ENG/1793160000.000100"
	keyReply   = "C0ENG/1793163600.000200"
	keyDM      = "D0BOB/1793164000.000300"
	keyDeploy  = "C0OPS/1793165000.000400"
	keyCake    = "C0RAND/1793166000.000500"
	keyMine    = "C0ENG/1793166500.000600"
	keyKettle  = "C0RAND/1793100000.000700"
)
