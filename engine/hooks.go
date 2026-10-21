package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// agent: which coding agent runs the hook ("claude" or "codex"). Codex has no "ask" decision for PreToolUse
// and no way to wake an idle session, so a few behaviors differ.
var agent = "claude"

// rememberActiveRepo lets the MCP server (which Codex starts outside the project) find the repo in use.
func rememberActiveRepo(cwd string) {
	if cwd == "" {
		return
	}
	writeJSON(filepath.Join(dataRoot(), "active.json"), map[string]any{"cwd": cwd, "at": time.Now().Unix()})
}

// filesInPatch lists the files touched by a Codex apply_patch payload.
func filesInPatch(patch string) []string {
	var out []string
	for _, l := range strings.Split(patch, "\n") {
		l = strings.TrimSpace(l)
		for _, p := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: ", "*** Move to: "} {
			if strings.HasPrefix(l, p) {
				out = append(out, strings.TrimSpace(strings.TrimPrefix(l, p)))
			}
		}
	}
	return out
}

type HookIn struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Source    string `json:"source"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Command      string `json:"command"`
	} `json:"tool_input"`
}

func readHookInput() HookIn {
	var in HookIn
	b, _ := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	json.Unmarshal(b, &in)
	if in.Cwd == "" {
		in.Cwd = os.Getenv("CLAUDE_PROJECT_DIR")
	}
	if in.Cwd == "" {
		in.Cwd, _ = os.Getwd()
	}
	return in
}

func emit(v any) { os.Stdout.Write(marshalNoEscape(v)) }

func addContext(event, text string) {
	if text == "" {
		return
	}
	emit(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": text}})
}

func withTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// dispatch runs a hook or command. Hooks never fail loudly: any problem means "say nothing".
func dispatch(cmd string, args []string) int {
	for _, v := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_PREFIX", "GIT_INDEX_FILE"} {
		os.Unsetenv(v) // git exports these to aliases; our own git calls must follow cmd.Dir
	}
	switch cmd {
	case "hook":
		if len(args) == 0 {
			return 0
		}
		for i, a := range args {
			if a == "--agent" && i+1 < len(args) {
				agent = args[i+1]
			}
		}
		defer func() { recover() }()
		in := readHookInput()
		rememberActiveRepo(in.Cwd)
		switch args[0] {
		case "session-start":
			hookSessionStart(in)
		case "prompt":
			hookPrompt(in)
		case "pre-edit":
			hookPreEdit(in)
		case "post-edit":
			hookPostEdit(in)
		case "pre-bash":
			hookPreBash(in)
		case "pre-push":
			hookPrePush(in)
		case "post-push":
			hookPostPush(in)
		case "session-end":
			hookSessionEnd(in)
		case "watch":
			return hookWatch(in)
		}
		return 0
	case "mcp":
		return serveMCP()
	default:
		return cli(cmd, args)
	}
}

// ---------- SessionStart ----------

func hookSessionStart(in HookIn) {
	c, why := load(in.Cwd, true)
	if c == nil {
		if strings.HasPrefix(why, "public repository") || strings.HasPrefix(why, "shallow clone") {
			emit(map[string]any{"systemMessage": "crew: off here — " + why})
		}
		return
	}
	ctx, cancel := withTimeout(6 * time.Second)
	defer cancel()
	online := true
	done := make(chan bool, 1)
	go func() { c.publishState(ctx, "active"); done <- true }()
	c.fetch(ctx)
	if ctx.Err() != nil {
		online = false
	}
	select {
	case <-done:
	case <-ctx.Done():
	}

	// unread = arrived since any session last saw the room
	var read struct{ Seq int64 }
	readJSON(c.path("read.json"), &read)
	sp := c.loadSpool()
	var unread []string
	// before new work starts, every contract change counts; questions and answers only if they are for us
	for _, it := range sp.Items {
		if it.Seq <= read.Seq {
			continue
		}
		switch it.Type {
		case "push", "notice", "reservation":
		case "question", "answer":
			if !it.Relevant {
				continue
			}
		default:
			continue
		}
		unread = append(unread, formatItem(it))
	}
	writeJSON(c.path("read.json"), map[string]int64{"Seq": sp.Seq})
	c.startSession(in.SessionID)

	out := map[string]any{}
	// context is a budget spent on every turn: with no teammate and nothing unread the model gets no text at all
	peers := c.activePeers()
	if len(peers) > 0 || len(unread) > 0 {
		text := c.boardBrief(peers)
		if len(unread) > 5 {
			unread = unread[len(unread)-5:]
		}
		if len(unread) > 0 {
			text += strings.Join(unread, "\n") + "\n"
		}
		if !online {
			text += "(relays unreachable; board may be stale)\n"
		}
		out["hookSpecificOutput"] = map[string]any{"hookEventName": "SessionStart", "additionalContext": text}
	}
	if in.Source == "startup" || in.Source == "" {
		out["systemMessage"] = c.humanLine(peers, online)
	}
	if len(out) > 0 {
		emit(out)
	}
}

func (c *Ctx) humanLine(peers []Peer, online bool) string {
	if !online {
		return fmt.Sprintf("crew: you are %s in the %s room — could not reach the relays (offline or WebSockets blocked?), so the board may be stale. /crew:off turns it off here.", c.Me, c.Repo.Path)
	}
	if len(peers) == 0 {
		return fmt.Sprintf("crew: you are %s in the %s room — no teammates yet; they appear when they use crew (Claude Code or Codex) in this repo. %s /crew:off turns it off here.", c.Me, c.Repo.Path, c.relayNote())
	}
	var who []string
	for i, p := range peers {
		if i == 4 {
			who = append(who, fmt.Sprintf("+%d", len(peers)-4))
			break
		}
		st := "active"
		if p.State.Status == "left" || time.Since(time.Unix(p.At, 0)) > activeFor {
			st = ago(p.At)
		}
		who = append(who, fmt.Sprintf("%s#%s (%s)", p.Name, fp(p.Pub), st))
	}
	return fmt.Sprintf("crew: %s room — %s. %s /crew:off turns it off here.", c.Repo.Path, strings.Join(who, ", "), c.relayNote())
}

func (c *Ctx) relayNote() string {
	if !c.Pinned {
		return "Encrypted via public Nostr relays (they see IP and timing)."
	}
	var hosts []string
	for _, u := range c.Relays {
		hosts = append(hosts, strings.TrimPrefix(strings.TrimPrefix(u, "wss://"), "ws://"))
	}
	return "Encrypted via relays pinned in this repo's .claude/crew.json (" + strings.Join(hosts, ", ") + "), which see IP and timing."
}

// ---------- UserPromptSubmit (local only) ----------

func hookPrompt(in HookIn) {
	c, _ := load(in.Cwd, false)
	if c == nil {
		return
	}
	addContext("UserPromptSubmit", deliver(c, in.SessionID))
}

func deliver(c *Ctx, sid string) string {
	var l []string
	for _, it := range c.pending(sid, true) {
		if it.Relevant || it.Type == "reservation" {
			l = append(l, formatItem(it))
		}
	}
	if len(l) == 0 {
		return ""
	}
	if len(l) > 5 {
		l = l[len(l)-5:]
	}
	return dataNote + "\n" + strings.Join(l, "\n")
}

// ---------- PreToolUse on edits (local only) ----------

func relPath(c *Ctx, cwd, file string) string {
	if file == "" {
		return ""
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(cwd, file)
	}
	rel, err := filepath.Rel(c.Repo.Top, file)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return norm(rel)
}

func hookPreEdit(in HookIn) {
	c, _ := load(in.Cwd, false)
	if c == nil {
		return
	}
	files := []string{in.ToolInput.FilePath, in.ToolInput.NotebookPath}
	if in.ToolName == "apply_patch" || strings.Contains(in.ToolInput.Command, "*** Begin Patch") {
		files = filesInPatch(in.ToolInput.Command) // Codex edits arrive as one patch touching several files
	}
	var notes []string
	var ask string
	for _, file := range files {
		if file == "" {
			continue
		}
		rel := relPath(c, in.Cwd, file)
		if rel == "" || c.untracked(rel) {
			continue // ignored and generated files are nobody's business
		}
		for _, p := range c.activePeers() {
			who := p.Name + "#" + fp(p.Pub)
			for _, r := range liveReservations(p.State.Reservations) {
				if !match(rel, r.Path) {
					continue
				}
				if r.Exclusive && ask == "" && !c.overridden(rel) {
					ask = fmt.Sprintf("crew: %s reserved %s (exclusive, until %s). Parallel edits may break their work.",
						who, cleanIncoming(r.Path, 120), time.Unix(r.Until, 0).Format("15:04"))
				} else if !r.Exclusive && c.warnOnce(rel+"|"+p.Pub) {
					notes = append(notes, fmt.Sprintf("crew: %s is also editing %s. FYI, no reply needed.", who, rel))
				}
			}
		}
		// the edit itself is the lease: 1 h, renewed on every edit; contract files are exclusive
		exclusive := c.contractZone(rel)
		c.updateMe(func(m *Me) {
			now := time.Now().Unix()
			m.Touched = append(recentTouches(m.Touched, 8*time.Hour), Touch{Path: rel, At: now})
			m.Reservations = liveReservations(m.Reservations)
			found := false
			for i := range m.Reservations {
				if m.Reservations[i].Path == rel {
					m.Reservations[i].Until = now + int64(leaseTTL.Seconds())
					m.Reservations[i].Exclusive = m.Reservations[i].Exclusive || exclusive
					found = true
				}
			}
			if !found && !coveredBy(rel, m.Reservations) {
				m.Reservations = append(m.Reservations, Reservation{Path: rel, Exclusive: exclusive, Until: now + int64(leaseTTL.Seconds()), Auto: true})
			}
			m.Dirty = true
		})
	}
	if d := deliver(c, in.SessionID); d != "" {
		notes = append(notes, d)
	}
	emitPreToolUse(ask, notes)
}

// emitPreToolUse: Claude Code asks the human itself; Codex has no "ask" decision, so the edit is denied
// with an explanation and Claude-less agents use crew_override once their human agrees.
func emitPreToolUse(ask string, notes []string) {
	out := map[string]any{"hookEventName": "PreToolUse"}
	if ask != "" {
		if agent == "codex" {
			ask += " Blocked: ask your human; if they agree, crew_send type override with that file, then retry."
			out["permissionDecision"] = "deny"
		} else {
			out["permissionDecision"] = "ask"
		}
		out["permissionDecisionReason"] = ask
		notes = append([]string{"[" + ask + "]"}, notes...)
	}
	if len(notes) > 0 {
		out["additionalContext"] = strings.Join(notes, "\n")
	}
	if len(out) > 1 {
		emit(map[string]any{"hookSpecificOutput": out})
	}
}

// overridden: the human explicitly agreed to edit this path despite a teammate's exclusive reservation.
func (c *Ctx) overridden(rel string) bool {
	m := map[string]int64{}
	readJSON(c.path("overrides.json"), &m)
	for p, until := range m {
		if until > time.Now().Unix() && (p == rel || match(rel, p)) {
			return true
		}
	}
	return false
}

func (c *Ctx) warnOnce(key string) bool {
	ok := false
	withLock(c.path("warned.lock"), func() error {
		m := map[string]int64{}
		readJSON(c.path("warned.json"), &m)
		if time.Since(time.Unix(m[key], 0)) > 10*time.Minute {
			m[key] = time.Now().Unix()
			ok = true
			for k, v := range m {
				if time.Since(time.Unix(v, 0)) > 24*time.Hour {
					delete(m, k)
				}
			}
			writeJSON(c.path("warned.json"), m)
		}
		return nil
	})
	return ok
}

// ---------- PreToolUse on Bash (local only): writes into a teammate's exclusive reservation ----------

var (
	reWriteCmd = regexp.MustCompile(`(^|[\s;&|(])(touch|rm|mv|cp|mkdir|rmdir|tee|truncate|install|ln|chmod|dd|patch|unzip|tar)\b|(^|[^0-9&])>{1,2}\s*[^&\s]|\bsed\s+(-[a-z]*i|--in-place)|\bgit\s+(mv|rm|checkout|restore|apply|stash)\b|\b(Set-Content|Add-Content|Out-File|New-Item|Remove-Item|Move-Item|Copy-Item)\b`)
	// tools that create migration files without naming the path
	reMigrationCmd = regexp.MustCompile(`\bmigration\s+new\b|\bmigrate\s+(dev|create|diff)\b|\bmakemigrations\b|\balembic\s+revision\b|\bdrizzle-kit\s+generate\b|\bdb:generate\b|\bgenerate:migration\b`)
	reTokenSplit   = regexp.MustCompile("[\\s;&|<>()`'\"=]+")
)

func hookPreBash(in HookIn) {
	cmd := in.ToolInput.Command
	if strings.TrimSpace(cmd) == "" {
		return
	}
	c, _ := load(in.Cwd, false)
	if c == nil {
		return
	}
	writes := reWriteCmd.MatchString(cmd)
	migration := reMigrationCmd.MatchString(cmd)
	var notes []string
	var ask string
	if writes || migration {
		// candidate paths named in the command, relative to the repo
		var paths []string
		for _, tok := range reTokenSplit.Split(cmd, -1) {
			tok = strings.TrimSpace(tok)
			if tok == "" || strings.HasPrefix(tok, "-") || !strings.ContainsAny(tok, "/\\.") {
				continue
			}
			if rel := relPath(c, in.Cwd, tok); rel != "" {
				paths = append(paths, rel)
			}
		}
		if migration {
			paths = append(paths, "supabase/migrations/new.sql", "migrations/new.sql", "prisma/migrations/new/migration.sql", "db/migrations/new.sql", "drizzle/new.sql", "alembic/versions/new.py")
		}
	outer:
		for _, p := range c.activePeers() {
			who := p.Name + "#" + fp(p.Pub)
			for _, r := range liveReservations(p.State.Reservations) {
				if !r.Exclusive {
					continue
				}
				for _, path := range paths {
					if (match(path, r.Path) || match(r.Path, path)) && !c.overridden(path) && !c.overridden(r.Path) {
						ask = fmt.Sprintf("crew: this command changes files under %s, which %s reserved exclusively until %s%s. Approve only if you agreed with them.",
							cleanIncoming(r.Path, 120), who, time.Unix(r.Until, 0).Format("15:04"), reasonSuffix(r.Reason))
						break outer
					}
				}
			}
		}
	}
	if rePush.MatchString(cmd) {
		recordPrePush(c) // Codex has no `if` filter on hooks, so pushes are recognized here too
	}
	if d := deliver(c, in.SessionID); d != "" {
		notes = append(notes, d)
	}
	emitPreToolUse(ask, notes)
}

// ---------- PostToolUse on edits (async: network allowed) ----------

func hookPostEdit(in HookIn) {
	c, _ := load(in.Cwd, false)
	if c == nil {
		return
	}
	// one network round at a time per repo; skip if another hook is already doing it
	f, err := os.OpenFile(c.path("net.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if st, e := os.Stat(c.path("net.lock")); e == nil && time.Since(st.ModTime()) > time.Minute {
			os.Remove(c.path("net.lock"))
		}
		return
	}
	f.Close()
	defer os.Remove(c.path("net.lock"))
	ctx, cancel := withTimeout(20 * time.Second)
	defer cancel()
	c.maybePublish(ctx)
	if time.Since(time.Unix(c.loadMe().LastFetch, 0)) > 30*time.Second {
		c.fetch(ctx)
	}
}

// ---------- git push → automatic notice to the team ----------

var rePush = regexp.MustCompile(`\bgit\b[^|;&]*\bpush\b`)

func hookPrePush(in HookIn) {
	if !rePush.MatchString(in.ToolInput.Command) {
		return
	}
	c, _ := load(in.Cwd, false)
	if c == nil {
		return
	}
	recordPrePush(c)
}

func recordPrePush(c *Ctx) {
	if sha, err := git(c.Repo.Top, "rev-parse", "@{u}"); err == nil {
		writeJSON(c.path("prepush.json"), map[string]any{"sha": sha, "at": time.Now().Unix()})
	}
}

func hookPostPush(in HookIn) {
	if !rePush.MatchString(in.ToolInput.Command) {
		return
	}
	c, _ := load(in.Cwd, false)
	if c == nil {
		return
	}
	var pre struct {
		Sha string `json:"sha"`
		At  int64  `json:"at"`
	}
	if !readJSON(c.path("prepush.json"), &pre) || time.Since(time.Unix(pre.At, 0)) > 30*time.Minute {
		return
	}
	os.Remove(c.path("prepush.json"))
	now, err := git(c.Repo.Top, "rev-parse", "@{u}")
	if err != nil || now == pre.Sha {
		return
	}
	author := []string{}
	if e := myEmail(c.Repo.Top); e != "" {
		author = []string{"--author=" + e}
	}
	rng := pre.Sha + ".." + now
	commits, _ := git(c.Repo.Top, append(append([]string{"log", "--format=%s"}, author...), rng)...)
	if strings.TrimSpace(commits) == "" {
		return
	}
	filesOut, _ := git(c.Repo.Top, append(append([]string{"log", "--name-only", "--format="}, author...), rng)...)
	seen := map[string]bool{}
	var files, contract []string
	for _, f := range strings.Split(filesOut, "\n") {
		f = strings.TrimSpace(f)
		if f != "" && !seen[f] {
			seen[f] = true
			files = append(files, f)
			if c.contractZone(f) {
				contract = append(contract, f)
			}
		}
	}
	ctx, cancel := withTimeout(10 * time.Second)
	defer cancel()
	// only contract changes are news to teammates; other pushes just release this device's leases
	if len(contract) > 0 {
		subj := strings.Split(strings.TrimSpace(commits), "\n")
		branch, _ := git(c.Repo.Top, "rev-parse", "--abbrev-ref", "@{u}")
		text := fmt.Sprintf("pushed %s to %s (%s) — pull before building on them", strings.Join(trimList(contract, 4), ", "), branch, subj[0])
		text = redact(text)
		if r := []rune(text); len(r) > maxText {
			text = string(r[:maxText-1]) + "…"
		}
		c.sendMsg(ctx, "push", text, "", trimList(contract, 20), false)
	}
	c.updateMe(func(m *Me) {
		keep := []Reservation{}
		for _, r := range liveReservations(m.Reservations) {
			if !(r.Auto && seen[r.Path]) {
				keep = append(keep, r)
			}
		}
		m.Reservations = keep
	})
	c.publishState(ctx, "active")
}

func trimList(l []string, n int) []string {
	if len(l) <= n {
		return l
	}
	return append(append([]string{}, l[:n]...), fmt.Sprintf("+%d more", len(l)-n))
}

// ---------- SessionEnd ----------

func hookSessionEnd(in HookIn) {
	c, _ := load(in.Cwd, false)
	if c == nil {
		return
	}
	// the session's watcher notices the missing session file and exits within 30 s; we never kill by pid,
	// because a recycled pid could belong to an unrelated program
	os.Remove(sessionFile(c, in.SessionID))
	ents, _ := os.ReadDir(c.path("sessions"))
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".json") {
			return // another session of mine is still open in this repo
		}
	}
	ctx, cancel := withTimeout(1400 * time.Millisecond)
	defer cancel()
	c.publishState(ctx, "left")
}

// ---------- Stop + asyncRewake: wait for the team, wake Claude only when it matters ----------

func (c *Ctx) watchPidFile(sid string) string {
	return strings.TrimSuffix(sessionFile(c, sid), ".json") + ".watch"
}

func hookWatch(in HookIn) int {
	c, _ := load(in.Cwd, false)
	if c == nil {
		return 0
	}
	sid := in.SessionID
	pidFile := c.watchPidFile(sid)
	// another live watcher for this session refreshes its file every 30 s; a stale file with a recycled
	// pid must not keep this session deaf
	var w struct{ Pid int }
	if st, err := os.Stat(pidFile); err == nil && time.Since(st.ModTime()) < 90*time.Second &&
		readJSON(pidFile, &w) && w.Pid != os.Getpid() && pidAlive(w.Pid) {
		return 0
	}
	writeJSON(pidFile, map[string]int{"Pid": os.Getpid()})
	defer func() {
		var cur struct{ Pid int }
		if readJSON(pidFile, &cur) && cur.Pid == os.Getpid() {
			os.Remove(pidFile)
		}
	}()
	c.pending(sid, false) // registers the session if unknown

	if hasWake(c.pending(sid, false)) && c.allowWake(sid) {
		return wakeNow(c, sid)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 23*time.Hour)
	defer cancel()
	go func() {
		pctx, pc := withTimeout(15 * time.Second)
		c.publishState(pctx, "active")
		pc()
	}()

	signal := make(chan struct{}, 1)
	go subscribe(ctx, c.Relays, func() Filter { return c.filter(c.since()) }, func(ev Event) {
		c.ingest([]Event{ev})
		if hasWake(c.pending(sid, false)) {
			select {
			case signal <- struct{}{}:
			default:
			}
		}
	})

	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	lastBeat := time.Now()
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-signal:
			time.Sleep(3 * time.Second) // gather what arrives together into one wake
			if c.allowWake(sid) {
				return wakeNow(c, sid)
			}
		case <-tick.C:
			if sid != "" && !c.sessionOpen(sid) {
				return 0
			}
			now := time.Now()
			os.Chtimes(pidFile, now, now)
			if time.Since(lastBeat) > 10*time.Minute {
				lastBeat = time.Now()
				pctx, pc := withTimeout(15 * time.Second)
				c.publishState(pctx, "active")
				pc()
			}
		}
	}
}

func hasWake(items []Item) bool {
	for _, it := range items {
		if it.Wake {
			return true
		}
	}
	return false
}

// allowWake: at most wakesPerHr wakes per session per hour.
func (c *Ctx) allowWake(sid string) bool {
	p := strings.TrimSuffix(sessionFile(c, sid), ".json") + ".wakes"
	var l []int64
	readJSON(p, &l)
	cut := time.Now().Add(-time.Hour).Unix()
	keep := []int64{}
	for _, t := range l {
		if t > cut {
			keep = append(keep, t)
		}
	}
	if len(keep) >= wakesPerHr || len(keep) > 0 && time.Since(time.Unix(keep[len(keep)-1], 0)) < wakeGap {
		return false
	}
	writeJSON(p, append(keep, time.Now().Unix()))
	return true
}

func wakeNow(c *Ctx, sid string) int {
	if agent == "codex" {
		// Codex can't be woken from outside: tell the human with a desktop notification and leave the
		// messages pending, so they reach Codex with the next prompt. The watcher ends here and is re-armed
		// after the next turn, so the same news never notifies twice.
		for _, it := range c.pending(sid, false) {
			if it.Wake {
				notify("crew: "+it.From, formatItem(it))
				break
			}
		}
		return 0
	}
	text := deliver(c, sid)
	if text == "" {
		return 0
	}
	// nobody typed anything and the human may be away: the woken turn reports, it doesn't start work
	fmt.Fprintln(os.Stderr, "[crew] Team update that arrived while this session was idle; nobody typed anything. Start the reply with \"📨 Team update (crew)\" and tell your human in up to 3 lines. Don't edit files or run commands because of it; reply with crew_send only facts you already know — decisions go to your human:")
	fmt.Fprintln(os.Stderr, text)
	return 2
}

// ---------- settings shared by MCP and CLI ----------

func setOff(cwd string, off bool) string {
	r, why := openRepo(cwd)
	if r == nil {
		return "crew can't run here: " + why
	}
	p := filepath.Join(repoDir(r), "off")
	if off {
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o600)
		return "crew is now off for " + r.Path + " on this machine. Use /crew:on to turn it back on."
	}
	os.Remove(p)
	return "crew is on for " + r.Path + "."
}

func setPassphrase(cwd, pass string) string {
	r, why := openRepo(cwd)
	if r == nil {
		return "crew can't run here: " + why
	}
	if len([]rune(pass)) < 12 {
		return "use a passphrase of at least 12 characters, shared with the team outside the repository"
	}
	p := filepath.Join(repoDir(r), "pass.key")
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte(fmt.Sprintf("%x", passphraseKey(pass, r.Path))), 0o600)
	return "Team passphrase saved on this machine for " + r.Path + ". Every teammate must set the same passphrase."
}

func clearPassphrase(cwd string) string {
	r, why := openRepo(cwd)
	if r == nil {
		return "crew can't run here: " + why
	}
	os.Remove(filepath.Join(repoDir(r), "pass.key"))
	return "Team passphrase removed for " + r.Path + "."
}

func newRoomKey(cwd string) string {
	r, why := openRepo(cwd)
	if r == nil {
		return "crew can't run here: " + why
	}
	dir := filepath.Join(r.Top, ".claude")
	p := filepath.Join(dir, "crew.key")
	// never follow symlinks a repository may plant to make us overwrite another file
	for _, x := range []string{dir, p} {
		if st, err := os.Lstat(x); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return "refusing to write: " + x + " is a symbolic link"
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "could not create .claude: " + err.Error()
	}
	tmp := filepath.Join(dir, ".crew.key."+randomHex(4)+".tmp")
	if err := os.WriteFile(tmp, []byte(randomHex(32)+"\n"), 0o644); err != nil {
		return "could not write .claude/crew.key: " + err.Error()
	}
	os.Remove(p)
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return "could not write .claude/crew.key: " + err.Error()
	}
	return "Created .claude/crew.key. Commit and push it: teammates join the new room after they pull, and people without access to the repo lose access to new messages."
}
