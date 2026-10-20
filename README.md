# crew

**Your teammates' AI coding agents, aware of each other. Install it — that's the setup.**

When two or more people use Claude Code in the same repository, their agents don't know what the
others are doing. crew lets each agent see who is working on what, which files are reserved, which
contracts (schemas, migrations, types, dependencies, APIs) just changed, and questions that need a
human — in near real time, with **no account, no server, no token and no config file**.

```text
crew: org/app room — Ana#7f3c (active), Bruno#19ad (2 h ago). Encrypted via public Nostr relays.
```

## Install

```bash
claude plugin marketplace add Tecnologia-Porto-Real/crew
```

```bash
claude plugin install crew@crew
```

Open Claude Code in any git repository. When a teammate does the same in their clone, you see each other.

Requirements: git (with a remote), Claude Code 2.1.290+ recommended. Works on Windows, macOS and Linux,
in the terminal and in the desktop app. No Node, Python or anything else to install.

## How it works

- **The repo is the room.** Every full clone has the same first commit and the same `owner/repo` path.
  crew derives the team's room and an encryption key from them (HKDF). Same repo → same room, with nothing to share.
- **Public relays carry the messages, encrypted.** Events go to 3 free public [Nostr](https://nostr.com) relays
  (chosen by hash, so the whole team picks the same ones). Content is AES-256-GCM encrypted with the room key,
  padded, and signed with a per-device key. Relays see an opaque room tag, your IP and timing — never the content.
  State expires after 24 h, messages after 7 days.
- **Claude gets it through hooks.** A summary at session start; a warning before editing a file a teammate
  reserved (exclusive reservations ask *you* before Claude proceeds); and when your session is idle, a background
  watcher wakes Claude only for things that matter: a question for the team, a contract notice touching your
  files, a push that changed files you are working on (max 4 wakes per hour).
- **Contract files** (migrations, `*.sql`, Prisma/GraphQL/OpenAPI/proto schemas, generated DB types,
  dependency manifests and lockfiles, Dockerfiles) get an automatic exclusive reservation while you edit them,
  and every `git push` sends the team a short notice of what changed.

Claude also gets four tools: `crew_board`, `crew_post` (task, notice, question, note, done), `crew_reserve`
and `crew_settings`. Commands: `/crew:board`, `/crew:off`, `/crew:on`, `/crew:passphrase`, `/crew:new-key`.

## Privacy and security — read this

- **Who can read the team's messages:** anyone who can read the repository, now or in the past
  (the key comes from its history). That's the same group that can already read your code.
  To cut off people who lost access, run `/crew:new-key` and commit `.claude/crew.key`.
- **Public repositories are off by default**, because anyone could derive the key. A team that wants crew
  there sets a shared passphrase with `/crew:passphrase` (stored only on each machine).
- **Shallow clones, repos without a remote, and CI are off.**
- Messages are limited to 500 characters, common secret formats are redacted, `.env` content is refused,
  and incoming text is shown to Claude as information from a teammate — never as instructions.
- Relay operators can see metadata (IP, time, approximate size, how many devices talk in a room).
  If your company forbids that, set `CREW_OFF=1` (or `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`), or
  pin your own relay in `.claude/crew.json`: `{"relays": ["wss://relay.example.com"]}`.

## Team options (optional, committed to the repo)

`.claude/crew.json`:

```json
{ "relays": ["wss://your-relay"], "contract": ["api/schema/**", "shared/types.ts"] }
```

## Development

The engine is a single Go program in `engine/` (no CGO). `scripts/build.sh` cross-compiles it into
`plugin/runtime/`, and `plugin/runtime/run.sh` picks the right binary; hooks start it through
`git -c alias.crew-run=!sh crew-run …`, which works anywhere git does.

```bash
cd engine && go test ./...
```

```bash
bash scripts/e2e.sh
```

The end-to-end test runs two simulated teammates against the real public relays.

## License

MIT
