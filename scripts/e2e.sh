#!/bin/bash
# End-to-end test against the real public relays: two teammates (Alice, Bob) on one machine,
# each with their own clone and their own plugin data dir. Nothing is configured.
set -u
root=$(cd "$(dirname "$0")/.." && pwd)
case "$(uname -s)" in MINGW*|MSYS*) BIN="$root/plugin/runtime/crew-windows-amd64.exe" ;; Darwin) BIN="$root/plugin/runtime/crew-darwin-$(uname -m | sed s/x86_64/amd64/)" ;; *) BIN="$root/plugin/runtime/crew-linux-amd64" ;; esac
base=$(mktemp -d)
np() { cygpath -m "$1" 2>/dev/null || echo "$1"; }   # native path for the engine
B_=$(np "$base")
pass=0; fail=0
ok() { echo "ok   - $1"; pass=$((pass+1)); }
ko() { echo "FAIL - $1"; echo "       $2" | head -c 600; echo; fail=$((fail+1)); }
check() { if echo "$2" | grep -q -- "$3"; then ok "$1"; else ko "$1" "$2"; fi; }

remote="https://github.com/crew-e2e-$RANDOM$RANDOM/app-$RANDOM.git"   # does not exist → treated as private
git init -q "$base/alice" 2>/dev/null && cd "$base/alice" && git config user.name "Alice Doe" && git config user.email a@x.y
echo hi > README.md && echo "node_modules/" > .gitignore && git add . && git commit -qm init && git remote add origin "$remote"
git clone -q "$base/alice" "$base/bob" && cd "$base/bob" && git remote set-url origin "$remote" && git config user.name "Bob Roe" && git config user.email b@x.y

A() { (cd "$base/alice" && CREW_DATA="$base/data-alice" "$BIN" "$@"); }
B() { (cd "$base/bob" && CREW_DATA="$base/data-bob" "$BIN" "$@"); }
hookA() { echo "$2" | (cd "$base/alice" && CREW_DATA="$base/data-alice" "$BIN" hook "$1"); }
hookB() { echo "$2" | (cd "$base/bob" && CREW_DATA="$base/data-bob" "$BIN" hook "$1"); }

ra=$(A whoami); rb=$(B whoami)
echo "alice: $ra"; echo "bob:   $rb"
[ "$(echo "$ra" | sed 's/.*room=\([0-9a-f]*\).*/\1/')" = "$(echo "$rb" | sed 's/.*room=\([0-9a-f]*\).*/\1/')" ] && ok "both clones derive the same room with zero setup" || ko "same room" "$ra / $rb"

out=$(hookA session-start "{\"session_id\":\"a1\",\"cwd\":\"$B_/alice\",\"source\":\"startup\"}")
check "first teammate sees an empty room" "$out" "no other session active"
if echo "$out" | grep -q additionalContext; then ko "a solo session costs the model no tokens" "$out"; else ok "a solo session costs the model no tokens"; fi

out=$(hookB session-start "{\"session_id\":\"b0\",\"cwd\":\"$B_/bob\",\"source\":\"startup\"}")
if echo "$out" | grep -q "Alice#"; then ko "a session with nothing to coordinate stays out of the room" "$out"; else ok "a session with nothing to coordinate stays out of the room"; fi
hookB session-end '{"session_id":"b0","cwd":"'"$B_"'/bob"}' >/dev/null

A send scope "Refactor the login flow" --files "db/migrations/**,src/auth,package.json" >/dev/null
out=$(hookB session-start "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\",\"source\":\"startup\"}")
check "second teammate sees the first one, no setup" "$out" "Alice#"
check "with a teammate active, the agent is asked for a scope and shown the tool's shape" "$out" "crew_send {type"
out=$(B board)
check "board shows the teammate's declared scope" "$out" "Refactor the login flow"
check "a scope on contract files becomes an exclusive reservation" "$out" "db/migrations/\*\* (exclusive)"

out=$(hookB pre-edit "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\",\"tool_name\":\"Write\",\"tool_input\":{\"file_path\":\"$B_/bob/db/migrations/002_x.sql\"}}")
check "editing a file reserved exclusively asks the human first" "$out" '"permissionDecision":"ask"'

out=$(hookB pre-edit "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\",\"tool_name\":\"Edit\",\"tool_input\":{\"file_path\":\"$B_/bob/src/app.ts\"}}")
if echo "$out" | grep -q permissionDecision; then ko "normal file is not blocked" "$out"; else ok "normal file is not blocked"; fi
hookB pre-edit "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\",\"tool_name\":\"Write\",\"tool_input\":{\"file_path\":\"$B_/bob/node_modules/.cache/tmp.sql\"}}" >/dev/null
me=$(cat "$base"/data-bob/repos/*/rooms/*/sessions/b1.me.json)
if echo "$me" | grep -q node_modules; then ko "git-ignored files are never reserved" "$me"; else ok "git-ignored files are never reserved"; fi
check "an edit becomes a lease of the edited file" "$me" "src/app.ts"

j='{"session_id":"b1","cwd":"'"$B_"'/bob","tool_name":"Bash","tool_input":{"command":"f=\"db/migrations/$(date +%Y)_x.sql\" && : > \"$f\""}}'; out=$(hookB pre-bash "$j")
check "a shell command writing into a reserved folder asks the human" "$out" '"permissionDecision":"ask"'
out=$(hookB pre-bash "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"npx supabase migration new add_x\"}}")
check "a migration generator command asks the human" "$out" '"permissionDecision":"ask"'
out=$(hookB pre-bash "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"ls db/migrations && cat db/migrations/001.sql\"}}")
if echo "$out" | grep -q permissionDecision; then ko "reading a reserved folder is free" "$out"; else ok "reading a reserved folder is free"; fi
out=$(hookB pre-bash "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"npm install lodash\"}}")
check "adding a dependency asks when a teammate reserved package.json" "$out" '"permissionDecision":"ask"'
out=$(hookB pre-bash "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"npm install\"}}")
if echo "$out" | grep -q permissionDecision; then ko "a bare install is free" "$out"; else ok "a bare install (no manifest change) is free"; fi

# two sessions of Bob on one machine see each other, with their own scopes
hookB session-start "{\"session_id\":\"b9\",\"cwd\":\"$B_/bob\",\"source\":\"startup\"}" >/dev/null
out=$(hookB pre-edit "{\"session_id\":\"b9\",\"cwd\":\"$B_/bob\",\"tool_name\":\"Edit\",\"tool_input\":{\"file_path\":\"$B_/bob/src/app.ts\"}}")
check "a second session on the same machine sees the first one's edit" "$out" "your other session"
hookB session-end '{"session_id":"b9","cwd":"'"$B_"'/bob"}' >/dev/null

# Bob's session goes idle: the Stop hook waits for the team
errf="$base/watch.err"
echo "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\"}" | (cd "$base/bob" && CREW_DATA="$base/data-bob" "$BIN" hook watch 2>"$errf") &
wpid=$!
sleep 6
A send scope "Refactor the login flow, step 2" --files "db/migrations/**,src/auth" >/dev/null
sleep 8
if kill -0 $wpid 2>/dev/null; then ok "a teammate's scope change does not wake an idle session"; else ko "scope change must not wake" "$(cat "$errf")"; fi
t0=$(date +%s)
A send question "Can I rename users.email to users.login?" >/dev/null
for i in $(seq 1 40); do kill -0 $wpid 2>/dev/null || break; sleep 1; done
wait $wpid; code=$?
t1=$(date +%s)
[ "$code" = 2 ] && ok "a question wakes the idle session (exit 2) in $((t1-t0)) s" || ko "question wakes (exit $code)" "$(cat "$errf")"
check "the wake text carries the question" "$(cat "$errf")" "rename users.email"
check "the wake text is framed as information, not instructions" "$(cat "$errf")" "not instructions"

out=$(hookB prompt "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\"}")
if echo "$out" | grep -q "rename users.email"; then ko "delivered once per session" "$out"; else ok "a message is delivered once per session"; fi
check "a question nobody answered is kept in front of the human" "$out" "wait for a human decision"

# a question for the team reaches the session Bob used last, once — not every open session
hookB session-start "{\"session_id\":\"b2\",\"cwd\":\"$B_/bob\",\"source\":\"startup\"}" >/dev/null
A send question "Who reviews the login refactor?" >/dev/null
sleep 4
out=$(B board >/dev/null; hookB prompt "{\"session_id\":\"b2\",\"cwd\":\"$B_/bob\"}")
check "a team question reaches the session used last" "$out" "Who reviews the login refactor"
out=$(hookB prompt "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\"}")
if echo "$out" | grep -q "Who reviews"; then ko "one person sees a question once" "$out"; else ok "the same person doesn't get it again in another session"; fi

# ---- Codex mode (same engine, Codex hook semantics) ----
hookBc() { echo "$2" | (cd "$base/bob" && CREW_DATA="$base/data-bob" CREW_NO_NOTIFY=1 "$BIN" hook "$1" --agent codex); }
patch='*** Begin Patch\n*** Add File: db/migrations/003_codex.sql\n+select 1;\n*** End Patch'
j='{"session_id":"c1","cwd":"'"$B_"'/bob","tool_name":"apply_patch","tool_input":{"command":"'"$patch"'"}}'
out=$(hookBc pre-edit "$j")
check "codex: a patch into a reserved folder is denied (codex has no ask)" "$out" '"permissionDecision":"deny"'
check "codex: the denial explains how the human can allow it" "$out" "type override"
B send override "Bob's human agreed with Alice" --files db/migrations/003_codex.sql >/dev/null
out=$(hookBc pre-edit "$j")
if echo "$out" | grep -q permissionDecision; then ko "codex: after the human's override the patch goes through" "$out"; else ok "codex: after the human's override the patch goes through"; fi
check "codex: the teammate is told about the override" "$(A board)" "overrides your reservation"
hookBc session-start '{"session_id":"c2","cwd":"'"$B_"'/bob","source":"startup"}' >/dev/null
echo '{"session_id":"c2","cwd":"'"$B_"'/bob"}' | (cd "$base/bob" && CREW_DATA="$base/data-bob" CREW_NO_NOTIFY=1 "$BIN" hook watch --agent codex 2>/dev/null) &
cw=$!
sleep 5
A send question "Codex side: ok to merge the login refactor today?" >/dev/null
for i in $(seq 1 40); do kill -0 $cw 2>/dev/null || break; sleep 1; done
wait $cw; code=$?
[ "$code" = 0 ] && ok "codex: the idle watcher notifies and exits without waking (exit 0)" || ko "codex watcher exit" "$code"
out=$(hookBc prompt '{"session_id":"c2","cwd":"'"$B_"'/bob"}')
check "codex: the question reaches Codex with the next prompt" "$out" "ok to merge the login refactor"

out=$(A send question "SUPABASE_URL=https://x.supabase.co
SUPABASE_KEY=abc123" 2>&1)
check "env-file content is refused" "$out" ".env content"
out=$(A send question "$(printf 'x%.0s' $(seq 1 230))" 2>&1)
check "messages longer than 200 characters are refused" "$out" "limit is 200"
lim=""
for i in 1 2 3 4 5 6; do r=$(A send question "q$i" 2>&1); case "$r" in *"limit of 6"*) lim=yes; break;; esac; done
[ -n "$lim" ] && ok "an agent can send at most 6 messages per hour" || ko "agent message budget" "$r"
check "the limit says when the next message can go" "$r" "next one can go in"
out=$(B send answer "Alice reviews it" --to "Alice" 2>&1; A send answer "yes, after Friday" --to "Bob" 2>&1)
check "answers have their own budget, so news can't block a reply" "$out" "Sent."

# applying a migration is announced by crew itself
hookA post-migrate '{"session_id":"a1","cwd":"'"$B_"'/alice","tool_name":"mcp__supabase__apply_migration","tool_input":{"name":"add_login_tokens","query":"select 1"}}'
sleep 3
check "an applied migration is announced to the team automatically" "$(B board)" "applied migration add_login_tokens"

out=$(cd "$base" && git init -q pub && cd pub && git -c user.name=P -c user.email=p@x commit -q --allow-empty -m x && git remote add origin https://github.com/git/git.git && CREW_DATA="$base/data-alice" "$BIN" hook session-start <<<"{\"session_id\":\"p\",\"cwd\":\"$B_/pub\",\"source\":\"startup\"}")
check "public repositories stay off by default" "$out" "public repository"

out=$(cd "$base" && git clone -q --depth 1 "file://$base/alice" shallow 2>&1; cd "$base/shallow" && git remote set-url origin "$remote" && CREW_DATA="$base/data-alice" "$BIN" hook session-start <<<"{\"session_id\":\"s\",\"cwd\":\"$B_/shallow\",\"source\":\"startup\"}")
check "shallow clones stay off and say why" "$out" "shallow clone"

echo; echo "$pass passed, $fail failed"
rm -rf "$base"
[ $fail = 0 ]
