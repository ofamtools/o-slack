package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Message kinds (matomeru-old's internal/slack, plus channel: activity in
// a channel you watch).
const (
	KindMention = "mention"
	KindDM      = "dm"
	KindThread  = "thread"
	KindOncall  = "oncall"
	KindChannel = "channel"
)

// Message is one Slack message as o-slack reads it from any operation's
// result. Key (CHANNEL/TS) is its stable id: the row id's tail and the
// inbox item's --key.
type Message struct {
	Key         string   `json:"key"`
	Channel     string   `json:"channel"`
	ChannelName string   `json:"channel_name,omitempty"`
	TS          string   `json:"ts"`
	ThreadTS    string   `json:"thread_ts,omitempty"`
	User        string   `json:"user,omitempty"`
	UserName    string   `json:"user_name,omitempty"`
	Text        string   `json:"text"`
	Permalink   string   `json:"permalink,omitempty"`
	DM          bool     `json:"dm,omitempty"`
	Via         []string `json:"via,omitempty"` // mentions, dms, thread, channel: which reads found it
	Kind        string   `json:"kind,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	Ignored     bool     `json:"ignored,omitempty"`
	IgnoredBy   string   `json:"ignored_by,omitempty"`
	Watching    bool     `json:"watching,omitempty"` // its thread is watched
	Stale       bool     `json:"stale,omitempty"`    // recorded, not in the last poll
	At          string   `json:"at,omitempty"`       // the ts as RFC 3339
}

// ThreadKey is the key of the thread a message is in (or starts).
func (m Message) ThreadKey() string {
	if m.ThreadTS != "" {
		return m.Channel + "/" + m.ThreadTS
	}
	return m.Key
}

// Reply reports whether the message is a reply in a thread.
func (m Message) Reply() bool { return m.ThreadTS != "" && m.ThreadTS != m.TS }

// Where names the message's channel for a person.
func (m Message) Where() string {
	if m.DM {
		return "a DM"
	}
	if m.ChannelName != "" && m.ChannelName != m.Channel {
		return "#" + strings.TrimPrefix(m.ChannelName, "#")
	}
	return m.Channel
}

// Who names the author.
func (m Message) Who() string {
	if m.UserName != "" {
		return m.UserName
	}
	if m.User != "" {
		return m.User
	}
	return "someone"
}

// messageKey is CHANNEL/TS, else a hash of what is known.
func messageKey(channel, ts, permalink, text string) string {
	if channel != "" && ts != "" {
		return channel + "/" + ts
	}
	sum := sha256.Sum256([]byte(permalink + "\x00" + text))
	return "h/" + hex.EncodeToString(sum[:8])
}

// parseMessageRef reads a message reference: a row id
// (slack-message/CHANNEL/TS), a key (CHANNEL/TS) or a permalink.
func parseMessageRef(ref string) (channel, ts, threadTS string, err error) {
	ref = strings.TrimSpace(ref)
	if m := permalinkRE.FindStringSubmatch(ref); m != nil {
		return m[1], m[2] + "." + m[3], m[4], nil
	}
	ref = strings.TrimPrefix(ref, messageNoun+"/")
	ch, t, ok := strings.Cut(ref, "/")
	if !ok || ch == "" || t == "" || strings.Contains(t, "/") {
		return "", "", "", fmt.Errorf("%q: not a message (slack-message/CHANNEL/TS, CHANNEL/TS or a permalink)", ref)
	}
	if _, err := strconv.ParseFloat(t, 64); err != nil {
		return "", "", "", fmt.Errorf("%q: %q is not a Slack ts", ref, t)
	}
	return ch, t, "", nil
}

// tsTime is a Slack ts as a time.
func tsTime(ts string) (time.Time, bool) {
	sec, frac, _ := strings.Cut(ts, ".")
	s, err := strconv.ParseInt(sec, 10, 64)
	if err != nil || s <= 0 {
		return time.Time{}, false
	}
	var ns int64
	if frac != "" {
		frac = (frac + "000000000")[:9]
		ns, _ = strconv.ParseInt(frac, 10, 64)
	}
	return time.Unix(s, ns).UTC(), true
}

// tsOf is a time as a Slack ts.
func tsOf(t time.Time) string {
	return fmt.Sprintf("%d.%06d", t.Unix(), t.Nanosecond()/1000)
}

// buildPermalink is a message's permalink from the workspace URL
// (https://NAME.slack.com): a reply links into its thread.
func buildPermalink(workspace string, m Message) string {
	workspace = strings.TrimRight(strings.TrimSpace(workspace), "/")
	if workspace == "" || m.Channel == "" || m.TS == "" || !strings.Contains(m.TS, ".") {
		return ""
	}
	u := workspace + "/archives/" + m.Channel + "/p" + strings.Replace(m.TS, ".", "", 1)
	if m.Reply() {
		u += "?thread_ts=" + m.ThreadTS + "&cid=" + m.Channel
	}
	return u
}

// messageFrom is a result record as a Message.
func messageFrom(rec map[string]string, via string) Message {
	m := Message{
		Channel: rec["channel"], ChannelName: rec["channel_name"], TS: rec["ts"], ThreadTS: rec["thread_ts"],
		User: rec["user"], UserName: rec["user_name"], Text: rec["text"], Permalink: rec["permalink"],
	}
	if m.Channel == "" || m.TS == "" {
		if p := permalinkRE.FindStringSubmatch(m.Permalink); p != nil {
			if m.Channel == "" {
				m.Channel = p[1]
			}
			if m.TS == "" {
				m.TS = p[2] + "." + p[3]
			}
			if m.ThreadTS == "" {
				m.ThreadTS = p[4]
			}
		}
	}
	if m.UserName == m.User {
		m.UserName = ""
	}
	if m.ChannelName == m.Channel {
		m.ChannelName = ""
	}
	m.DM = rec["is_im"] == "true" || isDMChannel(m.Channel, m.ChannelName)
	m.Key = messageKey(m.Channel, m.TS, m.Permalink, m.Text)
	if via != "" {
		m.Via = []string{via}
	}
	if t, ok := tsTime(m.TS); ok {
		m.At = t.Format(time.RFC3339)
	}
	return m
}

// isDMChannel reports a direct message channel: an ID starting D, a name
// starting @ or mpdm-, or Slack's "im" names.
func isDMChannel(id, name string) bool {
	return strings.HasPrefix(id, "D") && len(id) > 1 && strings.ToUpper(id) == id ||
		strings.HasPrefix(name, "@") || strings.HasPrefix(name, "mpdm-") || name == "im" || name == "directmessage"
}

// Channel is one Slack channel: what the channels operation answered, the
// on-call setting and o-slack's own marks.
type Channel struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Private  bool   `json:"private,omitempty"`
	Topic    string `json:"topic,omitempty"`
	Members  int    `json:"members,omitempty"`
	OnCall   bool   `json:"oncall"`
	OnCallBy string `json:"oncall_by,omitempty"` // setting (oncall.channels) or marked
	Active   bool   `json:"oncall_active"`       // on-call and the window is open
	Watched  bool   `json:"watched"`
	Ignored  bool   `json:"ignored"`
	Read     bool   `json:"read"` // the channels operation listed it
}

// Ref is how the channel is named in settings and marks: its ID, else
// its name.
func (c Channel) Ref() string {
	if c.ID != "" {
		return c.ID
	}
	return c.Name
}

// channelFrom is a result record as a Channel.
func channelFrom(rec map[string]string) Channel {
	c := Channel{ID: rec["id"], Name: strings.TrimPrefix(rec["name"], "#"), Topic: rec["topic"], Private: rec["private"] == "true", Read: true}
	c.Members, _ = strconv.Atoi(rec["members"])
	return c
}

// sameChannel reports whether ref (an ID, a name, #name) names the
// channel of id and name.
func sameChannel(ref, id, name string) bool {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "#")
	if ref == "" {
		return false
	}
	return ref == id || name != "" && strings.EqualFold(ref, strings.TrimPrefix(name, "#"))
}

// On-call (matomeru-old's internal/oncall): the week(s) you are on call,
// the channels to watch then and the tag that marks on-call mentions.
const (
	OncallAuto = "auto"
	OncallOn   = "on"
	OncallOff  = "off"
)

// Oncall is the on-call window. Start is the Monday the window starts
// (YYYY-MM-DD, local time), Weeks how many weeks from there; Mode on or
// off overrides the dates.
type Oncall struct {
	Mode     string   `json:"mode"`
	Start    string   `json:"start,omitempty"`
	Weeks    int      `json:"weeks"`
	Channels []string `json:"channels"`
	Tag      string   `json:"tag,omitempty"`
}

// Window is the window's start and end; false when no date is set.
func (o Oncall) Window(loc *time.Location) (time.Time, time.Time, bool) {
	if o.Start == "" {
		return time.Time{}, time.Time{}, false
	}
	start, err := time.ParseInLocation("2006-01-02", o.Start, loc)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	weeks := o.Weeks
	if weeks <= 0 {
		weeks = 1
	}
	return start, start.AddDate(0, 0, 7*weeks), true
}

// Active reports whether you are on call at now.
func (o Oncall) Active(now time.Time) bool {
	switch o.Mode {
	case OncallOn:
		return true
	case OncallOff:
		return false
	}
	start, end, ok := o.Window(now.Location())
	return ok && !now.Before(start) && now.Before(end)
}

// Problems are the on-call settings that cannot be used.
func (o Oncall) Problems() []string {
	var out []string
	switch o.Mode {
	case OncallAuto, OncallOn, OncallOff, "":
	default:
		out = append(out, `oncall.mode must be "auto", "on" or "off"`)
	}
	if o.Start != "" {
		d, err := time.ParseInLocation("2006-01-02", o.Start, time.Local)
		switch {
		case err != nil:
			out = append(out, "oncall.start must be a date like 2026-10-19")
		case d.Weekday() != time.Monday:
			out = append(out, "oncall.start: the on-call week starts on a Monday")
		}
	}
	if o.Weeks < 0 || o.Weeks > 52 {
		out = append(out, "oncall.weeks must be between 1 and 52")
	}
	return out
}

// Who is you, as classification needs: your handle and user ID.
type Who struct {
	Me   string // handle, without @
	MeID string // U...
}

func (w Who) is(user, name string) bool {
	me := strings.ToLower(strings.TrimPrefix(w.Me, "@"))
	return w.MeID != "" && user == w.MeID || me != "" && (strings.EqualFold(strings.TrimPrefix(user, "@"), me) || strings.EqualFold(strings.TrimPrefix(name, "@"), me))
}

// Rules is what classification reads: you, the on-call window and every
// mark.
type Rules struct {
	Who          Who
	WatchDMs     bool
	OncallActive bool
	OncallTag    string
	OncallChans  []string // refs (setting and marked)
	Watched      []string // channel refs
	Ignored      []string // channel refs
	IgnoreUsers  []string // user IDs or names (bots)
	Threads      map[string]bool
	IgnThreads   map[string]bool
}

func (r Rules) inAny(refs []string, m Message) bool {
	for _, ref := range refs {
		if sameChannel(ref, m.Channel, m.ChannelName) {
			return true
		}
	}
	return false
}

// classify is matomeru-old's fixed assessment, adapted: what a message is
// to you and why, "" for nothing. Your own messages are nothing. An
// ignored user, channel or thread makes the message Ignored (still
// classified, so the Ignored list shows what it would have been).
func classify(m Message, r Rules) Message {
	m.Kind, m.Reason, m.Ignored, m.IgnoredBy = "", "", false, ""
	m.Watching = r.Threads[m.ThreadKey()]
	if r.Who.is(m.User, m.UserName) {
		return m
	}
	lower := strings.ToLower(m.Text)
	me := strings.ToLower(strings.TrimPrefix(r.Who.Me, "@"))
	switch {
	case r.OncallActive && r.inAny(r.OncallChans, m):
		m.Kind, m.Reason = KindOncall, "message in on-call channel "+m.Where()
	case r.OncallActive && r.OncallTag != "" && strings.Contains(lower, strings.ToLower(r.OncallTag)):
		m.Kind, m.Reason = KindOncall, "mentions "+r.OncallTag
	case m.DM && r.WatchDMs:
		m.Kind, m.Reason = KindDM, "direct message from "+m.Who()
	case me != "" && strings.Contains(lower, "@"+me) || r.Who.MeID != "" && strings.Contains(m.Text, "<@"+r.Who.MeID+">"):
		m.Kind, m.Reason = KindMention, "mentions you in "+m.Where()
	case m.Reply() && r.Threads[m.ThreadKey()]:
		m.Kind, m.Reason = KindThread, "reply in a thread you watch in "+m.Where()
	case contains(m.Via, "mentions"):
		// The server's search matched you where the text spells you otherwise.
		m.Kind, m.Reason = KindMention, "matched your mentions search in "+m.Where()
	case contains(m.Via, "dms") && r.WatchDMs:
		m.Kind, m.Reason = KindDM, "direct message from "+m.Who()
	case r.inAny(r.Watched, m):
		m.Kind, m.Reason = KindChannel, "message in watched channel "+m.Where()
	}
	if m.Kind == "" {
		return m
	}
	switch {
	case r.IgnThreads[m.ThreadKey()] || r.IgnThreads[m.Key]:
		m.Ignored, m.IgnoredBy = true, "thread"
	case r.inAny(r.Ignored, m) && m.Kind != KindOncall && m.Kind != KindDM:
		m.Ignored, m.IgnoredBy = true, "channel"
	default:
		for _, u := range r.IgnoreUsers {
			if u != "" && (u == m.User || strings.EqualFold(strings.TrimPrefix(u, "@"), m.UserName)) {
				m.Ignored, m.IgnoredBy = true, "user "+u
			}
		}
	}
	return m
}

// Poll is one read of Slack: the messages every read found (deduplicated
// by key, Via merged), what failed, and when.
type Poll struct {
	At       time.Time `json:"at"`
	Server   string    `json:"server"`
	Since    time.Time `json:"since"`
	Messages []Message `json:"messages"`
	Errors   []string  `json:"errors,omitempty"`
	Notes    []string  `json:"notes,omitempty"`
}

// PollPlan is what a poll reads.
type PollPlan struct {
	Mentions string   // the mentions query, expanded
	DMs      string   // the DMs query, expanded
	Threads  []string // watched thread keys (CHANNEL/TS)
	Channels []string // channel refs whose history to read
	Limit    int
	Since    time.Time
}

// expandQuery fills {me}, {me_id} and {since} (YYYY-MM-DD, the day before
// since: Slack's after: is exclusive and by date). A query naming a
// placeholder that has no value is "" with the reason.
func expandQuery(q string, who Who, since time.Time) (string, string) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", ""
	}
	if strings.Contains(q, "{me}") && who.Me == "" {
		return "", "set slack.me (your handle) for " + q
	}
	if strings.Contains(q, "{me_id}") && who.MeID == "" {
		return "", "set slack.me-id (your user ID, U...) for " + q
	}
	r := strings.NewReplacer("{me}", strings.TrimPrefix(who.Me, "@"), "{me_id}", who.MeID, "{since}", since.Local().AddDate(0, 0, -1).Format("2006-01-02"))
	return r.Replace(q), ""
}

// caller is one open connection's call: an operation by its mapping.
type caller struct {
	conn    Conn
	mapping Mapping
}

// read calls an operation and answers its records (prose read for
// permalinks when the shape is messages).
func (c caller) read(ctx context.Context, op string, values map[string]any) ([]map[string]string, error) {
	om := c.mapping.Ops[op]
	args, err := om.build(values)
	if err != nil {
		return nil, err
	}
	res, err := c.conn.Call(ctx, om.Tool, args)
	if err != nil {
		return nil, err
	}
	recs, text, err := om.records(res)
	if errors.Is(err, ErrProse) {
		spec, _ := opByName(op)
		if spec.Shape == "messages" {
			if p := proseRecords(text); len(p) > 0 {
				return p, nil
			}
		}
		return nil, fmt.Errorf("%s: %s answered prose with no permalinks; o-slack reads structuredContent or one JSON text block (map its fields in slack.results.%s)", op, om.Tool, op)
	}
	return recs, err
}

// messages reads an operation's messages.
func (c caller) messages(ctx context.Context, op, via string, values map[string]any) ([]Message, error) {
	recs, err := c.read(ctx, op, values)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(recs))
	for _, r := range recs {
		m := messageFrom(r, via)
		if m.TS == "" && m.Text == "" {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// poll runs a plan: each read on its own, a failed read an error of the
// poll (the rest still read).
func (c caller) poll(ctx context.Context, plan PollPlan, server string, now time.Time) Poll {
	p := Poll{At: now.UTC(), Server: server, Since: plan.Since.UTC()}
	byKey := map[string]int{}
	add := func(ms []Message) {
		for _, m := range ms {
			if i, ok := byKey[m.Key]; ok {
				old := &p.Messages[i]
				for _, v := range m.Via {
					if !contains(old.Via, v) {
						old.Via = append(old.Via, v)
					}
				}
				fill(old, m)
				continue
			}
			byKey[m.Key] = len(p.Messages)
			p.Messages = append(p.Messages, m)
		}
	}
	for _, q := range []struct{ via, query string }{{"mentions", plan.Mentions}, {"dms", plan.DMs}} {
		if q.query == "" {
			continue
		}
		ms, err := c.messages(ctx, opSearch, q.via, map[string]any{"query": q.query, "limit": plan.Limit})
		if err != nil {
			p.Errors = append(p.Errors, q.via+": "+err.Error())
			continue
		}
		add(ms)
	}
	for _, key := range plan.Threads {
		ch, ts, _ := strings.Cut(key, "/")
		ms, err := c.messages(ctx, opThread, "thread", map[string]any{"channel": ch, "ts": ts, "limit": plan.Limit})
		if err != nil {
			p.Errors = append(p.Errors, "thread "+key+": "+err.Error())
			continue
		}
		for i := range ms {
			if ms[i].Channel == "" {
				ms[i].Channel = ch
			}
			if ms[i].ThreadTS == "" && ms[i].TS != ts {
				ms[i].ThreadTS = ts
			}
			ms[i].Key = messageKey(ms[i].Channel, ms[i].TS, ms[i].Permalink, ms[i].Text)
		}
		add(ms)
	}
	for _, ref := range plan.Channels {
		ms, err := c.messages(ctx, opHistory, "channel", map[string]any{"channel": ref, "oldest": tsOf(plan.Since), "limit": plan.Limit})
		if err != nil {
			p.Errors = append(p.Errors, "channel "+ref+": "+err.Error())
			continue
		}
		for i := range ms {
			if ms[i].Channel == "" {
				ms[i].Channel = ref
				ms[i].Key = messageKey(ms[i].Channel, ms[i].TS, ms[i].Permalink, ms[i].Text)
			}
		}
		add(ms)
	}
	sort.SliceStable(p.Messages, func(i, j int) bool { return p.Messages[i].TS > p.Messages[j].TS })
	if p.Messages == nil {
		p.Messages = []Message{}
	}
	return p
}

// fill copies into m what n knows and m does not.
func fill(m *Message, n Message) {
	if m.ChannelName == "" {
		m.ChannelName = n.ChannelName
	}
	if m.ThreadTS == "" {
		m.ThreadTS = n.ThreadTS
	}
	if m.UserName == "" {
		m.UserName = n.UserName
	}
	if m.User == "" {
		m.User = n.User
	}
	if m.Permalink == "" {
		m.Permalink = n.Permalink
	}
	if m.Text == "" {
		m.Text = n.Text
	}
	m.DM = m.DM || n.DM
}
