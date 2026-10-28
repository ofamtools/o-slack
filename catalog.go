package main

import (
	"time"

	"github.com/unixsuperhero/ochazuke"
	ocli "github.com/unixsuperhero/ochazuke/cli"
)

// The catalog: o-slack as a family extension. Nouns (each with a short
// alias for the command line):
//
//	slack-message (message)  a mention, DM, thread reply or channel message
//	                         (row id slack-message/CHANNEL/TS; --target also
//	                         takes CHANNEL/TS or a permalink)
//	slack-channel (channel)  a channel, with its on-call flag and marks
//	                         (slack-channel/ID; --target takes an ID or #name)
//	slack-user (user)        a user's profile (slack-user/ID)
//	slack-signal (signal)    the inbox signals: preview, push
//	slack-map (map)          the tool mapping: check, show, candidates
//
// The Slack page (slackPage) is a split of Mentions, DMs, Threads, On-call
// and Channels beside the selected row's record; a host (mtm) mounts it
// from the catalog and fills each list from its source verb.

// Version is the catalog's version.
const Version = "0.1.0"

const (
	messageNoun = "slack-message"
	channelNoun = "slack-channel"
	userNoun    = "slack-user"
	signalNoun  = "slack-signal"
	mapNoun     = "slack-map"
	pageID      = "slack"
	targetsID   = "slack/targets"
)

// The settings (o-slack settings set NAME VALUE; ~/.config/o-slack/settings.json).
const (
	setServer          = "slack.server"
	setServerFallbacks = "slack.server-candidates"
	setTools           = "slack.tools."
	setArgs            = "slack.args."
	setResults         = "slack.results."
	setWorkspace       = "slack.workspace-url"
	setMe              = "slack.me"
	setMeID            = "slack.me-id"
	setWatchDMs        = "slack.watch-dms"
	setAutoWatch       = "slack.auto-watch-threads"
	setThreadAge       = "slack.thread-watch-age"
	setQueryMentions   = "slack.query.mentions"
	setQueryDMs        = "slack.query.dms"
	setLimit           = "slack.limit"
	setIgnoreUsers     = "slack.ignore-users"
	setOncallMode      = "oncall.mode"
	setOncallStart     = "oncall.start"
	setOncallWeeks     = "oncall.weeks"
	setOncallChannels  = "oncall.channels"
	setOncallTag       = "oncall.tag"
	setMaxAge          = "poll.max-age"
	setLookback        = "poll.lookback"
	setChannelsAge     = "channels.max-age"
)

const (
	defaultMentions = "<@{me_id}> after:{since}"
	defaultDMs      = "to:me after:{since}"
)

var defaultFallbacks = []string{"runlayer", "runlayer-plugin", "slack", "slack-no-dms"}

var messageColumns = []ochazuke.Column{
	{Title: "Kind", Width: 8},
	{Title: "Where", Min: 8, Max: 22},
	{Title: "Who", Max: 16, Priority: 2},
	{Title: "Text", Min: 16},
	{Title: "Age", Width: 5, Align: ochazuke.AlignRight, Priority: 3},
}

var channelColumns = []ochazuke.Column{
	{Title: "Channel", Min: 10, Max: 28},
	{Title: "On-call", Width: 8},
	{Title: "Mark", Width: 8, Priority: 1},
	{Title: "Topic", Priority: 2},
}

var mapColumns = []ochazuke.Column{
	{Title: "Op", Width: 9},
	{Title: "Tool", Min: 12, Max: 34},
	{Title: "Status", Width: 8},
	{Title: "Problems and fixes", Min: 20},
}

func listID(c string) string { return pageID + "/" + c }

// pageLists are the page's message lists, in order.
var pageLists = []string{colMentions, colDMs, colThreads, colOncall}

// slackPage is the Slack page as the catalog declares it.
func slackPage() ocli.CatalogPage {
	p := ocli.CatalogPage{ID: pageID, Title: "Slack", Layout: ocli.LayoutSplit,
		Detail: &ocli.CatalogDetail{Fields: []string{
			"kind", "reason", "channel", "channel_name", "user", "user_name", "text", "permalink", "ts", "thread_ts", "at",
			"via", "watching", "ignored", "ignored_by", "stale",
			"id", "name", "topic", "members", "private", "oncall", "oncall_by", "oncall_active", "watched", "read",
		}}}
	for _, id := range pageLists {
		c, _ := collectionByID(id)
		p.Lists = append(p.Lists, ocli.CatalogList{ID: listID(id), Noun: messageNoun, Source: messageNoun + "." + id, Title: c.Title})
	}
	p.Lists = append(p.Lists, ocli.CatalogList{ID: listID("channels"), Noun: channelNoun, Source: channelNoun + ".list", Title: "Channels"})
	return p
}

// newRuntime builds the runtime: settings, nouns, verbs, key rows and the
// declared page. targets adds the shell's --target list.
func (t *Tool) newRuntime(targets bool) error {
	r := ochazuke.NewRuntime()
	r.Config().App = "o-slack"
	t.r = r
	settings := []ochazuke.Setting{
		{Name: setServer, Type: ochazuke.SettingString, Default: "", Description: "The Slack MCP server: an o-mcp server name or ID (o-mcp configured lists them); empty tries slack.server-candidates"},
		{Name: setServerFallbacks, Type: ochazuke.SettingList, Default: defaultFallbacks, Description: "Server names tried in order when slack.server is empty"},
		{Name: setWorkspace, Type: ochazuke.SettingString, Default: "", Description: "Your workspace URL (https://NAME.slack.com): builds permalinks when results carry none"},
		{Name: setMe, Type: ochazuke.SettingString, Default: "", Description: "Your Slack handle, without @ ({me} in queries; mentions and your own messages)"},
		{Name: setMeID, Type: ochazuke.SettingString, Default: "", Description: "Your Slack user ID, U... ({me_id} in queries; <@ID> mentions)"},
		{Name: setWatchDMs, Type: ochazuke.SettingBoolean, Default: true, Description: "Read direct messages (slack.query.dms) and signal them"},
		{Name: setAutoWatch, Type: ochazuke.SettingBoolean, Default: true, Description: "Watch the thread of each message that mentions you, so its replies become signals"},
		{Name: setThreadAge, Type: ochazuke.SettingDuration, Default: 7 * 24 * time.Hour, Description: "How long an automatic thread watch lasts"},
		{Name: setQueryMentions, Type: ochazuke.SettingString, Default: defaultMentions, Description: "The search for mentions ({me}, {me_id}, {since}: the day before the poll reads from); empty reads none"},
		{Name: setQueryDMs, Type: ochazuke.SettingString, Default: defaultDMs, Description: "The search for direct messages ({me}, {me_id}, {since}); empty reads none"},
		{Name: setLimit, Type: ochazuke.SettingInteger, Default: 50, Description: "Messages or channels per read"},
		{Name: setIgnoreUsers, Type: ochazuke.SettingList, Default: []string{}, Description: "Users (IDs or names, such as bots) whose messages are Ignored"},
		{Name: setOncallMode, Type: ochazuke.SettingString, Default: OncallAuto, Enum: []string{OncallAuto, OncallOn, OncallOff}, Description: "auto (by oncall.start and oncall.weeks), on or off"},
		{Name: setOncallStart, Type: ochazuke.SettingString, Default: "", Description: "The Monday your on-call week starts, YYYY-MM-DD"},
		{Name: setOncallWeeks, Type: ochazuke.SettingInteger, Default: 1, Description: "How many weeks from oncall.start"},
		{Name: setOncallChannels, Type: ochazuke.SettingList, Default: []string{}, Description: "Channels (IDs or #names) whose every message is a signal while you are on call; slack-channel oncall adds one"},
		{Name: setOncallTag, Type: ochazuke.SettingString, Default: "", Description: "The on-call user group or tag (such as @oncall) whose mentions are signals while on call"},
		{Name: setMaxAge, Type: ochazuke.SettingDuration, Default: 60 * time.Second, Description: "How long a poll answers the lists before the next (o-slack signals always polls)"},
		{Name: setLookback, Type: ochazuke.SettingDuration, Default: 24 * time.Hour, Description: "How far back a poll reads when there is no last run (and the most a run reads back)"},
		{Name: setChannelsAge, Type: ochazuke.SettingDuration, Default: time.Hour, Description: "How long the channels read answers the Channels list"},
	}
	for _, op := range ops {
		settings = append(settings,
			ochazuke.Setting{Name: setTools + op.Name, Type: ochazuke.SettingString, Default: "", Section: "slack.tools",
				Description: "The tool for " + op.About + "; empty picks the first the server lists of " + candidateNames(op.Name)},
			ochazuke.Setting{Name: setArgs + op.Name, Type: ochazuke.SettingList, Default: []string{}, Section: "slack.args",
				Description: "The tool's arguments: PARAM=SERVER_ARG (PARAM one of " + joinList(op.Params) + ") and SERVER_ARG:=VALUE constants; empty takes the candidate's"},
			ochazuke.Setting{Name: setResults + op.Name, Type: ochazuke.SettingList, Default: []string{}, Section: "slack.results",
				Description: "Where the result's fields are: FIELD=PATH (dotted keys, | between alternatives; items= is the record list), over the defaults"},
		)
	}
	for _, s := range settings {
		if err := r.DeclareSetting(s); err != nil {
			return err
		}
	}
	for alias, noun := range map[string]string{"message": messageNoun, "channel": channelNoun, "user": userNoun, "signal": signalNoun, "map": mapNoun} {
		if err := r.Alias(alias, noun); err != nil {
			return err
		}
	}
	if targets {
		var err error
		if t.targets, err = r.AddList(targetsID, nil, ochazuke.Columns(messageColumns...)); err != nil {
			return err
		}
	}
	t.define()
	return ocli.DeclarePage(r, slackPage())
}

func joinList(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

// define registers the nouns' verbs, parameters and key rows.
func (t *Tool) define() {
	r := t.r
	msg := r.Define(messageNoun).Columns(messageColumns...).
		Verb("list", t.listMessages, "List the messages of a collection (mentions, dms, threads, oncall, ignored), or of all but ignored").Rows(messageNoun).Fetch().Quiet()
	for _, c := range collections {
		msg.Verb(c.ID, t.collectionRows(c.ID), c.Title+": "+c.Description).Rows(messageNoun).Fetch().Quiet()
	}
	msg.Verb("show", t.onMessage(t.showMessage), "Show one message's record (from the last poll, else what was recorded)").Fetch().
		Verb("open", t.onMessage(t.openMessage), "Open the message's permalink in the browser").
		Verb("copy-link", t.onMessage(t.copyMessageLink), "Copy the message's permalink to the clipboard and answer it").
		Verb("read-thread", t.onMessage(t.readThread), "Read the message's thread: its replies as rows").Rows(messageNoun).Fetch().
		Verb("watch-thread", t.onMessage(t.watchThread), "Watch the message's thread: its replies become signals").
		Verb("unwatch-thread", t.onMessage(t.unwatchThread), "Stop watching the message's thread").
		Verb("ignore", t.onMessage(t.ignoreThread), "Ignore the message and its thread: they move to Ignored and send no signals").
		Verb("unignore", t.onMessage(t.unignoreThread), "Stop ignoring the message's thread").
		Verb("author", t.onMessage(t.messageAuthor), "Show the profile of the message's author").Fetch().
		Verb("channel", t.onMessage(t.messageChannel), "Show the message's channel").Fetch().
		Verb("send-to-inbox", t.onMessage(t.sendMessage), "Send the message to the mtm inbox as an action item (mtm action-item add --source slack)").Positional("why").
		Verb("refresh", t.refresh, "Poll Slack now and answer each collection's count")
	msg.Mode("normal").
		Bind("enter", ochazuke.Do(messageNoun+".open")).
		Bind("y y", ochazuke.Do(messageNoun+".copy-link")).
		Bind("w", ochazuke.Do(messageNoun+".watch-thread")).
		Bind("W", ochazuke.Do(messageNoun+".unwatch-thread")).
		Bind("d d", ochazuke.Do(messageNoun+".ignore")).
		Bind("g i", ochazuke.Do(messageNoun+".send-to-inbox")).
		Bind("g t", ochazuke.Do(messageNoun+".read-thread"))
	ch := r.Define(channelNoun).Columns(channelColumns...).
		Verb("list", t.listChannels, "List the channels: the server's, the on-call ones and the ones you marked").Rows(channelNoun).Fetch().Quiet().
		Verb("show", t.onChannel(t.showChannel), "Show one channel's record").Fetch().
		Verb("open", t.onChannel(t.openChannel), "Open the channel in the browser (needs slack.workspace-url and its ID)").
		Verb("copy-link", t.onChannel(t.copyChannelLink), "Copy the channel's link to the clipboard and answer it").
		// No Rows(messageNoun) here: ochazuke's CatalogDoc.RowSource takes the
		// first verb (sorted) declaring rows, which would make history, not
		// list, the channel noun's row source in a host (workaround; o-plans todo).
		Verb("history", t.onChannel(t.channelHistory), "Read the channel's recent messages as rows").Fetch().
		Verb("watch", t.onChannel(t.watchChannel), "Watch the channel: its every message becomes a signal").
		Verb("unwatch", t.onChannel(t.unwatchChannel), "Stop watching the channel").
		Verb("ignore", t.onChannel(t.ignoreChannel), "Ignore the channel: its mentions and replies go to Ignored (DMs and on-call still count)").
		Verb("unignore", t.onChannel(t.unignoreChannel), "Stop ignoring the channel").
		Verb("oncall", t.onChannel(t.markOncall), "Flag the channel on-call: while you are on call, its every message is a signal").
		Verb("not-oncall", t.onChannel(t.unmarkOncall), "Drop the channel's on-call flag (a channel in oncall.channels stays: edit the setting)").
		Verb("send-to-inbox", t.onChannel(t.sendChannel), "Send the channel to the mtm inbox as an action item").Positional("why").
		Verb("refresh", t.refreshChannels, "Read the server's channels now")
	ch.Mode("normal").
		Bind("enter", ochazuke.Do(channelNoun+".open")).
		Bind("y y", ochazuke.Do(channelNoun+".copy-link")).
		Bind("w", ochazuke.Do(channelNoun+".watch")).
		Bind("W", ochazuke.Do(channelNoun+".unwatch")).
		Bind("d d", ochazuke.Do(channelNoun+".ignore")).
		Bind("o", ochazuke.Do(channelNoun+".oncall")).
		Bind("O", ochazuke.Do(channelNoun+".not-oncall")).
		Bind("g i", ochazuke.Do(channelNoun+".send-to-inbox"))
	r.Define(userNoun).
		Verb("show", t.onUser(t.showUser), "Show a user's profile").Fetch().
		Verb("open", t.onUser(t.openUser), "Open the user's profile in the browser (needs slack.workspace-url)").
		Verb("copy-id", t.onUser(t.copyUserID), "Copy the user's ID to the clipboard and answer it")
	r.Define(signalNoun).
		Verb("preview", t.signalVerb(true), "What o-slack signals would send now (o-slack signals --dry-run)").Fetch().
		Verb("push", t.signalVerb(false), "Poll, and send each new mention, DM, thread reply and on-call message to the mtm inbox (o-slack signals)")
	r.Define(mapNoun).Columns(mapColumns...).
		Verb("check", t.checkMap, "Compare the tool mapping with the server's tools: missing tools, renamed or missing arguments, and the settings that fix them").Rows(mapNoun).Fetch().
		Verb("show", t.showMap, "The mapping in effect, against the server's tools (--offline: o-mcp's saved schemas)").Rows(mapNoun).Fetch().
		Verb("candidates", t.mapCandidates, "The candidate tools o-slack tries for each operation, in order").Rows(mapNoun).Fetch().Quiet()
	fresh := ochazuke.Parameter{Type: "boolean", Description: "Poll now, not the cached poll"}
	offline := map[string]ochazuke.Parameter{
		"offline": {Type: "boolean", Description: "Use o-mcp's saved schemas (o-mcp discover) instead of connecting"},
		"schemas": {Type: "string", Description: "A saved snapshot or tools/list JSON file to check against instead"},
	}
	why := map[string]ochazuke.Parameter{"why": {Type: "string", Description: "Why it needs you (default: sent from o-slack)"}}
	params := map[string]map[string]ochazuke.Parameter{
		messageNoun + ".list": {
			"collection": {Type: "string", Enum: append([]string{"all"}, colMentions, colDMs, colThreads, colOncall, colIgnored), Description: "The collection (default all but ignored)"},
			"fresh":      fresh,
		},
		messageNoun + ".send-to-inbox": why,
		channelNoun + ".send-to-inbox": why,
		channelNoun + ".list":          {"fresh": {Type: "boolean", Description: "Read the server's channels now"}},
		mapNoun + ".check":             offline,
		mapNoun + ".show":              offline,
	}
	for _, c := range collections {
		params[messageNoun+"."+c.ID] = map[string]ochazuke.Parameter{"fresh": fresh}
	}
	for key, p := range params {
		op := r.Operations[key]
		op.Parameters = p
		r.Operations[key] = op
	}
}
