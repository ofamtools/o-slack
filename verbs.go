package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"ofam.dev/ochazuke"
	ocli "ofam.dev/ochazuke/cli"
)

// The verbs' handlers. Each reads the settings on the loop into a Config
// and does its work off it (ochazuke.Pending); a verb that changed what
// the lists show reloads the page after.

func (t *Tool) after(res ochazuke.Result) ochazuke.Result {
	return res.Then(func(ochazuke.Ctx, any, error) {
		if t.changed != nil {
			t.changed()
		}
	})
}

func refOf(inst *ochazuke.Instance, what string) (string, error) {
	if inst == nil {
		return "", errors.New("name a " + what + ": --target")
	}
	id, _ := inst.Data.(string)
	if id == "" {
		id = inst.ID
	}
	return id, nil
}

func boolArg(a ochazuke.Arguments, name string) bool { v, _ := a[name].(bool); return v }

// Messages.

// msgRef is a message as a verb names it.
type msgRef struct{ Channel, TS, ThreadTS string }

func (r msgRef) Key() string { return r.Channel + "/" + r.TS }

func (t *Tool) onMessage(run func(ctx context.Context, cfg Config, m Message, args ochazuke.Arguments) (any, error)) ochazuke.AppHandler {
	return func(ctx ochazuke.Ctx) ochazuke.Result {
		ref, err := refOf(ctx.Instance, "message (slack-message/CHANNEL/TS, CHANNEL/TS or a permalink)")
		if err != nil {
			return ochazuke.Failed(err)
		}
		ch, ts, thread, err := parseMessageRef(ref)
		if err != nil {
			return ochazuke.Failed(err)
		}
		cfg, args := t.config(), ctx.Args
		return t.after(ochazuke.Pending(func(c context.Context) (any, error) {
			m := t.knownMessage(cfg, msgRef{ch, ts, thread})
			if strings.HasPrefix(ref, "https://") && m.Permalink == "" {
				m.Permalink = ref
			}
			return run(c, cfg, m, args)
		}))
	}
}

// knownMessage is what o-slack knows of a message without polling: the
// cached poll's record, else a stub of its reference (record first).
func (t *Tool) knownMessage(cfg Config, r msgRef) Message {
	var c cachedPoll
	st, _ := t.store.State()
	if ok, _ := t.store.read(pollFile, &c); ok {
		for _, m := range c.Poll.Messages {
			if m.Key == r.Key() {
				m = classify(m, cfg.rules(st, t.now()))
				if m.Permalink == "" {
					m.Permalink = buildPermalink(cfg.Workspace, m)
				}
				return m
			}
		}
	}
	m := Message{Key: r.Key(), Channel: r.Channel, TS: r.TS, ThreadTS: r.ThreadTS, Text: "(not read yet)", Stale: true}
	if tt, ok := tsTime(r.TS); ok {
		m.At = tt.Format(time.RFC3339)
	}
	m = classify(m, cfg.rules(st, t.now()))
	m.Permalink = buildPermalink(cfg.Workspace, m)
	return m
}

// permalink is a message's link: its own, else built from slack.
// workspace-url, else the permalink operation's answer.
func (t *Tool) permalink(ctx context.Context, cfg Config, m Message) (string, error) {
	if m.Permalink != "" {
		return m.Permalink, nil
	}
	s, err := t.connect(ctx, cfg)
	if err != nil {
		return "", err
	}
	defer s.Close()
	recs, err := s.read(ctx, opPermalink, map[string]any{"channel": m.Channel, "ts": m.TS})
	if err != nil {
		return "", fmt.Errorf("%s: no permalink: set slack.workspace-url (https://NAME.slack.com), or map slack.tools.permalink: %w", m.Key, err)
	}
	if len(recs) == 0 || recs[0]["permalink"] == "" {
		return "", fmt.Errorf("%s: the permalink tool answered no permalink", m.Key)
	}
	return recs[0]["permalink"], nil
}

func (t *Tool) listMessages(ctx ochazuke.Ctx) ochazuke.Result {
	which := ochazuke.StringArg(ctx.Args, "collection", "all")
	if _, ok := collectionByID(which); !ok && which != "all" {
		return ochazuke.Failed(fmt.Errorf("collection %q: one of all, mentions, dms, threads, oncall, ignored", which))
	}
	cfg, fresh := t.config(), boolArg(ctx.Args, "fresh")
	return ochazuke.Pending(func(c context.Context) (any, error) {
		cs, err := t.collections(c, cfg, fresh)
		if err != nil {
			return nil, err
		}
		if which != "all" {
			return t.messageRows(cs[which]), nil
		}
		var all []Message
		for _, col := range collections {
			if col.ID != colIgnored {
				all = append(all, cs[col.ID]...)
			}
		}
		return t.messageRows(all), nil
	})
}

func (t *Tool) collections(ctx context.Context, cfg Config, fresh bool) (Collections, error) {
	p, st, err := t.current(ctx, cfg, fresh, time.Time{})
	if err != nil {
		return nil, err
	}
	return collect(p, cfg.rules(st, t.now()), cfg.Workspace), nil
}

func (t *Tool) collectionRows(id string) ochazuke.AppHandler {
	return func(ctx ochazuke.Ctx) ochazuke.Result {
		cfg, fresh := t.config(), boolArg(ctx.Args, "fresh")
		return ochazuke.Pending(func(c context.Context) (any, error) {
			cs, err := t.collections(c, cfg, fresh)
			if err != nil {
				return nil, err
			}
			return t.messageRows(cs[id]), nil
		})
	}
}

func (t *Tool) messageRows(ms []Message) []ocli.Row {
	out := make([]ocli.Row, 0, len(ms))
	for _, m := range ms {
		out = append(out, messageRow(m, t.now()))
	}
	return out
}

func (t *Tool) showMessage(_ context.Context, _ Config, m Message, _ ochazuke.Arguments) (any, error) {
	return m, nil
}

func (t *Tool) openMessage(ctx context.Context, cfg Config, m Message, _ ochazuke.Arguments) (any, error) {
	u, err := t.permalink(ctx, cfg, m)
	if err != nil {
		return nil, err
	}
	return map[string]string{"opened": u}, t.opener(ctx, u)
}

func (t *Tool) copyMessageLink(ctx context.Context, cfg Config, m Message, _ ochazuke.Arguments) (any, error) {
	u, err := t.permalink(ctx, cfg, m)
	if err != nil {
		return nil, err
	}
	return map[string]any{"permalink": u, "copied": t.clip(u) == nil}, nil
}

func (t *Tool) readThread(ctx context.Context, cfg Config, m Message, _ ochazuke.Arguments) (any, error) {
	ts := m.ThreadTS
	if ts == "" {
		ts = m.TS
	}
	s, err := t.connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	ms, err := s.messages(ctx, opThread, "thread", map[string]any{"channel": m.Channel, "ts": ts, "limit": cfg.Limit})
	if err != nil {
		return nil, err
	}
	st, _ := t.store.State()
	rules := cfg.rules(st, t.now())
	for i := range ms {
		if ms[i].Channel == "" {
			ms[i].Channel = m.Channel
		}
		if ms[i].ThreadTS == "" && ms[i].TS != ts {
			ms[i].ThreadTS = ts
		}
		ms[i].Key = messageKey(ms[i].Channel, ms[i].TS, ms[i].Permalink, ms[i].Text)
		c := classify(ms[i], rules)
		ms[i].Kind, ms[i].Reason, ms[i].Watching, ms[i].Ignored = c.Kind, c.Reason, c.Watching, c.Ignored
		if ms[i].Permalink == "" {
			ms[i].Permalink = buildPermalink(cfg.Workspace, ms[i])
		}
	}
	return t.messageRows(ms), nil
}

func (t *Tool) threadMark(m Message, auto bool) Mark {
	return Mark{Ref: m.ThreadKey(), Label: m.Who() + " in " + m.Where(), URL: m.Permalink, Auto: auto, At: t.now().UTC()}
}

func (t *Tool) watchThread(_ context.Context, _ Config, m Message, _ ochazuke.Arguments) (any, error) {
	mk := t.threadMark(m, false)
	_, err := t.store.Change(func(st *State) error {
		st.Threads[mk.Ref] = mk
		delete(st.IgnoredThreads, mk.Ref)
		delete(st.IgnoredThreads, m.Key)
		delete(st.Unwatched, mk.Ref)
		return nil
	})
	return map[string]any{"thread": mk.Ref, "watching": true}, err
}

func (t *Tool) unwatchThread(_ context.Context, _ Config, m Message, _ ochazuke.Arguments) (any, error) {
	mk := t.threadMark(m, false)
	_, err := t.store.Change(func(st *State) error {
		delete(st.Threads, mk.Ref)
		st.Unwatched[mk.Ref] = mk
		return nil
	})
	return map[string]any{"thread": m.ThreadKey(), "watching": false}, err
}

func (t *Tool) ignoreThread(_ context.Context, _ Config, m Message, _ ochazuke.Arguments) (any, error) {
	mk := t.threadMark(m, false)
	_, err := t.store.Change(func(st *State) error {
		st.IgnoredThreads[mk.Ref] = mk
		delete(st.Threads, mk.Ref)
		delete(st.Unwatched, mk.Ref)
		return nil
	})
	return map[string]any{"thread": mk.Ref, "ignored": true}, err
}

func (t *Tool) unignoreThread(_ context.Context, _ Config, m Message, _ ochazuke.Arguments) (any, error) {
	_, err := t.store.Change(func(st *State) error {
		delete(st.IgnoredThreads, m.ThreadKey())
		delete(st.IgnoredThreads, m.Key)
		return nil
	})
	return map[string]any{"thread": m.ThreadKey(), "ignored": false}, err
}

func (t *Tool) messageAuthor(ctx context.Context, cfg Config, m Message, _ ochazuke.Arguments) (any, error) {
	if m.User == "" {
		return nil, fmt.Errorf("%s: the author is not known (not read yet)", m.Key)
	}
	return t.user(ctx, cfg, m.User)
}

func (t *Tool) messageChannel(ctx context.Context, cfg Config, m Message, _ ochazuke.Arguments) (any, error) {
	return t.findChannel(ctx, cfg, m.Channel)
}

func (t *Tool) sendMessage(ctx context.Context, cfg Config, m Message, args ochazuke.Arguments) (any, error) {
	s := signalOf(m)
	if s.Title == "" || m.Kind == "" {
		s.Kind, s.Title = "sent", "Slack: "+m.Who()+" in "+m.Where()
	}
	if u, err := t.permalink(ctx, cfg, m); err == nil {
		s.URL = u
	}
	s.Why = strings.TrimSpace(ochazuke.StringArg(args, "why", ""))
	if s.Why == "" {
		s.Why = strings.TrimSpace("sent from o-slack: " + clip(m.Text, 80))
	}
	if err := t.inbox(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

func (t *Tool) refresh(ochazuke.Ctx) ochazuke.Result {
	cfg := t.config()
	return t.after(ochazuke.Pending(func(c context.Context) (any, error) {
		p, st, err := t.current(c, cfg, true, time.Time{})
		if err != nil {
			return nil, err
		}
		cs := collect(p, cfg.rules(st, t.now()), cfg.Workspace)
		out := map[string]any{"polled": p.At, "server": p.Server, "messages": len(p.Messages)}
		for _, col := range collections {
			out[col.ID] = len(cs[col.ID])
		}
		if len(p.Errors) > 0 {
			out["errors"] = p.Errors
		}
		if len(p.Notes) > 0 {
			out["notes"] = p.Notes
		}
		if pr := cfg.Oncall.Problems(); len(pr) > 0 {
			out["oncall_problems"] = pr
		}
		return out, nil
	}))
}

// Channels.

func (t *Tool) onChannel(run func(ctx context.Context, cfg Config, ref string, args ochazuke.Arguments) (any, error)) ochazuke.AppHandler {
	return func(ctx ochazuke.Ctx) ochazuke.Result {
		ref, err := refOf(ctx.Instance, "channel (slack-channel/ID, an ID or #name)")
		if err != nil {
			return ochazuke.Failed(err)
		}
		ref = trimHash(strings.TrimPrefix(ref, channelNoun+"/"))
		if ref == "" {
			return ochazuke.Failed(errors.New("name a channel"))
		}
		cfg, args := t.config(), ctx.Args
		return t.after(ochazuke.Pending(func(c context.Context) (any, error) { return run(c, cfg, ref, args) }))
	}
}

func (t *Tool) listChannels(ctx ochazuke.Ctx) ochazuke.Result {
	cfg, fresh := t.config(), boolArg(ctx.Args, "fresh")
	return ochazuke.Pending(func(c context.Context) (any, error) {
		chans, _, err := t.channels(c, cfg, fresh)
		if err != nil {
			// The marks still answer without the server (record first).
			st, serr := t.store.State()
			if serr != nil {
				return nil, err
			}
			chans = mergeChannels(nil, cfg, st, t.now())
		}
		out := make([]ocli.Row, 0, len(chans))
		for _, ch := range chans {
			out = append(out, channelRow(ch))
		}
		return out, nil
	})
}

// knownChannels are the cached channels with the marks, no server.
func (t *Tool) knownChannels(cfg Config) []Channel {
	var c channelCache
	t.store.read(channelsFile, &c)
	st, _ := t.store.State()
	return mergeChannels(c.Channels, cfg, st, t.now())
}

func (t *Tool) findChannel(_ context.Context, cfg Config, ref string) (Channel, error) {
	for _, c := range t.knownChannels(cfg) {
		if sameChannel(ref, c.ID, c.Name) {
			return c, nil
		}
	}
	c := Channel{Name: ref}
	if looksLikeID(ref) {
		c = Channel{ID: ref}
	}
	return c, nil
}

func (t *Tool) showChannel(ctx context.Context, cfg Config, ref string, _ ochazuke.Arguments) (any, error) {
	return t.findChannel(ctx, cfg, ref)
}

func (t *Tool) channelURL(ctx context.Context, cfg Config, ref string) (string, error) {
	c, _ := t.findChannel(ctx, cfg, ref)
	ws := strings.TrimRight(cfg.Workspace, "/")
	if ws == "" || c.ID == "" {
		return "", fmt.Errorf("%s: no link: it needs slack.workspace-url and the channel's ID", ref)
	}
	return ws + "/archives/" + c.ID, nil
}

func (t *Tool) openChannel(ctx context.Context, cfg Config, ref string, _ ochazuke.Arguments) (any, error) {
	u, err := t.channelURL(ctx, cfg, ref)
	if err != nil {
		return nil, err
	}
	return map[string]string{"opened": u}, t.opener(ctx, u)
}

func (t *Tool) copyChannelLink(ctx context.Context, cfg Config, ref string, _ ochazuke.Arguments) (any, error) {
	u, err := t.channelURL(ctx, cfg, ref)
	if err != nil {
		return nil, err
	}
	return map[string]any{"url": u, "copied": t.clip(u) == nil}, nil
}

func (t *Tool) channelHistory(ctx context.Context, cfg Config, ref string, _ ochazuke.Arguments) (any, error) {
	id := channelID(ref, t.knownChannels(cfg))
	s, err := t.connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	ms, err := s.messages(ctx, opHistory, "channel", map[string]any{"channel": id, "oldest": tsOf(t.now().Add(-cfg.Lookback)), "limit": cfg.Limit})
	if err != nil {
		return nil, err
	}
	st, _ := t.store.State()
	rules := cfg.rules(st, t.now())
	for i := range ms {
		if ms[i].Channel == "" {
			ms[i].Channel = id
			ms[i].Key = messageKey(id, ms[i].TS, ms[i].Permalink, ms[i].Text)
		}
		c := classify(ms[i], rules)
		ms[i].Kind, ms[i].Reason, ms[i].Ignored = c.Kind, c.Reason, c.Ignored
		if ms[i].Permalink == "" {
			ms[i].Permalink = buildPermalink(cfg.Workspace, ms[i])
		}
	}
	return t.messageRows(ms), nil
}

func (t *Tool) channelMark(ctx context.Context, cfg Config, ref string) Mark {
	c, _ := t.findChannel(ctx, cfg, ref)
	u, _ := t.channelURL(ctx, cfg, ref)
	r := c.Ref()
	if r == "" {
		r = ref
	}
	return Mark{Ref: r, Label: c.Name, URL: u, At: t.now().UTC()}
}

func (t *Tool) watchChannel(ctx context.Context, cfg Config, ref string, _ ochazuke.Arguments) (any, error) {
	m := t.channelMark(ctx, cfg, ref)
	_, err := t.store.Change(func(st *State) error {
		st.Watched[m.Ref] = m
		delete(st.Ignored, m.Ref)
		return nil
	})
	return map[string]any{"channel": m.Ref, "watched": true}, err
}

func (t *Tool) unwatchChannel(ctx context.Context, cfg Config, ref string, _ ochazuke.Arguments) (any, error) {
	m := t.channelMark(ctx, cfg, ref)
	_, err := t.store.Change(func(st *State) error { delete(st.Watched, m.Ref); delete(st.Watched, ref); return nil })
	return map[string]any{"channel": m.Ref, "watched": false}, err
}

func (t *Tool) ignoreChannel(ctx context.Context, cfg Config, ref string, _ ochazuke.Arguments) (any, error) {
	m := t.channelMark(ctx, cfg, ref)
	_, err := t.store.Change(func(st *State) error {
		st.Ignored[m.Ref] = m
		delete(st.Watched, m.Ref)
		return nil
	})
	return map[string]any{"channel": m.Ref, "ignored": true}, err
}

func (t *Tool) unignoreChannel(ctx context.Context, cfg Config, ref string, _ ochazuke.Arguments) (any, error) {
	m := t.channelMark(ctx, cfg, ref)
	_, err := t.store.Change(func(st *State) error { delete(st.Ignored, m.Ref); delete(st.Ignored, ref); return nil })
	return map[string]any{"channel": m.Ref, "ignored": false}, err
}

func (t *Tool) markOncall(ctx context.Context, cfg Config, ref string, _ ochazuke.Arguments) (any, error) {
	m := t.channelMark(ctx, cfg, ref)
	_, err := t.store.Change(func(st *State) error { st.OnCall[m.Ref] = m; return nil })
	return map[string]any{"channel": m.Ref, "oncall": true, "oncall_active": cfg.Oncall.Active(t.now().Local())}, err
}

func (t *Tool) unmarkOncall(ctx context.Context, cfg Config, ref string, _ ochazuke.Arguments) (any, error) {
	m := t.channelMark(ctx, cfg, ref)
	_, err := t.store.Change(func(st *State) error { delete(st.OnCall, m.Ref); delete(st.OnCall, ref); return nil })
	out := map[string]any{"channel": m.Ref, "oncall": false}
	for _, c := range cfg.Oncall.Channels {
		if sameChannel(c, m.Ref, m.Label) || trimHash(c) == ref {
			out["oncall"] = true
			out["note"] = "still in oncall.channels: edit that setting to drop it"
		}
	}
	return out, err
}

func (t *Tool) sendChannel(ctx context.Context, cfg Config, ref string, args ochazuke.Arguments) (any, error) {
	m := t.channelMark(ctx, cfg, ref)
	name := m.Label
	if name == "" {
		name = m.Ref
	}
	why := strings.TrimSpace(ochazuke.StringArg(args, "why", ""))
	if why == "" {
		why = "sent from o-slack: #" + name
	}
	s := Signal{Kind: "sent", Key: "channel/" + m.Ref, URL: m.URL, Title: "Slack channel #" + name, Why: why, Channel: m.Ref}
	if err := t.inbox(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

func (t *Tool) refreshChannels(ochazuke.Ctx) ochazuke.Result {
	cfg := t.config()
	return t.after(ochazuke.Pending(func(c context.Context) (any, error) {
		chans, notes, err := t.channels(c, cfg, true)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"channels": len(chans)}
		if len(notes) > 0 {
			out["notes"] = notes
		}
		return out, nil
	}))
}

// Users.

// User is a profile as the user operation answers it.
type User struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	RealName string `json:"real_name,omitempty"`
	Title    string `json:"title,omitempty"`
	Email    string `json:"email,omitempty"`
	TZ       string `json:"tz,omitempty"`
}

func (t *Tool) onUser(run func(ctx context.Context, cfg Config, id string) (any, error)) ochazuke.AppHandler {
	return func(ctx ochazuke.Ctx) ochazuke.Result {
		ref, err := refOf(ctx.Instance, "user (slack-user/ID or an ID)")
		if err != nil {
			return ochazuke.Failed(err)
		}
		id := strings.TrimPrefix(strings.TrimPrefix(ref, userNoun+"/"), "@")
		cfg := t.config()
		return ochazuke.Pending(func(c context.Context) (any, error) { return run(c, cfg, id) })
	}
}

func (t *Tool) user(ctx context.Context, cfg Config, id string) (User, error) {
	s, err := t.connect(ctx, cfg)
	if err != nil {
		return User{}, err
	}
	defer s.Close()
	recs, err := s.read(ctx, opUser, map[string]any{"user": id})
	if err != nil {
		return User{}, err
	}
	if len(recs) == 0 {
		return User{}, fmt.Errorf("user %s: the server answered no profile", id)
	}
	r := recs[0]
	u := User{ID: r["id"], Name: r["name"], RealName: r["real_name"], Title: r["title"], Email: r["email"], TZ: r["tz"]}
	if u.ID == "" {
		u.ID = id
	}
	return u, nil
}

func (t *Tool) showUser(ctx context.Context, cfg Config, id string) (any, error) {
	return t.user(ctx, cfg, id)
}

func (t *Tool) openUser(ctx context.Context, cfg Config, id string) (any, error) {
	ws := strings.TrimRight(cfg.Workspace, "/")
	if ws == "" {
		return nil, errors.New("opening a profile needs slack.workspace-url")
	}
	u := ws + "/team/" + id
	return map[string]string{"opened": u}, t.opener(ctx, u)
}

func (t *Tool) copyUserID(_ context.Context, _ Config, id string) (any, error) {
	return map[string]any{"id": id, "copied": t.clip(id) == nil}, nil
}

// Signals.

func (t *Tool) signalVerb(dry bool) ochazuke.AppHandler {
	return func(ochazuke.Ctx) ochazuke.Result {
		cfg := t.config()
		return ochazuke.Pending(func(c context.Context) (any, error) { return t.runSignals(c, cfg, dry) })
	}
}

// The mapping.

// serverTools are the tools map check compares against: a --schemas file,
// else (offline) o-mcp's saved snapshot of the server, else the live
// server's tools/list.
func (t *Tool) serverTools(ctx context.Context, cfg Config, offline bool, file string) ([]*mcp.Tool, string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, "", err
		}
		tools, err := decodeTools(b)
		return tools, "file " + file, err
	}
	srv, err := t.resolve(cfg.Server, cfg.Fallbacks)
	if err != nil {
		return nil, "", err
	}
	if offline {
		snap, err := t.schemas().Load(srv.ID)
		if err != nil {
			return nil, "", fmt.Errorf("%s: no saved schemas (run o-mcp discover %s on this machine): %w", srv.ID, srv.Name, err)
		}
		src := "o-mcp's saved schemas of " + srv.ID + " (" + snap.DiscoveredAt.Format(time.RFC3339) + ")"
		if snap.Fingerprint != "" && snap.Fingerprint != srv.Fingerprint() {
			src += "; stale: the server's configuration changed since (o-mcp discover " + srv.Name + ")"
		}
		return snap.Tools, src, nil
	}
	conn, err := t.dial(ctx, srv)
	if err != nil {
		return nil, "", err
	}
	defer conn.Close()
	tools, err := conn.Tools(ctx)
	return tools, "live tools/list of " + srv.ID, err
}

// decodeTools reads an o-mcp snapshot ({"tools": [...]}), a tools/list
// answer ({"tools": [...]}) or a bare list of tools.
func decodeTools(b []byte) ([]*mcp.Tool, error) {
	var snap struct {
		Tools []*mcp.Tool `json:"tools"`
	}
	if err := json.Unmarshal(b, &snap); err == nil && snap.Tools != nil {
		return snap.Tools, nil
	}
	var list []*mcp.Tool
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("not an o-mcp snapshot, a tools/list answer or a list of tools: %w", err)
	}
	return list, nil
}

func (t *Tool) checkMap(ctx ochazuke.Ctx) ochazuke.Result {
	cfg, offline, file := t.config(), boolArg(ctx.Args, "offline"), ochazuke.StringArg(ctx.Args, "schemas", "")
	return ochazuke.Pending(func(c context.Context) (any, error) {
		tools, src, err := t.serverTools(c, cfg, offline, file)
		if err != nil {
			return nil, err
		}
		checks, problems := checkMapping(cfg.Map, tools)
		return checkRows(checks, problems, src, len(tools)), nil
	})
}

func (t *Tool) showMap(ctx ochazuke.Ctx) ochazuke.Result {
	cfg, offline, file := t.config(), boolArg(ctx.Args, "offline"), ochazuke.StringArg(ctx.Args, "schemas", "")
	return ochazuke.Pending(func(c context.Context) (any, error) {
		tools, src, err := t.serverTools(c, cfg, offline, file)
		if err != nil {
			// Without a server the mapping is the settings and the first candidates.
			tools, src = nil, "no server ("+err.Error()+"): first candidates, unverified"
		}
		m := resolveAll(cfg.Map, tools)
		out := []ocli.Row{}
		for _, op := range ops {
			om := m.Ops[op.Name]
			var args []string
			for _, a := range om.Args {
				args = append(args, a.String())
			}
			out = append(out, ocli.Row{Noun: mapNoun, ID: mapNoun + "/" + op.Name, Label: op.Name + " " + om.Tool,
				Data:  map[string]any{"op": op.Name, "tool": om.Tool, "from": om.From, "note": om.Note, "args": args, "results": om.Results, "source": src},
				Cells: []string{op.Name, om.Tool, om.From, strings.Join(args, " ")}})
		}
		return out, nil
	})
}

func (t *Tool) mapCandidates(ochazuke.Ctx) ochazuke.Result {
	out := []ocli.Row{}
	for _, op := range ops {
		for i, c := range candidates[op.Name] {
			out = append(out, ocli.Row{Noun: mapNoun, ID: fmt.Sprintf("%s/%s/%d", mapNoun, op.Name, i+1), Label: op.Name + " " + c.Tool,
				Data:  map[string]any{"op": op.Name, "rank": i + 1, "tool": c.Tool, "args": c.Args, "server": c.Server},
				Cells: []string{op.Name, c.Tool, fmt.Sprint(i + 1), strings.Join(c.Args, " ") + "  (" + c.Server + ")"}})
		}
	}
	return ochazuke.Done(out)
}
