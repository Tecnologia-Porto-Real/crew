package main

// Local state per repo, under the plugin data dir. Several hooks and sessions run at once,
// so every read-modify-write goes through a lock file and atomic renames.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// dataRoot is the same for every agent (Claude Code, Codex…), so hooks and the MCP server of any agent on
// this machine share one device identity and one view of the room.
func dataRoot() string {
	if d := os.Getenv("CREW_DATA"); d != "" {
		return d
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".crew")
	}
	if d, err := os.UserConfigDir(); err == nil && d != "" {
		return filepath.Join(d, "crew")
	}
	return filepath.Join(os.TempDir(), "crew-"+strconv.Itoa(os.Getuid())) // never a relative path inside the repo
}

func repoDir(r *Repo) string {
	h := sha256.Sum256([]byte(r.Root + "|" + r.Path))
	return filepath.Join(dataRoot(), "repos", hex.EncodeToString(h[:8]))
}

func readJSON(p string, v any) bool {
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

func writeJSON(p string, v any) error {
	os.MkdirAll(filepath.Dir(p), 0o700)
	b, _ := json.MarshalIndent(v, "", " ")
	tmp := p + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	var err error
	for i := 0; i < 40; i++ { // Windows refuses rename while another process reads the file
		if err = os.Rename(tmp, p); err == nil {
			return nil
		}
		time.Sleep(15 * time.Millisecond)
	}
	os.Remove(tmp)
	return err
}

// withLock runs fn holding an exclusive lock file; a lock older than 20 s is considered abandoned.
func withLock(p string, fn func() error) error {
	os.MkdirAll(filepath.Dir(p), 0o700)
	deadline := time.Now().Add(8 * time.Second)
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.WriteString(strconv.Itoa(os.Getpid()))
			f.Close()
			defer os.Remove(p)
			return fn()
		}
		if st, e := os.Stat(p); e == nil && time.Since(st.ModTime()) > 20*time.Second {
			os.Remove(p)
			continue
		}
		if time.Now().After(deadline) {
			return errors.New("lock timeout")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// ---------- data model ----------

type Reservation struct {
	Path      string `json:"path"`
	Exclusive bool   `json:"exclusive,omitempty"`
	Until     int64  `json:"until"`
	Reason    string `json:"reason,omitempty"`
	Auto      bool   `json:"auto,omitempty"`
}

type Touch struct {
	Path string `json:"path"`
	At   int64  `json:"at"`
}

type State struct {
	Task         string        `json:"task,omitempty"`
	Branch       string        `json:"branch,omitempty"`
	Status       string        `json:"status"` // active | left
	Touched      []Touch       `json:"touched,omitempty"`
	Reservations []Reservation `json:"reservations,omitempty"`
}

// Envelope is the encrypted payload of every crew event.
type Envelope struct {
	V     int      `json:"v"`
	Type  string   `json:"type"` // state | notice | question | note | done | push
	Name  string   `json:"name"`
	Ts    int64    `json:"ts"`
	To    string   `json:"to,omitempty"`
	Text  string   `json:"text,omitempty"`
	Files []string `json:"files,omitempty"`
	State *State   `json:"state,omitempty"`
}

type Peer struct {
	Pub   string `json:"pub"`
	Name  string `json:"name"`
	At    int64  `json:"at"` // created_at of the latest state
	State State  `json:"state"`
}

type Item struct {
	Seq      int64    `json:"seq"`
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	From     string   `json:"from"` // Name#fp
	To       string   `json:"to,omitempty"`
	Text     string   `json:"text"`
	Files    []string `json:"files,omitempty"`
	At       int64    `json:"at"`
	Wake     bool     `json:"wake,omitempty"`     // worth waking an idle session
	Relevant bool     `json:"relevant,omitempty"` // worth showing mid-task (else only on the board)
}

type Spool struct {
	Seq   int64           `json:"seq"`
	Items []Item          `json:"items"`
	IDs   map[string]bool `json:"ids"`
	Since int64           `json:"since"` // newest created_at ingested
}

type Me struct {
	Task          string        `json:"task,omitempty"`
	Touched       []Touch       `json:"touched,omitempty"`
	Reservations  []Reservation `json:"reservations,omitempty"`
	LastPublished int64         `json:"lastPublished"`
	LastFetch     int64         `json:"lastFetch"`
	Dirty         bool          `json:"dirty"`
}

type Session struct {
	Seen  int64 `json:"seen"`
	Start int64 `json:"start"`
}

func (c *Ctx) path(name string) string { return filepath.Join(c.Dir, name) }

func (c *Ctx) loadPeers() map[string]Peer {
	m := map[string]Peer{}
	readJSON(c.path("peers.json"), &m)
	return m
}

func (c *Ctx) loadSpool() Spool {
	s := Spool{IDs: map[string]bool{}}
	readJSON(c.path("spool.json"), &s)
	if s.IDs == nil {
		s.IDs = map[string]bool{}
	}
	return s
}

func (c *Ctx) saveSpool(s Spool) {
	if len(s.Items) > 300 {
		s.Items = s.Items[len(s.Items)-300:]
	}
	if len(s.IDs) > 3000 {
		keep := map[string]bool{}
		for _, it := range s.Items {
			keep[it.ID] = true
		}
		s.IDs = keep
	}
	writeJSON(c.path("spool.json"), s)
}

func (c *Ctx) loadMe() Me {
	var m Me
	readJSON(c.path("me.json"), &m)
	return m
}

func (c *Ctx) updateMe(fn func(*Me)) Me {
	var m Me
	if err := withLock(c.path("me.lock"), func() error {
		m = c.loadMe()
		fn(&m)
		return writeJSON(c.path("me.json"), m)
	}); err != nil {
		return c.loadMe() // couldn't lock: keep what is on disk, never publish an empty state
	}
	return m
}

func sessionFile(c *Ctx, sid string) string {
	if sid == "" {
		sid = "none"
	}
	clean := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '_'
	}, sid)
	return c.path(filepath.Join("sessions", clean+".json"))
}
