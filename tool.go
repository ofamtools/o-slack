package main

import (
	"context"
	"io"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/unixsuperhero/o-mcp/client"
	"github.com/unixsuperhero/ochazuke"
)

// Tool is o-slack: its runtime (nouns, verbs, settings, the Slack page),
// the server it reaches through o-mcp's client, the inbox seam and the
// data directory.
type Tool struct {
	r       *ochazuke.Runtime
	targets *ochazuke.List // the shell's --target rows (nil on the page)
	store   Store
	resolve Resolver
	dial    Dialer
	schemas func() client.Store // o-mcp's saved schemas (map check --offline)
	inbox   Inbox
	now     func() time.Time
	opener  func(ctx context.Context, url string) error
	clip    func(text string) error
	changed func()

	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

// Config is the settings, read on the loop so work off it reads no
// runtime state.
type Config struct {
	Server      string
	Fallbacks   []string
	Map         MapSettings
	Workspace   string
	Who         Who
	WatchDMs    bool
	AutoWatch   bool
	ThreadAge   time.Duration
	Mentions    string
	DMs         string
	Limit       int
	IgnoreUsers []string
	Oncall      Oncall
	MaxAge      time.Duration
	Lookback    time.Duration
	ChannelsAge time.Duration
}

func (t *Tool) config() Config {
	s := t.r.Settings()
	ms := MapSettings{Tools: map[string]string{}, Args: map[string][]string{}, Results: map[string][]string{}}
	for _, op := range ops {
		ms.Tools[op.Name] = s.String(setTools + op.Name)
		ms.Args[op.Name] = s.Strings(setArgs + op.Name)
		ms.Results[op.Name] = s.Strings(setResults + op.Name)
	}
	return Config{
		Server: s.String(setServer), Fallbacks: s.Strings(setServerFallbacks), Map: ms,
		Workspace: s.String(setWorkspace), Who: Who{Me: s.String(setMe), MeID: s.String(setMeID)},
		WatchDMs: s.Bool(setWatchDMs), AutoWatch: s.Bool(setAutoWatch), ThreadAge: s.Duration(setThreadAge),
		Mentions: s.String(setQueryMentions), DMs: s.String(setQueryDMs), Limit: s.Int(setLimit),
		IgnoreUsers: s.Strings(setIgnoreUsers),
		Oncall: Oncall{Mode: s.String(setOncallMode), Start: s.String(setOncallStart), Weeks: s.Int(setOncallWeeks),
			Channels: s.Strings(setOncallChannels), Tag: s.String(setOncallTag)},
		MaxAge: s.Duration(setMaxAge), Lookback: s.Duration(setLookback), ChannelsAge: s.Duration(setChannelsAge),
	}
}

// rules is what classification reads from the settings and the marks.
func (cfg Config) rules(st State, now time.Time) Rules {
	r := Rules{Who: cfg.Who, WatchDMs: cfg.WatchDMs, OncallActive: cfg.Oncall.Active(now.Local()), OncallTag: cfg.Oncall.Tag,
		IgnoreUsers: cfg.IgnoreUsers, Threads: map[string]bool{}, IgnThreads: map[string]bool{}}
	r.OncallChans = append(slices.Clone(cfg.Oncall.Channels), keys(st.OnCall)...)
	r.Watched, r.Ignored = keys(st.Watched), keys(st.Ignored)
	for k := range st.Threads {
		r.Threads[k] = true
	}
	for k := range st.IgnoredThreads {
		r.IgnThreads[k] = true
	}
	return r
}

// plan is what a poll reads now.
func (cfg Config) plan(st State, chans []Channel, since, now time.Time) (PollPlan, []string) {
	var notes []string
	p := PollPlan{Limit: cfg.Limit, Since: since}
	var n string
	if p.Mentions, n = expandQuery(cfg.Mentions, cfg.Who, since); n != "" {
		notes = append(notes, n)
	}
	if cfg.WatchDMs {
		if p.DMs, n = expandQuery(cfg.DMs, cfg.Who, since); n != "" {
			notes = append(notes, n)
		}
	}
	p.Threads = keys(st.Threads)
	var refs []string
	if cfg.Oncall.Active(now.Local()) {
		refs = append(refs, cfg.Oncall.Channels...)
		refs = append(refs, keys(st.OnCall)...)
	}
	refs = append(refs, keys(st.Watched)...)
	seen := map[string]bool{}
	for _, ref := range refs {
		id := channelID(ref, chans)
		if !seen[id] {
			seen[id] = true
			p.Channels = append(p.Channels, id)
		}
	}
	return p, notes
}

// channelID is a channel ref as an ID when the channels read knows it.
func channelID(ref string, chans []Channel) string {
	for _, c := range chans {
		if c.ID != "" && sameChannel(ref, c.ID, c.Name) {
			return c.ID
		}
	}
	if len(ref) > 0 && ref[0] == '#' {
		return ref[1:]
	}
	return ref
}

// session is an open connection with its mapping resolved against the
// tools the server lists.
type session struct {
	caller
	server client.Server
	tools  []*mcp.Tool
}

// connect resolves slack.server, connects and lists its tools.
func (t *Tool) connect(ctx context.Context, cfg Config) (*session, error) {
	srv, err := t.resolve(cfg.Server, cfg.Fallbacks)
	if err != nil {
		return nil, err
	}
	conn, err := t.dial(ctx, srv)
	if err != nil {
		return nil, err
	}
	tools, err := conn.Tools(ctx)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &session{caller: caller{conn: conn, mapping: resolveAll(cfg.Map, tools)}, server: srv, tools: tools}, nil
}

func (s *session) Close() error { return s.conn.Close() }

// cachedPoll is poll.json: the poll and the plan that made it.
type cachedPoll struct {
	Plan PollPlan `json:"plan"`
	Poll Poll     `json:"poll"`
}

func samePlan(a, b PollPlan) bool {
	return a.Mentions == b.Mentions && a.DMs == b.DMs && a.Limit == b.Limit && slices.Equal(a.Threads, b.Threads) && slices.Equal(a.Channels, b.Channels)
}

// current is the state and a poll answering it: the cached poll while it
// is younger than poll.max-age and read what the plan reads; else a new
// poll from since (zero: poll.lookback ago), saved. fresh always polls.
// The poll runs holding the lock, so lists loading at once poll once.
func (t *Tool) current(ctx context.Context, cfg Config, fresh bool, since time.Time) (Poll, State, error) {
	var p Poll
	var st State
	err := t.store.locked(func() error {
		var err error
		if st, err = t.store.State(); err != nil {
			return err
		}
		now := t.now()
		if since.IsZero() {
			since = now.Add(-cfg.Lookback)
		}
		var chans channelCache
		t.store.read(channelsFile, &chans)
		plan, notes := cfg.plan(st, chans.Channels, since, now)
		if !fresh {
			var c cachedPoll
			if ok, _ := t.store.read(pollFile, &c); ok && now.Sub(c.Poll.At) < cfg.MaxAge && samePlan(c.Plan, plan) {
				p = c.Poll
				return nil
			}
		}
		s, err := t.connect(ctx, cfg)
		if err != nil {
			return err
		}
		defer s.Close()
		// A channel named (#name) that the channels read does not know yet:
		// read the channels now, so its history is read by ID.
		if unresolved(plan.Channels, chans.Channels) {
			if recs, rerr := s.read(ctx, opChannels, map[string]any{"limit": cfg.Limit}); rerr != nil {
				notes = append(notes, "channels: "+rerr.Error())
			} else {
				chans = channelCacheOf(recs, now)
				if err := t.store.write(channelsFile, chans); err != nil {
					return err
				}
				plan, notes = cfg.plan(st, chans.Channels, since, now)
			}
		}
		p = s.poll(ctx, plan, s.server.ID, now)
		p.Notes = append(p.Notes, notes...)
		nameChannels(p.Messages, chans.Channels)
		if st, err = t.autoWatch(cfg, st, p, now); err != nil {
			return err
		}
		return t.store.write(pollFile, cachedPoll{Plan: plan, Poll: p})
	})
	return p, st, err
}

// autoWatch watches the thread of each message that mentions you (slack.
// auto-watch-threads), so its replies become signals, and drops auto
// watches older than slack.thread-watch-age. A thread you ignored or
// unwatched is not watched again.
func (t *Tool) autoWatch(cfg Config, st State, p Poll, now time.Time) (State, error) {
	if !cfg.AutoWatch {
		return st, nil
	}
	rules := cfg.rules(st, now)
	var add []Mark
	for _, m := range p.Messages {
		c := classify(m, rules)
		if c.Kind != KindMention || c.Ignored || c.Watching || st.IgnoredThreads[c.ThreadKey()].Ref != "" || st.Unwatched[c.ThreadKey()].Ref != "" {
			continue
		}
		add = append(add, Mark{Ref: c.ThreadKey(), Label: c.Who() + " in " + c.Where(), URL: c.Permalink, Auto: true, At: now.UTC()})
	}
	expired := false
	for _, m := range st.Threads {
		if m.Auto && cfg.ThreadAge > 0 && now.Sub(m.At) > cfg.ThreadAge {
			expired = true
		}
	}
	if len(add) == 0 && !expired {
		return st, nil
	}
	st.init()
	for _, m := range add {
		st.Threads[m.Ref] = m
	}
	for k, m := range st.Threads {
		if m.Auto && cfg.ThreadAge > 0 && now.Sub(m.At) > cfg.ThreadAge {
			delete(st.Threads, k)
		}
	}
	return st, t.store.write(stateFile, st)
}

// channelCache is channels.json.
type channelCache struct {
	At       time.Time `json:"at"`
	Channels []Channel `json:"channels"`
}

// channels is the channels list: the channels operation's (cached for
// channels.max-age) with the on-call setting and the marks; a channel only
// named in a setting or a mark is a row too (record first).
func (t *Tool) channels(ctx context.Context, cfg Config, fresh bool) ([]Channel, []string, error) {
	var notes []string
	var c channelCache
	t.store.read(channelsFile, &c)
	if fresh || t.now().Sub(c.At) >= cfg.ChannelsAge {
		s, err := t.connect(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		recs, rerr := s.read(ctx, opChannels, map[string]any{"limit": cfg.Limit})
		s.Close()
		if rerr != nil {
			notes = append(notes, rerr.Error())
		} else {
			c = channelCacheOf(recs, t.now())
			if err := t.store.locked(func() error { return t.store.write(channelsFile, c) }); err != nil {
				return nil, nil, err
			}
		}
	}
	st, err := t.store.State()
	if err != nil {
		return nil, nil, err
	}
	return mergeChannels(c.Channels, cfg, st, t.now()), notes, nil
}

// channelCacheOf is a channels read as channels.json (DMs dropped).
func channelCacheOf(recs []map[string]string, now time.Time) channelCache {
	c := channelCache{At: now.UTC(), Channels: []Channel{}}
	for _, r := range recs {
		if ch := channelFrom(r); ch.Ref() != "" && r["is_im"] != "true" {
			c.Channels = append(c.Channels, ch)
		}
	}
	return c
}

// unresolved reports a channel ref that is a name no read channel has.
func unresolved(refs []string, chans []Channel) bool {
	for _, ref := range refs {
		if !looksLikeID(ref) && channelID(ref, chans) == trimHash(ref) {
			return true
		}
	}
	return false
}

// nameChannels fills each message's channel name from the channels read.
func nameChannels(ms []Message, chans []Channel) {
	for i := range ms {
		if ms[i].ChannelName != "" {
			continue
		}
		for _, c := range chans {
			if c.ID != "" && c.ID == ms[i].Channel {
				ms[i].ChannelName = c.Name
			}
		}
	}
}

// mergeChannels marks the read channels and adds the ones only named.
func mergeChannels(read []Channel, cfg Config, st State, now time.Time) []Channel {
	out := slices.Clone(read)
	find := func(ref string) int {
		for i, c := range out {
			if sameChannel(ref, c.ID, c.Name) {
				return i
			}
		}
		name, id := "", ""
		if len(ref) > 0 && ref[0] == '#' || !looksLikeID(ref) {
			name = trimHash(ref)
		} else {
			id = ref
		}
		out = append(out, Channel{ID: id, Name: name})
		return len(out) - 1
	}
	active := cfg.Oncall.Active(now.Local())
	for _, ref := range cfg.Oncall.Channels {
		i := find(ref)
		out[i].OnCall, out[i].OnCallBy, out[i].Active = true, "setting", active
	}
	for _, ref := range keys(st.OnCall) {
		i := find(ref)
		if !out[i].OnCall {
			out[i].OnCall, out[i].OnCallBy = true, "marked"
		}
		out[i].Active = active
	}
	for _, ref := range keys(st.Watched) {
		out[find(ref)].Watched = true
	}
	for _, ref := range keys(st.Ignored) {
		out[find(ref)].Ignored = true
	}
	slices.SortStableFunc(out, func(a, b Channel) int {
		switch {
		case a.OnCall != b.OnCall:
			if a.OnCall {
				return -1
			}
			return 1
		case a.Watched != b.Watched:
			if a.Watched {
				return -1
			}
			return 1
		}
		if a.Name+a.ID < b.Name+b.ID {
			return -1
		}
		return 1
	})
	return out
}

func trimHash(s string) string {
	if len(s) > 0 && s[0] == '#' {
		return s[1:]
	}
	return s
}

// looksLikeID reports a Slack ID: C, G or D then upper-case letters and
// digits.
func looksLikeID(s string) bool {
	if len(s) < 3 || (s[0] != 'C' && s[0] != 'G' && s[0] != 'D') {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// newTool is a Tool on the real o-mcp client, inbox, opener and clipboard.
func newTool(stdin io.Reader, stdout, stderr io.Writer) *Tool {
	return &Tool{store: Store{Dir: dataDir()}, resolve: configuredServer, dial: dialMCP, schemas: mcpStore, inbox: mtmInbox,
		now: time.Now, opener: openURL, clip: copyToClipboard, Stdin: stdin, Stdout: stdout, Stderr: stderr}
}

// sub is a fresh Tool sharing t's fakes and data directory.
func (t *Tool) sub(stdin io.Reader, stdout, stderr io.Writer) *Tool {
	return &Tool{store: t.store, resolve: t.resolve, dial: t.dial, schemas: t.schemas, inbox: t.inbox, now: t.now,
		opener: t.opener, clip: t.clip, Stdin: stdin, Stdout: stdout, Stderr: stderr}
}
