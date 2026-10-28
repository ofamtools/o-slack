package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// o-slack's own data, in its data directory ($O_SLACK_DATA, else
// $XDG_DATA_HOME/o-slack, else ~/.local/share/o-slack):
//
//	state.json     threads watched and ignored, channel marks (watch, ignore, on-call)
//	poll.json      the last poll (the page's rows come from it while fresh)
//	channels.json  the last channels read
//	signals.json   what the last o-slack signals run saw, and signals not yet sent
//	lock           flock'd around a poll and each file's read-modify-write
//
// Every write is a temp file renamed into place. No token or credential is
// ever written here.

const (
	stateFile    = "state.json"
	pollFile     = "poll.json"
	channelsFile = "channels.json"
	signalsFile  = "signals.json"
	lockFile     = "lock"
)

func dataDir() string {
	if d := os.Getenv("O_SLACK_DATA"); d != "" {
		return d
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "o-slack")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "o-slack")
	}
	return filepath.Join(home, ".local", "share", "o-slack")
}

// Mark is what o-slack recorded of a thread or channel when it was marked:
// enough to show a row before (or without) a poll answering it.
type Mark struct {
	Ref   string    `json:"ref"`
	Label string    `json:"label,omitempty"`
	URL   string    `json:"url,omitempty"`
	Auto  bool      `json:"auto,omitempty"` // a thread watched because you were mentioned in it
	At    time.Time `json:"at"`
}

// State is o-slack's own marks. A thread is in at most one of Threads,
// IgnoredThreads and Unwatched; a channel in at most one of Watched and Ignored, and
// OnCall independently.
type State struct {
	Threads        map[string]Mark `json:"threads"`         // CHANNEL/TS
	IgnoredThreads map[string]Mark `json:"ignored_threads"` // CHANNEL/TS
	Watched        map[string]Mark `json:"watched_channels"`
	Ignored        map[string]Mark `json:"ignored_channels"`
	OnCall         map[string]Mark `json:"oncall_channels"`
	Unwatched      map[string]Mark `json:"unwatched_threads"` // unwatched by hand: never auto-watched again
}

func (st *State) init() {
	for _, m := range []*map[string]Mark{&st.Threads, &st.IgnoredThreads, &st.Watched, &st.Ignored, &st.OnCall, &st.Unwatched} {
		if *m == nil {
			*m = map[string]Mark{}
		}
	}
}

func newState() State { var st State; st.init(); return st }

func keys(m map[string]Mark) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Store is the data directory.
type Store struct{ Dir string }

func (s Store) path(name string) string { return filepath.Join(s.Dir, name) }

func (s Store) locked(fn func() error) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path(lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", s.path(lockFile), err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

func (s Store) read(name string, v any) (bool, error) {
	b, err := os.ReadFile(s.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("%s: %w", s.path(name), err)
	}
	return true, nil
}

func (s Store) write(name string, v any) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, "."+name+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), s.path(name))
}

// State reads state.json.
func (s Store) State() (State, error) {
	var st State
	_, err := s.read(stateFile, &st)
	st.init()
	return st, err
}

// Change edits state.json under the lock.
func (s Store) Change(edit func(*State) error) (State, error) {
	var st State
	err := s.locked(func() error {
		var err error
		if st, err = s.State(); err != nil {
			return err
		}
		if err := edit(&st); err != nil {
			return err
		}
		return s.write(stateFile, st)
	})
	return st, err
}

// Snapshot is what the last signals run saw: every message key it read
// (with its ts, for pruning), signals that failed to send, and when the
// last complete poll ran (the next one reads from there).
type Snapshot struct {
	At      time.Time         `json:"at"`
	Seen    map[string]string `json:"seen"`
	Pending []Signal          `json:"pending,omitempty"`
}
