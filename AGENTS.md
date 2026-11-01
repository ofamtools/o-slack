# o-slack: agent guide

`o-slack` (so `o slack`) is the family's Slack tool. It provides the Slack page (Mentions, DMs, Threads, On-call, Channels) and sends signals into the mtm inbox. It reaches Slack only through an MCP server, via `ofam.dev/o-mcp/client`, and never writes to Slack. It runs only on the owner's work machine, where the Slack MCP server is the company's Runlayer gateway. Here it is built and tested against a fake go-sdk server. Design: `~/hub/o-plans/plans/inbox-and-signals.md` (lane L4).

It meets mtm only through each other's command line. mtm mounts the page from `o-slack catalog` and runs `o-slack NOUN VERB --target ID`. o-slack runs `mtm action-item add --source slack --key KEY --url URL --title T --why WHY`, which is spelled once, in `inboxCommand`.

## Plans and todos

Family plans, todos and the roadmap live in `~/hub/o-plans/` (`README.md` is the index). Read it before planning work; add or update items there. Do not add a TODO.md or docs/plans/ to this repo.

## Where does X live

| Concern | File |
|---|---|
| `main`, the oden app, `--target` reveal, `o slack signals` | `main.go` |
| Server resolution (`slack.server`, fallbacks), the dialer (`dialMCP`, `dialWith`), the needs-sign-in and unreachable messages, o-mcp's store | `conn.go` |
| The operations, the candidate tool table, args and result paths, `map check`, record extraction, prose | `mapping.go` |
| `Message`, `Channel`, on-call window, `classify`, the poll | `slack.go` |
| Collections, signal diffing, the inbox seam (`inboxCommand`, `mtmInbox`), `runSignals` | `signals.go` |
| `Tool`, settings as `Config`, the cached poll (`current`), auto-watch, channels | `tool.go` |
| Verb handlers | `verbs.go` |
| The catalog: settings, nouns, verbs, key rows, the declared page | `catalog.go` |
| Rows and cells, URL opener, clipboard | `rows.go` |
| The page on its own (`MountPage`) | `tui.go` |
| The data directory: state, lock, atomic writes | `state.go` |
| The fake Slack MCP server and fixtures | `fake_test.go`, `testdata/slack.json` |
| The logo | `docs/logo/build.py` |

## Invariants

- Read only toward Slack. No verb maps or calls a write tool (`writeTool` filters suggestions). Watched, ignored and on-call marks are o-slack's own.
- Auth goes only through o-mcp/client: the server's own configured credentials, or an OAuth sign-in o-mcp keeps in the Keychain (`o mcp login`). Never store a token, never call Slack directly, and never connect interactively: a poll fails with the login command, it never opens a browser.
- The tool mapping is settings (`slack.tools.*`, `slack.args.*`, `slack.results.*`) over one table (`candidates`). Fix a server's names with settings, not code.
- Record first: watch, ignore, on-call and send-to-inbox work on a message or channel o-slack has not read.
- Signals: the first run is a baseline. Backlog (older than the last run less `overlap`) sends nothing. Unsent signals stay pending. `--dry-run` writes nothing.
- Work that calls the server or the inbox runs off the loop (`ochazuke.Pending`); handlers read settings on the loop into a `Config`.
- Tests use the fake MCP server (in memory) and a fake inbox. They reach no Slack, no real MCP server and no mtm. Live verification happens on the work machine only (README, "On the work machine").
- Every commit: `gofmt -l . && go vet ./... && go test -race ./...` green (`GOWORK=off GOFLAGS=-trimpath`), and `go run . catalog` answers. No amends.

Build: `go build -o ~/bin/o-slack .`. Pinned in `go.mod`: ochazuke `v0.31.0`, oden `v0.1.1`; o-mcp is replaced by its `oauth` worktree during the sign-in lane (the coordinator pins it). o-mcp is private: fetch with `GOPRIVATE=ofam.dev/* GOPROXY=direct`.
