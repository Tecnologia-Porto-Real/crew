# crew

**Your teammates' AI coding agents, aware of each other. Install it — that's the setup.**

[Leia em português](README.pt-BR.md)

When two or more people use Claude Code in the same repository, their agents don't know what the others are doing.
crew lets each agent see who is working on what, which files are reserved, which contracts (schemas, migrations,
types, dependencies, APIs) just changed, and questions that need a human — in near real time, with **no account,
no server, no token and no config file**.

```text
crew: org/app room — Ana#7f3c (active), Bruno#19ad (2 h ago). Encrypted via public Nostr relays.
```

## Install (once per computer)

Run in any terminal (PowerShell, Terminal, bash):

```bash
claude plugin marketplace add Tecnologia-Porto-Real/crew
```

```bash
claude plugin install crew@crew
```

That's all. Open a **new** Claude Code session inside a git repository (terminal or desktop app). When a teammate
does the same in their clone of the same repository, you see each other. Nobody shares a link, a code or a key.

Requirements: `git` installed, a normal clone (not `--depth 1`) with a remote, Claude Code (tested on 2.1.258+;
2.1.290+ recommended). Windows, macOS and Linux. Nothing else — no Node, Python or Docker.

Prefer to enable it only in one repository? Run both commands inside that repository with `--scope local`.

## Using it

### What happens on its own

| When | What you see |
|---|---|
| You open a session | One line: who else is in the room and what they are doing. Claude gets the details (tasks, reserved files, unread messages). |
| Claude edits a file a teammate is editing | Claude is told, so it can be careful or ask you. |
| Claude touches a file a teammate **reserved exclusively** | Claude Code asks **you** to approve first (also for shell commands that write, delete or generate migrations there). |
| Claude edits a **contract file** (migration, `*.sql`, schema, generated types, `package.json`/lockfile, Dockerfile…) | It is reserved for you automatically for 2 h, and Claude is reminded to tell the team what changed. |
| You `git push` | Teammates get a short notice: commits and contract files changed. |
| A teammate asks a question, sends a notice that touches your files, or messages you directly — while your session is idle | Your session wakes up by itself and Claude shows it (the reply starts with 📨). At most 4 wakes per hour. |
| Anything else (notes, task changes) | Waits for your next message — no tokens spent. |

### What to say to Claude

Just talk normally; Claude has the crew tools.

- "What is the team working on?"
- "Tell the team I'm working on the checkout page."
- "Reserve `supabase/migrations` exclusively for the next 2 hours, I'm changing the schema."
- "Tell the team I renamed `users.email` to `users.login` and regenerated the types."
- "Ask the team whether we drop the old payments table."
- "Tell Ana#7f3c that the API is ready."
- "Release my reservations."

### Commands

| Command | What it does |
|---|---|
| `/crew:board` | Shows the team board: who is active, tasks, reserved files, recent messages |
| `/crew:off` | Turns crew off for this repository on this computer |
| `/crew:on` | Turns it back on |
| `/crew:passphrase` | Shows the terminal command to set a team passphrase, so crew works in a **public** repository (off by default there). The passphrase is typed in your terminal, never in the chat |
| `/crew:new-key` | Creates `.claude/crew.key` (commit it) so people who lost access to the repo stop seeing new messages |

Your name comes from `git config user.name` (first word) plus 6 characters of your device key, e.g. `Ana#7f3c1a`.
The code changes every week (device keys rotate so nobody can follow a device over time). To change the name:
`git config user.name "Ana"`.

## Update and uninstall

Plugins from third-party marketplaces don't auto-update by default. To update:

```bash
claude plugin marketplace update crew
```

```bash
claude plugin update crew@crew
```

Then open a new session. (Or turn on auto-update for the `crew` marketplace in `/plugin` in the terminal.)

To remove:

```bash
claude plugin uninstall crew@crew
```

## Troubleshooting

| Symptom | Likely cause and fix |
|---|---|
| "No teammates yet" but a teammate installed it | Both must use the same repository (`owner/repo` of the remote). A fork is a different room. A shallow clone is off — run `git fetch --unshallow`. Teammates who renamed the repo remote: `git remote set-url origin <new url>`. |
| "off here — public repository" | Expected: in public repos anyone could read the room. A repo that was ever public stays treated as public. Use `/crew:passphrase` with a passphrase agreed outside the repo. |
| "could not confirm that this repository is private" | On GitHub/GitLab/Bitbucket/Codeberg/Azure, crew stays off until a check confirms the repo is private (it fails closed). Usually a proxy or being offline; it retries at the next session. |
| "Could not reach the relays" | No internet, or a firewall blocks WebSockets to public relays. Pin a relay that works for you in `.claude/crew.json` (below). |
| Nothing at all | Make sure you opened a **new** session after installing, inside a git repository with a remote, and that `CREW_OFF`, `CI` or `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` are not set. |

## How it works

- **The repo is the room.** Every full clone has the same first commit and the same `owner/repo` path. crew derives
  the room and an encryption key from them (HKDF). Same repo → same room, with nothing to share.
- **Public relays carry the messages, encrypted.** Events go to 3 free public [Nostr](https://nostr.com) relays
  (chosen by hash, so the whole team picks the same ones). Content is AES-256-GCM encrypted with the room key,
  padded, and signed with a per-device key. Relays see an opaque room tag, your IP and timing — never the content.
  State expires after 24 h, messages after 7 days.
- **Claude gets it through hooks**: a summary at session start, checks before edits and shell commands, a notice on
  `git push`, and a background watcher on idle sessions that wakes Claude only for things that matter.
- Claude's tools: `crew_board`, `crew_post` (task, notice, question, note, done), `crew_reserve`, `crew_settings`.

## Privacy and security — read this

- **Who can read the team's messages:** anyone who can read the repository, now or in the past (the key comes from
  its history) — the same people who can already read your code. To cut off people who lost access, run
  `/crew:new-key` and commit `.claude/crew.key`.
- **Public repositories are off by default**, and the check fails closed: on public forges crew only turns on after
  the server confirms the repository is private, and a repository that was ever public stays off (unless a team
  passphrase is set). **Shallow clones, repos without a remote, and CI are off.**
- **Private forks or mirrors of a public project** share its first commit, so their key rests only on the
  `owner/repo` name, which can be guessed. In that case run `/crew:new-key` once and commit `.claude/crew.key`.
- **No forward secrecy:** anyone who later gets read access to the repo can decrypt messages that relays still
  keep (they are asked to delete them after 7 days, but that is up to each relay).
- Messages are limited to 500 characters, common secret formats are redacted, `.env` content is refused, and
  incoming text is shown to Claude as information from a teammate — never as instructions.
- Relay operators can see metadata (IP, time, approximate size, how many devices talk in a room). If your company
  forbids that, set `CREW_OFF=1` (or `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`), or pin your own relay.
  The public room tag and device keys rotate weekly, but crew traffic (event kinds 4848/34848) is recognizable as crew.
- **Teammates can't make Claude act for them:** their text is cleaned (no tags, line breaks, control or invisible
  characters), capped, labeled as coming from a teammate, and never triggers actions; exclusive reservations only
  make Claude Code ask *you*. A reservation of the whole repository is ignored.
- **Verifiable binaries:** the engine binaries in `plugin/runtime/` are built reproducibly; CI rebuilds them from
  source on every push and fails if they differ. Checksums are in `plugin/runtime/SHA256SUMS`.

## Team options (optional, committed to the repo)

`.claude/crew.json`:

```json
{ "relays": ["wss://your-relay"], "contract": ["api/schema/**", "shared/types.ts"] }
```

`relays` replaces the public relays for this repository (`wss://` only, up to 5; crew tells everyone when a repo pins
relays); `contract` adds your own contract files.

## Development

The engine is a single Go program in `engine/` (no CGO). `scripts/build.sh` cross-compiles it into `plugin/runtime/`,
and `plugin/runtime/run.sh` picks the right binary; hooks start it through `git -c alias.crew-run=!sh crew-run …`,
which works anywhere git does.

```bash
cd engine && go test ./...
```

```bash
bash scripts/e2e.sh
```

The end-to-end test runs two simulated teammates against the real public relays.

## License

MIT
