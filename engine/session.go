package main

// Sessions: one device often runs several agent sessions in the same repo (and subagents inside them).
// Each one gets its own identity in the room (Name#sess), its own scope and leases, and its own routing:
// a question for a person reaches the one session that can act on it, not all of them.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// bind makes c speak for one session (and, inside it, one subagent). Every hook and tool call binds first.
func (c *Ctx) bind(sid, agentID string) {
	c.Sid, c.Agent = sid, agentID
	c.SessKey = cleanSid(sid) // the same name its session file has, so every path to a session agrees
	if agentID != "" {
		c.SessKey += "." + agentID
	}
	c.SessTag = c.tagOf(c.SessKey)
	c.ParentTag = c.tagOf(cleanSid(sid)) // a subagent also answers for its session
	c.Me = c.Name + "#" + c.SessTag
}

func (c *Ctx) tagOf(key string) string {
	h := sha256.Sum256([]byte("crew-sess|" + c.Keys.Pub + "|" + key))
	return hex.EncodeToString(h[:3])
}

// own: the item was sent by this session (or, in a subagent, by its session).
func (c *Ctx) own(it Item) bool {
	return c.MyPubs[it.FromPub] && (it.FromSess == c.SessTag || it.FromSess == c.ParentTag)
}

// activeFile records when the human last used a session. It is separate from the session file, which other
// hooks rewrite (pending, startSession) and would make the wrong session look recent.
func (c *Ctx) activeFile(name string) string {
	return c.path(filepath.Join("sessions", name+".active"))
}

// openSessions lists the open sessions of this device in this room, most recently active first.
func (c *Ctx) openSessions() []string {
	ents, _ := os.ReadDir(c.path("sessions"))
	type st struct {
		name string
		t    time.Time
	}
	var l []st
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".me.json") {
			continue
		}
		if inf, err := e.Info(); err == nil && time.Since(inf.ModTime()) < 72*time.Hour {
			name, t := strings.TrimSuffix(n, ".json"), time.Time{}
			if a, err := os.Stat(c.activeFile(name)); err == nil {
				t = a.ModTime()
			}
			l = append(l, st{name, t})
		}
	}
	sort.Slice(l, func(i, j int) bool { return l[i].t.After(l[j].t) })
	out := []string{}
	for _, s := range l {
		out = append(out, s.name)
	}
	return out
}

// primary: this session is the one of this device the human used last. Questions for a person (not a
// session) and file-less team news go to the primary session only, so one question costs one wake.
func (c *Ctx) primary() bool {
	open := c.openSessions()
	return len(open) == 0 || open[0] == strings.TrimSuffix(filepath.Base(sessionFile(c, c.Sid)), ".json")
}

// touchSession records activity (a prompt, a tool call) so routing knows which session the human is in.
func (c *Ctx) touchSession() {
	os.WriteFile(c.activeFile(cleanSid(c.Sid)), nil, 0o600)
}

// localSiblings: the other sessions (and subagents) of this device, read straight from disk — instant and
// offline. Their own events are never ingested back from the relays.
func (c *Ctx) localSiblings() []Peer {
	dir := c.path("sessions")
	ents, _ := os.ReadDir(dir)
	var out []Peer
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".me.json") {
			continue
		}
		var m Me
		inf, err := e.Info()
		if err != nil || time.Since(inf.ModTime()) > stateTTL || !readJSON(filepath.Join(dir, e.Name()), &m) {
			continue
		}
		if m.Sess == "" || m.Sess == c.SessTag || m.Sid == c.Sid || !m.hasState() {
			continue // a subagent and its session are one conversation, not siblings
		}
		if m.Agent != "" && time.Since(inf.ModTime()) > time.Hour {
			continue // a subagent that stopped working
		}
		if _, err := os.Stat(sessionFile(c, m.Sid)); err != nil {
			continue // that session ended
		}
		out = append(out, Peer{Pub: c.Keys.Pub, Sess: m.Sess, Who: c.Who, Name: c.Name, At: inf.ModTime().Unix(), Local: true,
			State: State{Task: m.Task, Status: "active", Touched: recentTouches(m.Touched, 8*time.Hour), Reservations: liveReservations(m.Reservations)}})
	}
	return out
}

// deviceFiles: what any session of this device touched or reserved, plus uncommitted changes.
func (c *Ctx) deviceFiles() []string {
	set := map[string]bool{}
	dir := c.path("sessions")
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		var m Me
		if strings.HasSuffix(e.Name(), ".me.json") && readJSON(filepath.Join(dir, e.Name()), &m) {
			for _, f := range filesOf(m) {
				set[f] = true
			}
		}
	}
	if out, err := git(c.Repo.Top, "status", "--porcelain", "-uno"); err == nil {
		for _, l := range strings.Split(out, "\n") {
			if len(l) > 3 {
				p := strings.TrimSpace(l[3:])
				if i := strings.Index(p, " -> "); i >= 0 {
					p = p[i+4:]
				}
				set[strings.Trim(p, `"`)] = true
			}
		}
	}
	out := []string{}
	for k := range set {
		out = append(out, k)
	}
	return out
}

func filesOf(m Me) []string {
	var out []string
	for _, t := range recentTouches(m.Touched, 8*time.Hour) {
		out = append(out, t.Path)
	}
	for _, r := range liveReservations(m.Reservations) {
		out = append(out, r.Path)
	}
	return out
}

// handle: how a peer is named in the room. Sessions carry their own id; crew < 0.5 only had the device's.
func handle(p Peer) string {
	if p.Sess != "" {
		return p.Name + "#" + p.Sess
	}
	return p.Name + "#" + fp(p.Pub)
}

// label adds who a peer really is when it is the user themself.
func (c *Ctx) label(p Peer) string {
	switch {
	case p.Local:
		return handle(p) + " (your other session)"
	case p.Who != "" && p.Who == c.Who:
		return handle(p) + " (you, on another device)"
	}
	return handle(p)
}

// addressed: 2 = to this session, 1 = to this person (any of their sessions), 0 = not to me.
func (c *Ctx) addressed(to string) int {
	t := strings.ToLower(strings.TrimSpace(to))
	switch {
	case t == "":
		return 0
	case t == strings.ToLower(c.Me) || strings.HasSuffix(t, "#"+c.SessTag) || strings.HasSuffix(t, "#"+c.ParentTag):
		return 2
	case t == strings.ToLower(c.Name) || strings.HasSuffix(t, "#"+fp(c.Keys.Pub)) || strings.TrimPrefix(t, "@") == strings.ToLower(c.Name):
		return 1
	}
	return 0
}

// view is what routing needs to know about the receiving session.
type view struct {
	mine    []string
	primary bool
	routed  map[string]string // item id → the session of this device it already went to
}

func (c *Ctx) view() view {
	v := view{mine: filesOf(c.loadMe()), primary: c.primary(), routed: map[string]string{}}
	if c.Agent != "" { // a subagent consumes its session's deliveries, so it judges them by the session's files too
		var parent Me
		readJSON(strings.TrimSuffix(sessionFile(c, c.Sid), ".json")+".me.json", &parent)
		v.mine = append(v.mine, filesOf(parent)...)
	}
	readJSON(c.path("routed.json"), &v.routed)
	return v
}

// first: this session is where the human is, and the item hasn't gone to another session of theirs yet.
func (c *Ctx) first(it Item, v view) bool {
	s := v.routed[it.ID]
	return v.primary && (s == "" || s == c.SessTag) || s == c.SessTag
}

// markRouted records that these items reached this session, so the person sees each one once.
func (c *Ctx) markRouted(items []Item) {
	if len(items) == 0 {
		return
	}
	withLock(c.path("routed.lock"), func() error {
		m := map[string]string{}
		readJSON(c.path("routed.json"), &m)
		for _, it := range items {
			if m[it.ID] == "" {
				m[it.ID] = c.SessTag
			}
		}
		if len(m) > 500 { // ids of expired messages; the spool keeps 300
			keep := map[string]string{}
			for _, it := range c.loadSpool().Items {
				if s, ok := m[it.ID]; ok {
					keep[it.ID] = s
				}
			}
			m = keep
		}
		return writeJSON(c.path("routed.json"), m)
	})
}

// overlaps: an item's files touch mine — the same file or glob, or two contract files in one folder
// (a new migration next to the one I'm writing is how migration numbers collide).
func (c *Ctx) overlaps(files, mine []string) bool {
	for _, f := range files {
		if firstMatch(mine, f) != "" {
			return true
		}
		if dir := path.Dir(norm(f)); dir != "." && c.Repo != nil && c.contractZone(f) {
			for _, m := range mine {
				if path.Dir(norm(m)) == dir && c.contractZone(m) {
					return true
				}
			}
		}
	}
	return false
}

// classify decides whether an item reaches this session mid-task (relevant) and whether it is worth a full
// model turn on an idle session (wake). Only things that change what this session should do qualify.
func (c *Ctx) classify(it Item, v view) (relevant, wake bool) {
	if c.own(it) {
		return false, false // my own message
	}
	if (it.Type == "question" || it.Type == "answer") && c.mine(it) && c.addressed(it.To) < 2 {
		return false, false // my other session asking "the team" or me: I'm the human who already knows
	}
	over := c.overlaps(it.Files, v.mine)
	switch it.Type {
	case "question":
		if it.To == "" {
			ok := over || len(it.Files) == 0 && c.first(it, v)
			return ok, ok
		}
		switch c.addressed(it.To) {
		case 2:
			return true, true
		case 1:
			ok := over || c.first(it, v)
			return ok, ok
		}
		return false, false
	case "answer":
		switch c.addressed(it.To) {
		case 2:
			return true, true
		case 1:
			ok := c.first(it, v)
			return ok, ok
		}
		return false, false
	case "notice", "push":
		if over {
			return true, true
		}
		return len(it.Files) == 0 && c.first(it, v), false // team-wide news (no files): mid-task, no wake
	case "reservation":
		if over {
			return true, true
		}
		return c.first(it, v), false
	}
	return false, false // legacy notes/status: board only
}

// mine: the item comes from another session of this device. A matching person hash only changes the
// label ("you, on another device"), never what gets through: a shared git account would hide a teammate.
func (c *Ctx) mine(it Item) bool { return c.MyPubs[it.FromPub] }

// direct: the item names this session or person, so it may wake sooner than general news.
func (c *Ctx) direct(it Item) bool {
	return (it.Type == "question" || it.Type == "answer") && c.addressed(it.To) > 0
}

// ---------- open questions: decisions waiting for a human ----------

// openQuestions: questions for this session/person or the team (last 24 h) that nobody has answered yet.
func (c *Ctx) openQuestions() []Item {
	sp := c.loadSpool()
	cut := time.Now().Add(-stateTTL).Unix()
	answered := func(q Item) bool {
		from := strings.ToLower(q.From)
		for _, it := range sp.Items {
			if it.Type == "answer" && it.At >= q.At && strings.ToLower(it.To) == from {
				return true
			}
		}
		for _, m := range c.allMe() {
			for _, s := range m.Sent {
				if s.Type == "answer" && s.At >= q.At && strings.ToLower(s.To) == from {
					return true
				}
			}
		}
		return false
	}
	var out []Item
	for _, it := range sp.Items {
		if it.Type != "question" || it.At < cut || c.mine(it) {
			continue
		}
		if it.To != "" && c.addressed(it.To) == 0 {
			continue
		}
		if !answered(it) {
			out = append(out, it)
		}
	}
	return out
}

func (c *Ctx) allMe() []Me {
	dir := c.path("sessions")
	ents, _ := os.ReadDir(dir)
	var out []Me
	for _, e := range ents {
		var m Me
		if strings.HasSuffix(e.Name(), ".me.json") && readJSON(filepath.Join(dir, e.Name()), &m) {
			out = append(out, m)
		}
	}
	return out
}

// ---------- tool calls → session (the MCP server isn't told which session calls it) ----------

type claim struct {
	Sid   string `json:"sid"`
	Agent string `json:"agent,omitempty"`
	Cwd   string `json:"cwd"`
	At    int64  `json:"at"`
}

// argHash identifies one tool call by tool name and canonical arguments. The PreToolUse hook sees the same
// call with its session id a moment before the MCP server receives it.
func argHash(tool string, raw json.RawMessage) string {
	if i := strings.LastIndex(tool, "__"); i >= 0 {
		tool = tool[i+2:]
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if dec.Decode(&v) != nil || v == nil {
		v = map[string]any{}
	}
	b, _ := json.Marshal(v)
	h := sha256.Sum256(append([]byte(tool+"|"), b...))
	return hex.EncodeToString(h[:12])
}

func saveClaim(tool string, raw json.RawMessage, cl claim) {
	p := filepath.Join(dataRoot(), "claims.json")
	withLock(p+".lock", func() error {
		m := map[string]claim{}
		readJSON(p, &m)
		for k, v := range m {
			if time.Since(time.Unix(v.At, 0)) > 2*time.Minute {
				delete(m, k)
			}
		}
		cl.At = time.Now().Unix()
		m[argHash(tool, raw)] = cl
		return writeJSON(p, m)
	})
}

func findClaim(tool string, raw json.RawMessage) (claim, bool) {
	m := map[string]claim{}
	readJSON(filepath.Join(dataRoot(), "claims.json"), &m)
	cl, ok := m[argHash(tool, raw)]
	return cl, ok && time.Since(time.Unix(cl.At, 0)) < 2*time.Minute
}

// ---------- local log of what crew told each session (hook text isn't kept in transcripts) ----------

type logEntry struct {
	At   int64  `json:"at"`
	Sess string `json:"sess"`
	Hook string `json:"hook"`
	Text string `json:"text"`
}

func (c *Ctx) logInjected(hook, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	p := c.path("log.jsonl")
	if st, err := os.Stat(p); err == nil && st.Size() > 512<<10 {
		os.Rename(p, c.path("log.1.jsonl"))
	}
	os.MkdirAll(filepath.Dir(p), 0o700)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(logEntry{At: time.Now().Unix(), Sess: c.SessTag, Hook: hook, Text: text})
	f.Write(append(b, '\n'))
}

// recentLog: the last n things crew injected, for this session (or all sessions when all is set).
func (c *Ctx) recentLog(n int, all bool) []logEntry {
	b, err := os.ReadFile(c.path("log.jsonl"))
	if err != nil {
		return nil
	}
	var out []logEntry
	for _, l := range strings.Split(string(b), "\n") {
		var e logEntry
		if json.Unmarshal([]byte(l), &e) == nil && e.At > 0 && (all || e.Sess == c.SessTag) {
			out = append(out, e)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}
