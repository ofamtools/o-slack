package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"ofam.dev/o-mcp/client"
)

// The connection: o-slack reaches Slack only through an MCP server o-mcp's
// client finds in the harnesses' configuration (slack.server names it).
// Credentials are that server's own (its headers, a bearer-token variable,
// a stdio server's env) or an OAuth sign-in o-mcp keeps in the Keychain
// (`o-mcp login NAME`); o-slack holds and stores none.

// Conn is one open session with the Slack MCP server.
type Conn interface {
	Tools(ctx context.Context) ([]*mcp.Tool, error)
	Call(ctx context.Context, tool string, args map[string]any) (*mcp.CallToolResult, error)
	Close() error
}

// Dialer opens a session with a resolved server.
type Dialer func(ctx context.Context, s client.Server) (Conn, error)

// Resolver finds the server slack.server names (files only, no connect).
type Resolver func(ref string, fallbacks []string) (client.Server, error)

// configuredServer is the real Resolver: ref by o-mcp's Find, else (ref
// empty) the first fallback name that is configured.
func configuredServer(ref string, fallbacks []string) (client.Server, error) {
	servers, _ := client.Configured(client.Options{})
	return findServer(servers, ref, fallbacks)
}

func findServer(servers []client.Server, ref string, fallbacks []string) (client.Server, error) {
	if ref = strings.TrimSpace(ref); ref != "" {
		s, err := client.Find(servers, ref)
		if errors.Is(err, client.ErrNotFound) {
			return s, fmt.Errorf("slack.server %q: %w (o-mcp configured lists the servers; set slack.server to one)", ref, err)
		}
		return s, err
	}
	for _, name := range fallbacks {
		if s, err := client.Find(servers, name); err == nil {
			return s, nil
		}
	}
	return client.Server{}, fmt.Errorf("slack.server is not set and no configured MCP server is named %s (o-mcp configured lists them; o-slack settings set slack.server NAME)", strings.Join(fallbacks, ", "))
}

// dialMCP is the real Dialer: o-mcp's defaults, so a server signed in
// with `o-mcp login` gets its token from the macOS Keychain. It is never
// interactive: a scheduled poll never opens a browser.
var dialMCP = dialWith(client.Options{})

// dialWith is a Dialer connecting with o (a test's home and keyring).
func dialWith(o client.Options) Dialer {
	return func(ctx context.Context, s client.Server) (Conn, error) {
		sess, err := client.Connect(ctx, s, o)
		if err != nil {
			return nil, explainConnect(s, err)
		}
		return sess, nil
	}
}

// explainConnect says what to do about a server o-slack cannot reach.
func explainConnect(s client.Server, err error) error {
	switch {
	case client.IsNeedsLogin(err):
		var n *client.NeedsLoginError
		errors.As(err, &n)
		return fmt.Errorf("%w\n%s needs an OAuth sign-in, and o-slack never opens a browser itself. Sign in once in a terminal:\n\n  %s\n\n"+
			"o-mcp keeps the token in the macOS Keychain and refreshes it; o-slack stores none. Then run o-slack again", err, s.Name, n.Command())
	case client.IsUnreachable(err):
		return fmt.Errorf("%w\no-slack connects with the server's own configured credentials and stores no token. "+
			"The server rejected the header or bearer-token variable its config gives, or answered 403: check them with o-mcp test %s", err, s.Name)
	case errors.Is(err, client.ErrDisabled):
		return fmt.Errorf("%w: enable %s in %s", err, s.Name, s.Config)
	}
	return err
}

// mcpStore is o-mcp's data directory (its saved schemas).
func mcpStore() client.Store {
	home, _ := os.UserHomeDir()
	return client.DefaultStore(home, os.Getenv)
}
