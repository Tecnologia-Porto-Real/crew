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
	cmd := exec.CommandContext(ctx, "git", args...)
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
	roots, err := git(top, "rev-list", "--max-parents=0", "HEAD")
	if err != nil || roots == "" {
		return nil, "repository has no commits yet"
	}
	root := oldestRoot(top, strings.Fields(roots))

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

// visibility probe: anonymous ls-remote, never prompting or using stored credentials.
// Returns "public", "private" (or nonexistent / unreachable anonymously) or "unknown".
func probeVisibility(r *Repo) string {
	if r.Host == "" {
		return "unknown"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	url := "https://" + r.Host + "/" + r.Path + ".git"
	cmd := exec.CommandContext(ctx, "git", "-c", "credential.helper=", "-c", "core.askPass=", "ls-remote", "--heads", url)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_ASKPASS=", "SSH_ASKPASS=")
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return "public"
	}
	s := strings.ToLower(string(out))
	if ctx.Err() != nil || strings.Contains(s, "could not resolve host") || strings.Contains(s, "unable to access") && strings.Contains(s, "timed out") {
		return "unknown"
	}
	return "private"
}
