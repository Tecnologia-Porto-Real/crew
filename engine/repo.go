package main

// Everything crew knows about "the team" comes from the git clone: the room, the key and the
// display name. No signup, no token, no setup command.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Repo struct {
	Top       string // working tree root
	CommonDir string // shared .git dir (worktrees share it)
	Root      string // oldest root commit
	Path      string // "owner/repo", lowercase, no host
	Host      string // remote host, for the visibility probe
	Remote    string
}

var errOff = errors.New("off")

func git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// a repository's own config must not make our background git calls run its commands
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false"}, args...)...)
	cmd.Dir = dir
	hideWindow(cmd)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// openRepo inspects the clone at dir. It returns a reason string when crew must stay off here.
func openRepo(dir string) (*Repo, string) {
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return nil, "not a git repository"
	}
	if s, _ := git(top, "rev-parse", "--is-shallow-repository"); s == "true" {
		return nil, "shallow clone (run `git fetch --unshallow` to join your team's room)"
	}
	common, _ := git(top, "rev-parse", "--path-format=absolute", "--git-common-dir")
	root := cachedRoot(common)
	if root == "" {
		// walking the whole history is slow on big repos, so the answer is cached per clone for a day
		roots, err := git(top, "rev-list", "--max-parents=0", "HEAD")
		if err != nil || roots == "" {
			return nil, "repository has no commits yet"
		}
		root = oldestRoot(top, strings.Fields(roots))
		saveRoot(common, root)
	}

	remote := pickRemote(top)
	if remote == "" {
		return nil, "repository has no remote"
	}
	url, _ := git(top, "remote", "get-url", remote)
	host, path := parseRemote(url)
	if path == "" {
		return nil, "could not read the remote URL"
	}
	return &Repo{Top: filepath.Clean(top), CommonDir: common, Root: root, Path: path, Host: host, Remote: remote}, ""
}

type rootEntry struct {
	Root string `json:"root"`
	At   int64  `json:"at"`
}

func cachedRoot(common string) string {
	if common == "" {
		return ""
	}
	m := map[string]rootEntry{}
	readJSON(filepath.Join(dataRoot(), "roots.json"), &m)
	if e, ok := m[common]; ok && time.Since(time.Unix(e.At, 0)) < 24*time.Hour && len(e.Root) >= 40 {
		return e.Root
	}
	return ""
}

func saveRoot(common, root string) {
	if common == "" {
		return
	}
	withLock(filepath.Join(dataRoot(), "roots.lock"), func() error {
		m := map[string]rootEntry{}
		readJSON(filepath.Join(dataRoot(), "roots.json"), &m)
		for k, e := range m {
			if time.Since(time.Unix(e.At, 0)) > 7*24*time.Hour {
				delete(m, k)
			}
		}
		m[common] = rootEntry{Root: root, At: time.Now().Unix()}
		return writeJSON(filepath.Join(dataRoot(), "roots.json"), m)
	})
}

func oldestRoot(top string, roots []string) string {
	if len(roots) == 1 {
		return roots[0]
	}
	type rc struct {
		sha string
		t   int64
	}
	var list []rc
	for _, r := range roots {
		ts, _ := git(top, "show", "-s", "--format=%ct", r)
		t, _ := strconv.ParseInt(ts, 10, 64)
		list = append(list, rc{r, t})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].t != list[j].t {
			return list[i].t < list[j].t
		}
		return list[i].sha < list[j].sha
	})
	return list[0].sha
}

// tracking remote of the current branch → origin → first remote
func pickRemote(top string) string {
	if up, err := git(top, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err == nil && strings.Contains(up, "/") {
		r := strings.SplitN(up, "/", 2)[0]
		if _, err := git(top, "remote", "get-url", r); err == nil {
			return r
		}
	}
	list, _ := git(top, "remote")
	remotes := strings.Fields(list)
	for _, r := range remotes {
		if r == "origin" {
			return r
		}
	}
	if len(remotes) > 0 {
		return remotes[0]
	}
	return ""
}

var (
	reScp = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`) // git@github.com:owner/repo.git
	reURL = regexp.MustCompile(`^[a-z+]+://(?:[^@/]+@)?([^/:]+)(?::\d+)?/(.+)$`)
)

// parseRemote returns (host, "owner/repo") from any common remote URL form.
func parseRemote(u string) (string, string) {
	u = strings.TrimSpace(u)
	var host, p string
	if m := reURL.FindStringSubmatch(u); m != nil {
		host, p = m[1], m[2]
	} else if m := reScp.FindStringSubmatch(u); m != nil && !strings.Contains(m[1], "\\") {
		host, p = m[1], m[2]
	} else {
		return "", ""
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	p = strings.TrimPrefix(p, "scm/") // bitbucket server
	return strings.ToLower(host), strings.ToLower(p)
}

// displayName: git user.name → GIT_AUTHOR_NAME → OS user.
func displayName(top string) string {
	if n, _ := git(top, "config", "user.name"); n != "" {
		return firstWord(n)
	}
	if n := os.Getenv("GIT_AUTHOR_NAME"); n != "" {
		return firstWord(n)
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		n := u.Username
		if i := strings.LastIndexAny(n, `\/`); i >= 0 {
			n = n[i+1:]
		}
		return firstWord(n)
	}
	return "dev"
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if f := strings.Fields(s); len(f) > 0 {
		s = f[0]
	}
	if len(s) > 24 {
		s = s[:24]
	}
	return s
}

func myEmail(top string) string {
	e, _ := git(top, "config", "user.email")
	return e
}

// Well-known public forges: there, an inconclusive visibility check keeps crew off (fail closed).
var publicForges = map[string]bool{"github.com": true, "gitlab.com": true, "bitbucket.org": true, "codeberg.org": true, "dev.azure.com": true}

// canonicalHost maps SSH endpoints and ~/.ssh/config aliases to the forge's web host.
func canonicalHost(h string) string {
	switch h {
	case "ssh.github.com":
		return "github.com"
	case "altssh.gitlab.com":
		return "gitlab.com"
	case "altssh.bitbucket.org":
		return "bitbucket.org"
	case "ssh.dev.azure.com", "vs-ssh.visualstudio.com":
		return "dev.azure.com"
	}
	if publicForges[h] || strings.Contains(h, ".") && reHost.MatchString(h) && !strings.HasSuffix(h, ".local") {
		return h
	}
	// an alias like "github-work": ask ssh what it really points to
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-G", "--", h)
	hideWindow(cmd)
	if out, err := cmd.Output(); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if f := strings.Fields(l); len(f) == 2 && f[0] == "hostname" && reHost.MatchString(strings.ToLower(f[1])) {
				return canonicalHost(strings.ToLower(f[1]))
			}
		}
	}
	return h
}

func onPublicForge(r *Repo) bool { return publicForges[canonicalHost(r.Host)] }

// probeVisibility: anonymous ls-remote, never prompting or using stored credentials.
// "public" = anyone can read it; "private" = the server positively refused anonymous access;
// "unknown" = anything else (offline, proxy, TLS interception, 5xx, rate limits…).
func probeVisibility(r *Repo) string {
	if r.Host == "" {
		return "unknown"
	}
	host, path := canonicalHost(r.Host), r.Path
	if host == "dev.azure.com" { // v3/org/project/repo → org/project/_git/repo
		p := strings.TrimPrefix(path, "v3/")
		if parts := strings.Split(p, "/"); len(parts) == 3 {
			path = parts[0] + "/" + parts[1] + "/_git/" + parts[2]
		}
	}
	return probeHost(host, path)
}

var reHost = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

func probeHost(host, path string) string {
	if !reHost.MatchString(host) {
		return "unknown"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	url := "https://" + host + "/" + path
	if !strings.Contains(path, "/_git/") {
		url += ".git"
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "credential.helper=", "-c", "core.askPass=", "ls-remote", "--heads", "--", url)
	// truly anonymous: no system/global/repo config (credential helpers such as `gh auth setup-git`,
	// url.insteadOf rewrites), nothing inherited from an outer git process, and a neutral directory
	cmd.Dir = os.TempDir()
	var env []string
	for _, kv := range os.Environ() {
		k := strings.ToUpper(strings.SplitN(kv, "=", 2)[0])
		if strings.HasPrefix(k, "GIT_") || strings.HasPrefix(k, "GCM_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_ASKPASS=", "SSH_ASKPASS=",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return "public"
	}
	s := strings.ToLower(string(out))
	if ctx.Err() != nil {
		return "unknown"
	}
	for _, refused := range []string{"terminal prompts disabled", "could not read username", "authentication failed",
		"repository not found", "requested url returned error: 401", "requested url returned error: 403", "requested url returned error: 404"} {
		if strings.Contains(s, refused) {
			return "private"
		}
	}
	return "unknown"
}
