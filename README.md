<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-animated-dark.svg">
    <img alt="o-slack" src="assets/logo-animated-light.svg" width="300">
  </picture>
  <br>
  <a href="go.mod"><img alt="Go version" src="https://img.shields.io/github/go-mod/go-version/ofamtools/o-slack"></a>
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/github/license/ofamtools/o-slack"></a>
</p>

<p align="center">The family's Slack page: mentions, DMs, replies in threads you watch and on-call activity in one place, with each new one sent to the mtm inbox.</p>

o-slack (`o slack` under [oden](https://github.com/ofamtools/oden)'s `o`, or `o-slack`) reads Slack only through an MCP server, using [o-mcp](https://github.com/ofamtools/o-mcp)'s client and that server's credentials: the ones its configuration gives, or an OAuth sign-in o-mcp keeps in the macOS Keychain (`o-mcp login`). It holds no Slack token, stores none and never falls back to calling Slack directly. It never writes to Slack. What you watch, ignore and flag on-call is o-slack's own state, kept in its data directory.

Which of the server's tools does what is **settings, not code**. A gateway that names its tools or arguments differently is fixed with `o-slack settings set`, and `o-slack map check` tells you which lines to set.

## Install

Module `ofam.dev/o-slack`; requires Go 1.27 or newer. o-mcp is a private module:

```sh
GOPRIVATE='ofam.dev/*' GOPROXY=direct go install ofam.dev/o-slack@latest
```

or, from a checkout:

```sh
export GOWORK=off GOFLAGS=-trimpath
gofmt -l . && go vet ./... && go test -race ./...
go build -o ~/bin/o-slack .
```

ochazuke (`v0.25.0`) and oden (`v0.1.1`) are pinned in `go.mod`. o-mcp is replaced by its `oauth` worktree (`../../../o-mcp/.worktrees/oauth`) for the sign-in lane; the coordinator pins it to a commit before merging.

## On the work machine

o-slack is meant for one machine: the work laptop, where Slack is reached through the company's Runlayer MCP gateway, which holds the Slack auth. Everything here was built against a fake server, so the first run there is a verification.

1. **Install** o-mcp and o-slack (see Install).
2. **Find the gateway.** `o-mcp configured` lists every server the harnesses (Claude Code, Codex, ...) configure; o-slack has no server list of its own. If the gateway's name is `runlayer`, `runlayer-plugin`, `slack` or `slack-no-dms`, o-slack finds it (in that order). Otherwise, and for the rest of you:
   ```sh
   o-slack settings set slack.server NAME      # a name or an ID from o-mcp configured
   o-slack settings set slack.me miso           # your handle
   o-slack settings set slack.me-id U0123ABC    # your user ID (the default mentions query uses it)
   o-slack settings set slack.workspace-url https://NAME.slack.com
   ```
3. **Sign in.** Runlayer asks for an OAuth sign-in in the browser:
   ```sh
   o-mcp login runlayer          # opens the browser; prints the URL too
   o-mcp auth status runlayer    # signed-in, issuer, expiry, scopes (never the token)
   ```
   o-mcp keeps the tokens in the macOS Keychain (service `o-mcp`) and refreshes them; o-slack stores none. o-slack never opens a browser itself, so a scheduled `o-slack signals` never pops a window. When the sign-in lapses, o-slack fails with **needs sign-in** and the exact `o-mcp login` command to run. **Unreachable** is something else: the gateway rejected the header or bearer-token variable its config gives, or answered 403 (`o-mcp test runlayer` shows which).
4. **Save the schemas and check the mapping.**
   ```sh
   o-mcp discover runlayer             # saves the tool schemas (no secrets)
   o-slack map check --format table    # live, through the server
   o-slack map check --offline         # the same against o-mcp's saved schemas
   ```
   Each operation is `ok`, `missing` (the tool named is not listed), `unmapped` (no candidate is listed), `args` (an argument was renamed, or one the tool requires is not sent) or `optional` (permalink only). Each problem prints the line that fixes it.
5. **Fix the mapping in settings.** Paste the `fix:` lines, for example:
   ```sh
   o-slack settings set slack.tools.history slack_channel_history
   o-slack settings set slack.args.thread '["channel=channel_id","ts=thread_ts","limit=limit"]'
   o-slack settings set slack.results.search '["items=data.hits","text=body.plain"]'
   ```
   Run `map check` again until the summary says `every operation maps`. A tool renamed to a name o-slack does not know falls back to its own parameter names, so expect a second round for its arguments. `o-slack map show` shows where each choice came from (settings, candidate or default). For results, `o-mcp call runlayer TOOL --data '{...}'` shows a real answer to map fields from.
6. **Look before sending.**
   ```sh
   o-slack message mentions --format table
   o-slack signals --dry-run      # the first real run records a baseline and sends nothing
   o-slack signals                # then from a schedule
   ```

Bring back the saved schemas (`o-mcp discover` output, no secrets) and any settings you had to change, so the defaults table below can be corrected.

## Usage

```sh
o-slack                                         # the Slack page (also o-slack view; --smoke renders one frame)
o-slack message mentions|dms|threads|oncall|ignored
o-slack message list [--collection C] [--fresh]
o-slack message show|open|copy-link|read-thread|author|channel --target MSG
o-slack message watch-thread|unwatch-thread|ignore|unignore --target MSG
o-slack message send-to-inbox [WHY] --target MSG
o-slack message refresh                         # poll now; the counts
o-slack channel list [--fresh]
o-slack channel show|open|copy-link|history --target CHANNEL
o-slack channel watch|unwatch|ignore|unignore|oncall|not-oncall --target CHANNEL
o-slack channel send-to-inbox [WHY] --target CHANNEL
o-slack user show|open|copy-id --target USER
o-slack map check|show [--offline] [--schemas FILE]
o-slack map candidates                          # the default tool table
o-slack signals [--dry-run]                     # also signal preview / signal push
o-slack settings list|set|reset
o-slack catalog                                 # the nouns, verbs and page, for mtm
```

`MSG` is a row id `slack-message/CHANNEL/TS`, `CHANNEL/TS` or a permalink. `CHANNEL` is `slack-channel/ID`, an ID or `#name`. `USER` is `slack-user/ID` or an ID. The nouns have short aliases: `message`, `channel`, `user`, `signal`, `map`. A list verb answers rows (`{noun, id, label, data, cells}`); `--format table` prints them as a table.

## Concepts

### Operations and the mapping

o-slack needs six operations from the server. For each one, `slack.tools.OP` names the tool. When it is empty, o-slack picks the first candidate the server lists, in this order:

| Op | Used for | Candidates, in order (argument mapping) | Confidence |
|---|---|---|---|
| `search` | mentions and DMs, every poll | `slack_search_public_and_private` (query, limit); `slack_search_public`; `conversations_search_messages` (query=search_query); `search_messages` (limit=count); `search` | medium: the first two are the names ~/proj/now guesses for Slack's own MCP server; none is seen on Runlayer yet |
| `history` | on-call and watched channels | `slack_read_channel` (channel=channel_id, oldest, limit); `conversations_history`; `slack_get_channel_history` | low-medium |
| `thread` | watched threads, read-thread | `slack_read_thread` (channel=channel_id, ts=message_ts); `conversations_replies` (ts=thread_ts); `slack_get_thread_replies` | low-medium: the ts argument name differs between servers |
| `channels` | the Channels list, resolving `#name` | `slack_search_channels`; `channels_list`; `slack_list_channels` | low |
| `user` | `slack-user show`, `message author` | `slack_read_user_profile` (user=user_id); `slack_get_user_profile` | low |
| `permalink` | only when a result has no permalink and `slack.workspace-url` is unset | `slack_get_permalink` | low; optional |

The table lives in `mapping.go` (`candidates`), and `o-slack map candidates` prints it. `slack.args.OP` overrides the arguments as `PARAM=SERVER_ARG` pairs plus `SERVER_ARG:=VALUE` constants (a JSON value). `slack.results.OP` says where each field sits in the answer, as `FIELD=PATH` (dotted keys, `|` between alternatives) over defaults that cover Slack's Web API spellings. Results are read from `structuredContent`, or from a single JSON text block. A prose answer is read for permalinks, one message per paragraph, with a leading `**name** in #channel` taken as the author and channel. A reply is a message whose `thread_ts` differs from its `ts`.

Write tools (send, post, react, ...) are never suggested and never called.

### Collections

| Collection | What is in it |
|---|---|
| Mentions | messages that mention you (`<@ID>` or `@handle`), or that your mentions search matched |
| DMs | direct messages (`slack.watch-dms`) |
| Threads | replies in threads you watch. A thread is watched by hand (`watch-thread`, `w`), or automatically when you are mentioned in it (`slack.auto-watch-threads`, for `slack.thread-watch-age`). A thread you unwatch or ignore is not auto-watched again |
| On-call | while you are on call, every message in an on-call channel and every mention of `oncall.tag`; and, at any time, every message in a channel you watch |
| Ignored | what an ignored thread, channel or user (`slack.ignore-users`) said. Ignoring a channel never hides DMs or on-call messages |

Your own messages are in no collection.

### On-call

`oncall.mode` is `auto` (the window is `oncall.start`, a Monday, for `oncall.weeks` weeks), `on` or `off`. On-call channels are `oncall.channels` plus the ones you flag (`channel oncall`, `o`). A `#name` is resolved to an ID by reading the channels once.

### Signals

`o-slack signals` polls once (it reads from the last run, less ten minutes, at most `poll.lookback` back) and sends each new message that is a mention, DM, watched-thread reply, on-call message or watched-channel message:

```sh
mtm action-item add --source slack --key CHANNEL/TS --url PERMALINK --title TITLE --why WHY [--text TEXT]
```

The key is the message's stable id, so mtm deduplicates it. `$O_SLACK_MTM` names another mtm and `$O_SLACK_MTM_ARGS` adds words before `action-item`. The command is spelled once, in `inboxCommand` (`signals.go`).

- The first run records a baseline and sends nothing, so connecting o-slack does not flood the inbox.
- A message older than the last run is backlog and sends nothing, for example the earlier replies of a thread watched since.
- Ignored messages and your own send nothing, but they are recorded, so unignoring replays nothing.
- A signal that fails to send is kept and sent first next run. A read that fails makes the next run read again from the same point.
- `--dry-run` prints the signals and their commands, sends nothing and leaves the last run as it was.

Run it from a schedule (mtm's, cron or a loop) to have signals arrive without a key press.

### Settings and data

| Setting | Default |
|---|---|
| `slack.server` | empty: tries `slack.server-candidates` (`runlayer`, `runlayer-plugin`, `slack`, `slack-no-dms`) |
| `slack.tools.OP`, `slack.args.OP`, `slack.results.OP` | empty: the candidates above |
| `slack.me`, `slack.me-id`, `slack.workspace-url` | empty |
| `slack.query.mentions` | `<@{me_id}> after:{since}` |
| `slack.query.dms` | `to:me after:{since}` (now's `to:me after:yesterday`, with the date filled in) |
| `slack.watch-dms`, `slack.auto-watch-threads` | true |
| `slack.thread-watch-age` | `168h` |
| `slack.limit` | 50 |
| `slack.ignore-users` | none |
| `oncall.mode`, `oncall.start`, `oncall.weeks`, `oncall.channels`, `oncall.tag` | `auto`, none, 1, none, none |
| `poll.max-age`, `poll.lookback`, `channels.max-age` | `60s`, `24h`, `1h` |

`{since}` is the day before the poll reads from, because Slack's `after:` is exclusive and by date. Settings live in `~/.config/o-slack/settings.json` (`$XDG_CONFIG_HOME`). The data directory (`$O_SLACK_DATA`, else `$XDG_DATA_HOME/o-slack`, else `~/.local/share/o-slack`) holds:
- `state.json`: threads and channels marked;
- `poll.json`: the last poll;
- `channels.json`: the last channels read;
- `signals.json`: the last signals run.

No credential is ever written there. `$O_SLACK_OPEN` names the URL opener and `$O_SLACK_CLIPBOARD` the clipboard command.

## The Slack page

The catalog declares the page as a data page (layout `split`), so mtm draws it from o-slack's rows. It shows the lists Mentions, DMs, Threads, On-call and Channels, each from its source verb, beside the selected row's record. `o-slack` mounts the same page over itself with ochazuke's `MountPage`, as a host does, and reloads it after a change and every `poll.max-age`.

| Key | On a message | On a channel |
|---|---|---|
| `enter` | open the permalink | open the channel |
| `y y` | copy the permalink | copy the link |
| `w` / `W` | watch / unwatch the thread | watch / unwatch the channel |
| `d d` | ignore the thread | ignore the channel |
| `g t` | read the thread | |
| `o` / `O` | | flag / unflag on-call |
| `g i` | send to the mtm inbox | send to the mtm inbox |
| `:` `?` `q` | the palette (every verb), every key, quit | |

The key rows are the catalog's, so mtm's page answers them too, and `bindings.json` can change them.

## In the family

- **mtm.** o-slack is an extension: mtm mounts the Slack page from `o-slack catalog` and runs its verbs as `o-slack NOUN VERB --target ID`. Signals go in through mtm's command line only.
- **o-mcp.** o-slack is a client of `o-mcp/client`: it uses `Configured` and `Find` to pick the server, `Connect` to open it (never interactive), and `Session.Tools` and `Session.Call` to use it. `map check --offline` reads o-mcp's saved schemas (`Store`). Auth is o-mcp's alone: `client.IsNeedsLogin` becomes the needs-sign-in message with `o-mcp login NAME`, `client.IsUnreachable` the rejected-credential one.
- **o-gh.** The same shape for GitHub: page, collections, the signals seam, tests.

## Logo

The logo is a speech bubble with a channel hash knocked out of it, in green. It is the family's own drawing, not Slack's mark or colours. `python3 docs/logo/build.py` writes `assets/icon.svg`, `assets/logo-light.svg` and `assets/logo-dark.svg`, with the wordmark in the family's lettering (`--check` confirms the glyphs still redraw ochazuke's and matomeru's).

MIT license.
