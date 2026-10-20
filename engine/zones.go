package main

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"
	"sync"
)

// Contract zones: files whose change silently breaks a teammate's work (schemas, migrations, generated
// types, dependency manifests, API specs). Editing one takes an automatic exclusive reservation, and
// teammates get asked before touching it. Generic across stacks; a team can add its own in
// .claude/crew.json {"contract": ["glob", ...]}.
var contractZones = []string{
	"**/migrations/**", "**/migrate/**", "**/*.sql",
	"prisma/schema.prisma", "**/schema.prisma", "drizzle/**", "**/schema.graphql", "**/*.graphql",
	"**/openapi.*", "**/swagger.*", "**/*.proto",
	"**/database.types*.ts", "**/supabase/**/*.sql", "**/models.py", "db/schema.rb", "**/alembic/**",
	"package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb", "bun.lock",
	"go.mod", "go.sum", "Cargo.toml", "Cargo.lock", "requirements*.txt", "pyproject.toml", "poetry.lock", "uv.lock",
	"Gemfile", "Gemfile.lock", "composer.json", "composer.lock",
	".env.example", "docker-compose*.yml", "Dockerfile",
}

var (
	reCache = map[string]*regexp.Regexp{}
	reMu    sync.Mutex
)

func globRe(g string) *regexp.Regexp {
	reMu.Lock()
	defer reMu.Unlock()
	if re, ok := reCache[g]; ok {
		return re
	}
	var b strings.Builder
	b.WriteString("(?i)^")
	for i := 0; i < len(g); i++ {
		ch := g[i]
		switch {
		case ch == '*' && i+1 < len(g) && g[i+1] == '*':
			i++
			if i+1 < len(g) && g[i+1] == '/' {
				i++
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case ch == '*':
			b.WriteString("[^/]*")
		case ch == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	b.WriteString("$")
	re := regexp.MustCompile(b.String())
	if len(reCache) > 2000 { // patterns come from teammates; keep the cache bounded
		reCache = map[string]*regexp.Regexp{}
	}
	reCache[g] = re
	return re
}

func norm(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "./")
	return strings.TrimSuffix(p, "/")
}

// match: does file fall under pattern? A pattern without wildcards also matches files inside it (a folder).
func match(file, pattern string) bool {
	f, p := norm(file), norm(pattern)
	if f == "" || p == "" {
		return false
	}
	if !strings.ContainsAny(p, "*?") {
		lf, lp := strings.ToLower(f), strings.ToLower(p)
		return lf == lp || strings.HasPrefix(lf, lp+"/")
	}
	return globRe(p).MatchString(f)
}

func (c *Ctx) contractZone(file string) bool {
	zones := contractZones
	var cfg struct {
		Contract []string `json:"contract"`
	}
	if b, err := readSmall(path.Join(norm(c.Repo.Top), ".claude", "crew.json"), 64<<10); err == nil && json.Unmarshal(b, &cfg) == nil {
		zones = append([]string{}, zones...)
		for i, z := range cfg.Contract {
			if i < 50 && !tooBroad(z) {
				zones = append(zones, z)
			}
		}
	}
	for _, z := range zones {
		if match(file, z) {
			return true
		}
	}
	return false
}

// generated files are produced from a source (lockfiles, generated types, build output, snapshots): leasing
// them only produces noise, so crew leases their source instead.
var generatedZones = []string{
	"package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb", "bun.lock", "go.sum", "Cargo.lock",
	"poetry.lock", "uv.lock", "composer.lock", "Gemfile.lock",
	"**/*.gerado.*", "**/*.generated.*", "**/*.gen.*", "**/database.types*.ts", "**/__snapshots__/**",
	"dist/**", "build/**", ".next/**", "out/**", "coverage/**",
}

// untracked: the file is ignored by git or generated; crew neither leases it nor warns about it.
func (c *Ctx) untracked(rel string) bool {
	for _, z := range generatedZones {
		if match(rel, z) {
			return true
		}
	}
	_, err := git(c.Repo.Top, "check-ignore", "-q", "--", rel)
	return err == nil // exit 0 = ignored
}

// coveredBy: an existing lease (a declared scope glob) already covers this file.
func coveredBy(rel string, l []Reservation) bool {
	for _, r := range l {
		if r.Path != rel && match(rel, r.Path) {
			return true
		}
	}
	return false
}
