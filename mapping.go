package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/unixsuperhero/o-mcp/client"
)

// The mapping: o-slack's operations (search, history, thread, channels,
// user, permalink) are calls to tools of whatever Slack MCP server
// slack.server names. Which tool, which of its arguments carries each of
// o-slack's parameters and where each field sits in its result are
// settings, so a server with other names is fixed by editing settings,
// not code:
//
//	slack.tools.OP    the tool's name ("" picks the first candidate the server lists)
//	slack.args.OP     PARAM=SERVER_ARG pairs, and SERVER_ARG:=VALUE constants
//	slack.results.OP  FIELD=PATH pairs; a PATH is dotted keys, | between alternatives
//
// The candidates (the one table of tool names o-slack ships) are guesses
// in order of likelihood; none is verified against a live server. o-slack
// map check compares the mapping with the tools a server lists.

// Operation names.
const (
	opSearch    = "search"
	opHistory   = "history"
	opThread    = "thread"
	opChannels  = "channels"
	opUser      = "user"
	opPermalink = "permalink"
)

// opSpec is one operation: its parameters (required ones first named in
// Required), the result shape it reads, and whether a poll needs it.
type opSpec struct {
	Name     string
	About    string
	Params   []string
	Required []string
	Shape    string // messages, channels, user, permalink
	Needed   string // when a poll calls it
}

var ops = []opSpec{
	{opSearch, "search messages (the mentions and DMs queries)", []string{"query", "limit"}, []string{"query"}, "messages", "every poll"},
	{opHistory, "read a channel's recent messages (on-call and watched channels)", []string{"channel", "oldest", "limit"}, []string{"channel"}, "messages", "on-call or watched channels"},
	{opThread, "read a thread's replies (watched threads, read-thread)", []string{"channel", "ts", "limit"}, []string{"channel", "ts"}, "messages", "watched threads"},
	{opChannels, "list or search channels (the Channels list)", []string{"query", "limit"}, nil, "channels", "the Channels list"},
	{opUser, "one user's profile (slack-user show)", []string{"user"}, []string{"user"}, "user", "slack-user show"},
	{opPermalink, "a message's permalink (when results carry none and slack.workspace-url is unset)", []string{"channel", "ts"}, []string{"channel", "ts"}, "permalink", "only when a message has no permalink"},
}

func opByName(name string) (opSpec, bool) {
	for _, o := range ops {
		if o.Name == name {
			return o, true
		}
	}
	return opSpec{}, false
}

// candidate is one guess at a server's tool for an operation.
type candidate struct {
	Tool   string
	Args   []string // PARAM=SERVER_ARG
	Server string   // which server it is believed to come from
}

// candidates is the one table of tool names: per operation, the tools
// o-slack tries in order. Slack's own MCP server (what a gateway such as
// Runlayer fronts) comes first; then korotovsky/slack-mcp-server and the
// archived reference server @modelcontextprotocol/server-slack; then
// generic names. Unverified: o-slack map check says which the server has.
var candidates = map[string][]candidate{
	opSearch: {
		{"slack_search_public_and_private", []string{"query=query", "limit=limit"}, "Slack's MCP server"},
		{"slack_search_public", []string{"query=query", "limit=limit"}, "Slack's MCP server (public only)"},
		{"conversations_search_messages", []string{"query=search_query", "limit=limit"}, "korotovsky/slack-mcp-server"},
		{"search_messages", []string{"query=query", "limit=count"}, "generic"},
		{"search", []string{"query=query"}, "generic (Glean)"},
	},
	opHistory: {
		{"slack_read_channel", []string{"channel=channel_id", "oldest=oldest", "limit=limit"}, "Slack's MCP server"},
		{"conversations_history", []string{"channel=channel_id", "limit=limit"}, "korotovsky/slack-mcp-server"},
		{"slack_get_channel_history", []string{"channel=channel_id", "limit=limit"}, "reference server-slack"},
	},
	opThread: {
		{"slack_read_thread", []string{"channel=channel_id", "ts=message_ts", "limit=limit"}, "Slack's MCP server"},
		{"conversations_replies", []string{"channel=channel_id", "ts=thread_ts", "limit=limit"}, "korotovsky/slack-mcp-server"},
		{"slack_get_thread_replies", []string{"channel=channel_id", "ts=thread_ts"}, "reference server-slack"},
	},
	opChannels: {
		{"slack_search_channels", []string{"query=query", "limit=limit"}, "Slack's MCP server"},
		{"channels_list", []string{"limit=limit"}, "korotovsky/slack-mcp-server"},
		{"slack_list_channels", []string{"limit=limit"}, "reference server-slack"},
	},
	opUser: {
		{"slack_read_user_profile", []string{"user=user_id"}, "Slack's MCP server"},
		{"slack_get_user_profile", []string{"user=user_id"}, "reference server-slack"},
	},
	opPermalink: {
		{"slack_get_permalink", []string{"channel=channel", "ts=message_ts"}, "generic (chat.getPermalink)"},
	},
}

// defaultResults are where each field is looked for when slack.results.OP
// does not say: the spellings Slack's Web API, the servers above and
// matomeru-old's fixtures use. items "" finds the first list of objects.
var defaultResults = map[string][]string{
	"messages": {
		"items=",
		"ts=ts|timestamp|message_ts|messageTs|id",
		"thread_ts=thread_ts|threadTs|parent_ts",
		"channel=channel.id|channel_id|channelId|channel.name|channel|conversation_id",
		"channel_name=channel.name|channel_name|channelName|channel",
		"user=user|user_id|userId|from.id|author.id",
		"user_name=username|user_name|userName|user_profile.real_name|user_profile.display_name|real_name|from.name|author.name|user",
		"text=text|message|content|body",
		"permalink=permalink|url|link|message_link",
		"is_im=is_im|channel.is_im|is_dm|channel.is_dm",
	},
	"channels": {
		"items=",
		"id=id|channel_id|channelId",
		"name=name|name_normalized|channel_name",
		"private=is_private|private|is_group",
		"topic=topic.value|topic|purpose.value|purpose",
		"members=num_members|member_count|members_count",
		"is_im=is_im",
	},
	"user": {
		"items=.",
		"id=id|user_id|user.id|profile.id",
		"name=name|username|user.name|display_name|profile.display_name",
		"real_name=real_name|profile.real_name|user.real_name",
		"title=title|profile.title|user.profile.title",
		"email=email|profile.email|user.profile.email",
		"tz=tz|timezone|user.tz",
	},
	"permalink": {
		"items=.",
		"permalink=permalink|url|link",
	},
}

// Arg is one mapped argument: an o-slack parameter carried by a server
// argument, or a constant the server is always sent.
type Arg struct {
	Param   string `json:"param,omitempty"`
	Server  string `json:"server"`
	Const   any    `json:"const,omitempty"`
	IsConst bool   `json:"is_const,omitempty"`
}

func (a Arg) String() string {
	if a.IsConst {
		b, _ := json.Marshal(a.Const)
		return a.Server + ":=" + string(b)
	}
	return a.Param + "=" + a.Server
}

// OpMap is one operation resolved: the tool, its arguments, where its
// fields are, and where each choice came from.
type OpMap struct {
	Op      string              `json:"op"`
	Tool    string              `json:"tool"`
	From    string              `json:"from"` // settings, candidate, default (unverified), none
	Note    string              `json:"note,omitempty"`
	Args    []Arg               `json:"args"`
	Results map[string][]string `json:"results"`
}

// MapSettings is the mapping as the settings give it.
type MapSettings struct {
	Tools   map[string]string
	Args    map[string][]string
	Results map[string][]string
}

// parseArgs reads PARAM=SERVER_ARG and SERVER_ARG:=VALUE entries.
func parseArgs(op opSpec, entries []string) ([]Arg, []string) {
	var out []Arg
	var problems []string
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if k, v, ok := strings.Cut(e, ":="); ok {
			var val any = v
			var parsed any
			if json.Unmarshal([]byte(v), &parsed) == nil {
				val = parsed
			}
			out = append(out, Arg{Server: strings.TrimSpace(k), Const: val, IsConst: true})
			continue
		}
		k, v, ok := strings.Cut(e, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			problems = append(problems, fmt.Sprintf("slack.args.%s: %q is not PARAM=SERVER_ARG or SERVER_ARG:=VALUE", op.Name, e))
			continue
		}
		if !contains(op.Params, k) {
			problems = append(problems, fmt.Sprintf("slack.args.%s: %q is not a parameter of %s (%s)", op.Name, k, op.Name, strings.Join(op.Params, ", ")))
			continue
		}
		out = append(out, Arg{Param: k, Server: v})
	}
	return out, problems
}

// parseResults reads FIELD=PATH entries over the shape's defaults.
func parseResults(shape string, entries []string) map[string][]string {
	out := map[string][]string{}
	for _, list := range [][]string{defaultResults[shape], entries} {
		for _, e := range list {
			k, v, ok := strings.Cut(strings.TrimSpace(e), "=")
			if !ok || strings.TrimSpace(k) == "" {
				continue
			}
			var paths []string
			for _, p := range strings.Split(v, "|") {
				paths = append(paths, strings.TrimSpace(p))
			}
			out[strings.TrimSpace(k)] = paths
		}
	}
	return out
}

// resolve is the operation's mapping: the tool slack.tools.OP names, else
// the first candidate the server lists (tools; nil when not known, which
// takes the first candidate unverified). Arguments are slack.args.OP when
// set, else the candidate's, else each parameter under its own name.
func resolve(op opSpec, ms MapSettings, tools []*mcp.Tool) (OpMap, []string) {
	m := OpMap{Op: op.Name, Results: parseResults(op.Shape, ms.Results[op.Name])}
	var cand *candidate
	if name := strings.TrimSpace(ms.Tools[op.Name]); name != "" {
		m.Tool, m.From = name, "settings"
		for i, c := range candidates[op.Name] {
			if c.Tool == name {
				cand = &candidates[op.Name][i]
			}
		}
	} else {
		for i, c := range candidates[op.Name] {
			if tools == nil || hasTool(tools, c.Tool) {
				cand = &candidates[op.Name][i]
				m.Tool, m.From = c.Tool, "candidate"
				if tools == nil {
					m.From = "default (unverified)"
				}
				break
			}
		}
		if cand == nil {
			m.From = "none"
		}
	}
	if cand != nil {
		m.Note = cand.Server
	}
	args, problems := parseArgs(op, ms.Args[op.Name])
	switch {
	case len(args) > 0:
	case cand != nil:
		args, _ = parseArgs(op, cand.Args)
	default:
		for _, p := range op.Params {
			args = append(args, Arg{Param: p, Server: p})
		}
	}
	m.Args = args
	return m, problems
}

func hasTool(tools []*mcp.Tool, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// Mapping is every operation resolved against one server's tools.
type Mapping struct {
	Ops      map[string]OpMap
	Problems []string
}

func resolveAll(ms MapSettings, tools []*mcp.Tool) Mapping {
	m := Mapping{Ops: map[string]OpMap{}}
	for _, op := range ops {
		om, p := resolve(op, ms, tools)
		m.Ops[op.Name] = om
		m.Problems = append(m.Problems, p...)
	}
	return m
}

// ErrUnmapped: the operation has no tool (none set, no candidate listed).
var ErrUnmapped = errors.New("no tool")

// build is the arguments a call sends: each mapped parameter given a
// nonempty value, and the constants. A required parameter no argument
// carries is an error.
func (m OpMap) build(values map[string]any) (map[string]any, error) {
	if m.Tool == "" {
		return nil, fmt.Errorf("%s: %w (set slack.tools.%s; o-slack map check lists the server's tools)", m.Op, ErrUnmapped, m.Op)
	}
	op, _ := opByName(m.Op)
	out := map[string]any{}
	carried := map[string]bool{}
	for _, a := range m.Args {
		if a.IsConst {
			out[a.Server] = a.Const
			continue
		}
		carried[a.Param] = true
		v, ok := values[a.Param]
		if !ok || v == nil || v == "" || v == 0 {
			continue
		}
		out[a.Server] = v
	}
	for _, r := range op.Required {
		if !carried[r] {
			return nil, fmt.Errorf("%s: no argument of %s carries %s (slack.args.%s)", m.Op, m.Tool, r, m.Op)
		}
	}
	return out, nil
}

// schema is the part of a tool's input schema map check reads.
type schema struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
}

func toolSchema(t *mcp.Tool) schema {
	var s schema
	b, _ := json.Marshal(t.InputSchema)
	_ = json.Unmarshal(b, &s)
	return s
}

// Check is one operation compared with the server's tools.
type Check struct {
	Op       string   `json:"op"`
	Tool     string   `json:"tool"`
	From     string   `json:"from"`
	Status   string   `json:"status"` // ok, missing, args, unmapped, optional
	Needed   string   `json:"needed"`
	Problems []string `json:"problems,omitempty"`
	Fix      []string `json:"fix,omitempty"`
	Args     []string `json:"args"`
	Schema   []string `json:"schema,omitempty"`
}

// checkMapping compares every operation's mapping with tools: a tool the
// server does not list, a mapped argument its schema lacks (renamed), a
// required schema argument nothing sends, a required parameter nothing
// carries. Each problem comes with the settings line that fixes it when
// one can be told.
func checkMapping(ms MapSettings, tools []*mcp.Tool) ([]Check, []string) {
	m := resolveAll(ms, tools)
	var out []Check
	for _, op := range ops {
		om := m.Ops[op.Name]
		c := Check{Op: op.Name, Tool: om.Tool, From: om.From, Needed: op.Needed, Status: "ok"}
		for _, a := range om.Args {
			c.Args = append(c.Args, a.String())
		}
		var tool *mcp.Tool
		for _, t := range tools {
			if t.Name == om.Tool {
				tool = t
			}
		}
		switch {
		case om.Tool == "" && op.Name == opPermalink:
			// Optional: results carry permalinks, or slack.workspace-url builds them.
			c.Status = "optional"
			c.Problems = append(c.Problems, "no permalink tool ("+candidateNames(op.Name)+"); permalinks come from results or are built from slack.workspace-url")
		case om.Tool == "":
			c.Status = "unmapped"
			c.Problems = append(c.Problems, "the server lists none of the candidates ("+candidateNames(op.Name)+")")
			if guess := similarTools(op, tools); len(guess) > 0 {
				c.Fix = append(c.Fix, "o-slack settings set slack.tools."+op.Name+" "+guess[0])
				c.Problems = append(c.Problems, "tools that look like it: "+strings.Join(guess, ", "))
			}
		case tool == nil:
			c.Status = "missing"
			c.Problems = append(c.Problems, "the server lists no tool "+om.Tool)
			for _, cand := range candidates[op.Name] {
				if hasTool(tools, cand.Tool) {
					c.Fix = append(c.Fix, "o-slack settings set slack.tools."+op.Name+" "+cand.Tool)
				}
			}
			if guess := similarTools(op, tools); len(guess) > 0 {
				c.Problems = append(c.Problems, "tools that look like it: "+strings.Join(guess, ", "))
				if len(c.Fix) == 0 {
					c.Fix = append(c.Fix, "o-slack settings set slack.tools."+op.Name+" "+guess[0])
				}
			}
		default:
			s := toolSchema(tool)
			for name := range s.Properties {
				c.Schema = append(c.Schema, name)
			}
			sort.Strings(c.Schema)
			sent := map[string]bool{}
			fixed := false
			var args []string
			for _, a := range om.Args {
				sent[a.Server] = true
				if len(s.Properties) > 0 {
					if _, ok := s.Properties[a.Server]; !ok {
						c.Status = "args"
						msg := fmt.Sprintf("%s has no argument %s", om.Tool, a.Server)
						if a.IsConst {
							msg += " (a constant)"
						} else {
							msg += " (for " + a.Param + ")"
							if g := similarArg(a, s); g != "" {
								msg += "; did you mean " + g + "?"
								a.Server = g
								fixed = true
							}
						}
						c.Problems = append(c.Problems, msg)
					}
				}
				args = append(args, a.String())
			}
			for _, r := range s.Required {
				if !sent[r] {
					c.Status = "args"
					c.Problems = append(c.Problems, fmt.Sprintf("%s requires %s, which nothing sends (map a parameter to it, or add %s:=VALUE)", om.Tool, r, r))
				}
			}
			carried := map[string]bool{}
			for _, a := range om.Args {
				carried[a.Param] = true
			}
			for _, r := range op.Required {
				if !carried[r] {
					c.Status = "args"
					c.Problems = append(c.Problems, "no argument carries "+r)
				}
			}
			if fixed {
				b, _ := json.Marshal(args)
				c.Fix = append(c.Fix, "o-slack settings set slack.args."+op.Name+" '"+string(b)+"'")
			}
		}
		out = append(out, c)
	}
	return out, m.Problems
}

func candidateNames(op string) string {
	var n []string
	for _, c := range candidates[op] {
		n = append(n, c.Tool)
	}
	return strings.Join(n, ", ")
}

// opWords are the words a tool name for each operation tends to hold.
var opWords = map[string][]string{
	opSearch:    {"search"},
	opHistory:   {"history", "read_channel", "channel_messages"},
	opThread:    {"thread", "replies"},
	opChannels:  {"channels", "list_channels", "search_channels", "conversations_list"},
	opUser:      {"user", "profile"},
	opPermalink: {"permalink"},
}

// similarTools are the server's tools whose names hold the operation's
// words, sorted, without write tools.
func similarTools(op opSpec, tools []*mcp.Tool) []string {
	var out []string
	for _, t := range tools {
		n := strings.ToLower(t.Name)
		if writeTool(n) {
			continue
		}
		for _, w := range opWords[op.Name] {
			if strings.Contains(n, w) {
				if op.Name == opSearch && (strings.Contains(n, "channel") || strings.Contains(n, "user")) {
					continue
				}
				out = append(out, t.Name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// writeTool reports a tool name that writes to Slack: o-slack never maps
// one (it is read only toward Slack).
func writeTool(name string) bool {
	for _, w := range []string{"send", "post", "reply", "react", "update", "delete", "create", "schedule", "add_", "remove", "invite", "join", "archive", "set_", "draft", "upload", "mark"} {
		if strings.Contains(name, w) {
			return true
		}
	}
	return false
}

// argWords are the spellings each parameter goes by.
var argWords = map[string][]string{
	"query":   {"query", "search_query", "q", "text", "search"},
	"limit":   {"limit", "count", "max_results", "page_size"},
	"channel": {"channel_id", "channel", "channelid", "conversation_id", "conversation", "channel_name"},
	"oldest":  {"oldest", "after", "since", "start", "from"},
	"ts":      {"message_ts", "thread_ts", "ts", "timestamp", "parent_ts"},
	"user":    {"user_id", "user", "userid", "id"},
}

// similarArg is a schema argument that is likely the one a mapped
// argument was renamed to.
func similarArg(a Arg, s schema) string {
	for _, w := range argWords[a.Param] {
		for name := range s.Properties {
			if strings.EqualFold(name, w) {
				return name
			}
		}
	}
	return ""
}

// Results: a call's result as records.

// ErrProse: the result is text, not JSON.
var ErrProse = errors.New("the server answered prose, not JSON")

// records is a result's records of the operation's shape, each a field
// map: from structuredContent or one JSON text block through the result
// paths. A tool error is an error; prose is ErrProse with the text.
func (m OpMap) records(res *mcp.CallToolResult) ([]map[string]string, string, error) {
	if res.IsError {
		return nil, "", fmt.Errorf("%s: %s answered an error: %s", m.Op, m.Tool, resultText(res))
	}
	data, err := client.Data(res)
	if err != nil {
		return nil, resultText(res), ErrProse
	}
	op, _ := opByName(m.Op)
	items := m.items(data, op.Shape)
	out := make([]map[string]string, 0, len(items))
	for _, it := range items {
		rec := map[string]string{}
		for field, paths := range m.Results {
			if field == "items" {
				continue
			}
			for _, p := range paths {
				if v, ok := lookup(it, p); ok {
					rec[field] = v
					break
				}
			}
		}
		out = append(out, rec)
	}
	return out, "", nil
}

// items are the result's records: the items path, else (empty) the data
// itself when it is a list of objects, else the first list of objects
// found depth first that has the shape's key field; "." is the data as
// one record.
func (m OpMap) items(data any, shape string) []map[string]any {
	paths := m.Results["items"]
	for _, p := range paths {
		switch p {
		case "":
			key := map[string]string{"messages": "text", "channels": "name", "user": "id", "permalink": "permalink"}[shape]
			if l := findList(data, key); l != nil {
				return l
			}
		case ".":
			if o, ok := data.(map[string]any); ok {
				// A user is often wrapped: {"user": {...}} or {"profile": {...}}.
				for _, k := range []string{"user", "profile", "result"} {
					if inner, ok := o[k].(map[string]any); ok && shape == "user" && o["id"] == nil {
						return []map[string]any{inner}
					}
				}
				return []map[string]any{o}
			}
		default:
			if v, ok := walk(data, p); ok {
				if l := objects(v); l != nil {
					return l
				}
				if o, ok := v.(map[string]any); ok {
					return []map[string]any{o}
				}
			}
		}
	}
	return nil
}

func objects(v any) []map[string]any {
	a, ok := v.([]any)
	if !ok {
		return nil
	}
	out := []map[string]any{}
	for _, x := range a {
		o, ok := x.(map[string]any)
		if !ok {
			return nil
		}
		out = append(out, o)
	}
	return out
}

// findList is the first list of objects, depth first, in which some
// object has key (now's _message_list).
func findList(v any, key string) []map[string]any {
	if l := objects(v); len(l) > 0 {
		for _, o := range l {
			if _, ok := o[key]; ok {
				return l
			}
		}
	}
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if l := findList(x[k], key); l != nil {
				return l
			}
		}
	case []any:
		for _, e := range x {
			if l := findList(e, key); l != nil {
				return l
			}
		}
	}
	return nil
}

// walk follows a dotted path.
func walk(v any, path string) (any, bool) {
	if path == "" || path == "." {
		return v, true
	}
	for _, k := range strings.Split(path, ".") {
		o, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = o[k]; !ok {
			return nil, false
		}
	}
	return v, true
}

// lookup is a path's value as text: a string, number or boolean; an
// object, a list or an empty string is no value (the next alternative).
func lookup(v any, path string) (string, bool) {
	x, ok := walk(v, path)
	if !ok {
		return "", false
	}
	switch t := x.(type) {
	case string:
		return t, t != ""
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case json.Number:
		return t.String(), true
	case bool:
		return strconv.FormatBool(t), true
	}
	return "", false
}

func resultText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// permalinkRE is a Slack message permalink: the workspace, the channel,
// the ts without its dot, and a reply's thread_ts.
var permalinkRE = regexp.MustCompile(`https://[A-Za-z0-9.-]+\.slack\.com/archives/([A-Z0-9]+)/p(\d{10})(\d{6})(?:\?[^\s)>\]]*?thread_ts=(\d+\.\d+))?[^\s)>\]]*`)

// proseHeadRE is a prose message's head: **NAME**, then in #CHANNEL, then a colon.
var proseHeadRE = regexp.MustCompile(`^\*\*([^*\n]+)\*\*(?:\s+in\s+#([A-Za-z0-9._-]+))?\s*:?`)

// proseRecords reads a prose result: each paragraph holding a permalink is
// one message (its channel and ts from the permalink, its text the
// paragraph). Prose with no permalink has no records.
func proseRecords(text string) []map[string]string {
	var out []map[string]string
	for _, para := range strings.Split(text, "\n\n") {
		m := permalinkRE.FindStringSubmatch(para)
		if m == nil {
			continue
		}
		text := strings.TrimSpace(permalinkRE.ReplaceAllString(para, ""))
		rec := map[string]string{"permalink": m[0], "channel": m[1], "ts": m[2] + "." + m[3]}
		// A leading **NAME** (in #CHANNEL): the author and the channel's name.
		if h := proseHeadRE.FindStringSubmatch(text); h != nil {
			rec["user_name"] = h[1]
			if h[2] != "" {
				rec["channel_name"] = h[2]
			}
			text = strings.TrimSpace(text[len(h[0]):])
		}
		rec["text"] = strings.Trim(text, "<> \n")
		if m[4] != "" {
			rec["thread_ts"] = m[4]
		}
		out = append(out, rec)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
