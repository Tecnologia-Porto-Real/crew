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
		defer func() { recover() }()
		in := readHookInput()
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
	for _, it := range sp.Items {
		if it.Seq > read.Seq {
			unread = append(unread, formatItem(it))
		}
	}
	if len(unread) > 10 {
		unread = unread[len(unread)-10:]
	}
	writeJSON(c.path("read.json"), map[string]int64{"Seq": sp.Seq})
	c.startSession(in.SessionID)

	text := c.board(1600)
	if len(unread) > 0 {
		text += "- Unread team messages:\n  " + strings.Join(unread, "\n  ") + "\n"
	}
	if !online {
		text += "- Could not reach the relays right now; the board may be out of date.\n"
	}
	out := map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "SessionStart", "additionalContext": text}}
	if in.Source == "startup" || in.Source == "" {
		out["systemMessage"] = c.humanLine()
	}
	emit(out)
}

func (c *Ctx) humanLine() string {
	peers := c.activePeers()
	if len(peers) == 0 {
		return fmt.Sprintf("crew: you are %s in the %s room — no teammates yet; they appear when they use crew in this repo. /crew:off turns it off here.", c.Me, c.Repo.Path)
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
	return fmt.Sprintf("crew: %s room — %s. Encrypted via public Nostr relays (they see IP and timing). /crew:off turns it off here.", c.Repo.Path, strings.Join(who, ", "))
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
	items := c.pending(sid, true)
	if len(items) == 0 {
		return ""
	}
	if len(items) > 10 {
		items = items[len(items)-10:]
	}
	var l []string
	for _, it := range items {
		l = append(l, formatItem(it))
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
	file := in.ToolInput.FilePath
	if file == "" {
		file = in.ToolInput.NotebookPath
	}
	rel := relPath(c, in.Cwd, file)
	var notes []string
	var ask string
	if rel != "" {
		for _, p := range c.activePeers() {
			who := p.Name + "#" + fp(p.Pub)
			for _, r := range liveReservations(p.State.Reservations) {
				if !match(rel, r.Path) {
					continue
				}
				if r.Exclusive && ask == "" {
					ask = fmt.Sprintf("crew: %s reserved %s exclusively until %s%s. Editing it in parallel may break their work — approve only if you agreed with them.",
						who, cleanIncoming(r.Path, 120), time.Unix(r.Until, 0).Format("15:04"), reasonSuffix(r.Reason))
				} else if !r.Exclusive && c.warnOnce(rel+"|"+p.Pub) {
					notes = append(notes, fmt.Sprintf("[crew] %s is also working on %s (shared reservation%s). Parallel edits to the same file may conflict.", who, rel, reasonSuffix(r.Reason)))
				}
			}
			if ask == "" {
				for _, t := range recentTouches(p.State.Touched, 2*time.Hour) {
					if strings.EqualFold(t.Path, rel) && c.warnOnce(rel+"|t|"+p.Pub) {
						notes = append(notes, fmt.Sprintf("[crew] %s edited %s %s (possibly not pushed yet). Consider pulling or checking with them before changing it.", who, rel, ago(t.At)))
					}
				}
			}
		}
		// record what I'm editing; contract zones get an automatic exclusive reservation
		contract := c.contractZone(rel)
		c.updateMe(func(m *Me) {
			now := time.Now().Unix()
			m.Touched = append(recentTouches(m.Touched, 8*time.Hour), Touch{Path: rel, At: now})
			if contract {
				found := false
				for i := range m.Reservations {
					if m.Reservations[i].Path == rel {
						m.Reservations[i].Until = now + 7200
						found = true
					}
				}
				if !found {
					m.Reservations = append(liveReservations(m.Reservations), Reservation{Path: rel, Exclusive: true, Until: now + 7200, Auto: true, Reason: "contract file being changed"})
				}
			}
			m.Dirty = true
		})
		if contract && c.warnOnce("contract|"+rel) {
			notes = append(notes, fmt.Sprintf("[crew] %s is a contract file (schema, migration, types, dependencies or API). Teammates' agents now see it as reserved; after changing it, send a short contract notice with the crew_post tool (type \"notice\") saying what changed.", rel))
		}
	}
	if d := deliver(c, in.SessionID); d != "" {
		notes = append(notes, d)
	}
	out := map[string]any{"hookEventName": "PreToolUse"}
	if ask != "" {
		out["permissionDecision"] = "ask"
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
					if match(path, r.Path) || match(r.Path, path) {
						ask = fmt.Sprintf("crew: this command changes files under %s, which %s reserved exclusively until %s%s. Approve only if you agreed with them.",
							cleanIncoming(r.Path, 120), who, time.Unix(r.Until, 0).Format("15:04"), reasonSuffix(r.Reason))
						break outer
					}
				}
			}
		}
	}
	if d := deliver(c, in.SessionID); d != "" {
		notes = append(notes, d)
	}
	out := map[string]any{"hookEventName": "PreToolUse"}
	if ask != "" {
		out["permissionDecision"] = "ask"
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
	subj := strings.Split(strings.TrimSpace(commits), "\n")
	branch, _ := git(c.Repo.Top, "rev-parse", "--abbrev-ref", "@{u}")
	text := fmt.Sprintf("pushed %d commit(s) to %s: %s", len(subj), branch, strings.Join(trimList(subj, 5), "; "))
	if len(contract) > 0 {
		text += ". Contract files changed: " + strings.Join(trimList(contract, 8), ", ") + " — pull before building on them"
	}
	if t, err := cleanOutgoing(text); err == nil {
		text = t
	} else {
		text = string([]rune(text)[:maxText-1])
	}
	ctx, cancel := withTimeout(10 * time.Second)
	defer cancel()
	c.sendMsg(ctx, "push", text, "", trimList(files, 40))
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
	os.Remove(sessionFile(c, in.SessionID))
	if pidFile := c.watchPidFile(in.SessionID); true {
		var w struct{ Pid int }
		if readJSON(pidFile, &w) && w.Pid > 0 && w.Pid != os.Getpid() {
			killPid(w.Pid)
		}
		os.Remove(pidFile)
	}
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
	var w struct{ Pid int }
	if readJSON(pidFile, &w) && w.Pid != os.Getpid() && pidAlive(w.Pid) {
		return 0 // this session already has a watcher
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
	go subscribe(ctx, c.Relays, func() Filter {
		since := c.loadSpool().Since - 60
		if since < time.Now().Add(-msgTTL).Unix() {
			since = time.Now().Add(-msgTTL).Unix()
		}
		return c.filter(since)
	}, func(ev Event) {
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
	if len(keep) >= wakesPerHr {
		return false
	}
	writeJSON(p, append(keep, time.Now().Unix()))
	return true
}

func wakeNow(c *Ctx, sid string) int {
	text := deliver(c, sid)
	if text == "" {
		return 0
	}
	fmt.Fprintln(os.Stderr, "[crew] Team update that arrived while this session was idle (nobody typed anything; on screen the reply appears right below the previous answer, so start it with a short line like \"📨 Team update (crew)\" and keep it brief):")
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

func newRoomKey(cwd string) string {
	r, why := openRepo(cwd)
	if r == nil {
		return "crew can't run here: " + why
	}
	p := filepath.Join(r.Top, ".claude", "crew.key")
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(randomHex(32)+"\n"), 0o644); err != nil {
		return "could not write .claude/crew.key: " + err.Error()
	}
	return "Created .claude/crew.key. Commit and push it: teammates join the new room after they pull, and people without access to the repo lose access to new messages."
}
