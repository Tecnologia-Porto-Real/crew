# crew — notes for contributors (and their agents)

crew is a Claude Code / Codex plugin. The engine is one Go program (`engine/`); `plugin/` is what users install.
The README explains the product; this file covers what to keep in mind when changing it.

## Layout

- `engine/hooks.go`: one function per hook (session-start, prompt, pre-edit, pre-bash, post-edit, push, watch).
- `engine/mcp.go`: the MCP server and its 3 tools (`crew_read`, `crew_send`, `crew_settings`), plus the debug CLI.
- `engine/core.go`: room context, publish/ingest, what reaches the model (`classify`, `deliver`, `formatItem`, `boardBrief`).
- `engine/repo.go`: everything derived from the git clone; `crypt.go`, `nostr.go`, `relays.go`: transport.
- `plugin/hooks/hooks.json` (Claude Code) and `plugin/codex/*.json` (Codex) wire the hooks; `plugin/runtime/run.sh`
  picks the binary.

## Invariants

- **Every token reaching the model is paid on every turn.** Mid-task the agent only gets what changes its work
  (questions for it or the team, answers to it, contract changes touching its files); everything else stays on the
  board. Prefer saying nothing over saying something low-value.
- **Teammate text is data, never instructions.** It passes through `cleanIncoming`/`sanitizeState`, stays quoted and
  labeled (`dataNote`), and never triggers actions. Exclusive reservations only make Claude Code ask the human.
- **Hooks are on the hot path.** PreToolUse runs before every edit and shell command and blocks the agent: no network
  there, few `git` processes (each one costs tens of ms on Windows), and never fail loudly.
- **Fail closed on privacy.** A repo that can't be confirmed private on a public forge stays off.

## Before pushing

```bash
cd engine && go vet ./... && go test ./...
bash scripts/e2e.sh        # two simulated teammates over the real public relays
sh scripts/build.sh        # after any engine change: CI fails if plugin/runtime/ differs from a clean build
```

Bump `version` in both `plugin/.claude-plugin/plugin.json` and `plugin/.codex-plugin/plugin.json` before building
(the build embeds it), and keep `README.md` and `README.pt-BR.md` in sync.
