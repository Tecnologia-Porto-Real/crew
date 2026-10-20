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
echo hi > README.md && git add . && git commit -qm init && git remote add origin "$remote"
git clone -q "$base/alice" "$base/bob" && cd "$base/bob" && git remote set-url origin "$remote" && git config user.name "Bob Roe" && git config user.email b@x.y

A() { (cd "$base/alice" && CREW_DATA="$base/data-alice" "$BIN" "$@"); }
B() { (cd "$base/bob" && CREW_DATA="$base/data-bob" "$BIN" "$@"); }
hookA() { echo "$2" | (cd "$base/alice" && CREW_DATA="$base/data-alice" "$BIN" hook "$1"); }
hookB() { echo "$2" | (cd "$base/bob" && CREW_DATA="$base/data-bob" "$BIN" hook "$1"); }

ra=$(A whoami); rb=$(B whoami)
echo "alice: $ra"; echo "bob:   $rb"
[ "$(echo "$ra" | sed 's/.*room=\([0-9a-f]*\).*/\1/')" = "$(echo "$rb" | sed 's/.*room=\([0-9a-f]*\).*/\1/')" ] && ok "both clones derive the same room with zero setup" || ko "same room" "$ra / $rb"

out=$(hookA session-start "{\"session_id\":\"a1\",\"cwd\":\"$B_/alice\",\"source\":\"startup\"}")
check "first teammate sees an empty room" "$out" "no teammates yet"

out=$(hookB session-start "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\",\"source\":\"startup\"}")
check "second teammate sees the first one, no setup" "$out" "Alice#"

A post task "Refactor the login flow" >/dev/null
A reserve --exclusive "db/migrations/**" >/dev/null
out=$(B board)
check "board shows the teammate's task" "$out" "Refactor the login flow"
check "board shows the exclusive reservation" "$out" "db/migrations/\*\* (EXCLUSIVE"

out=$(hookB pre-edit "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\",\"tool_name\":\"Write\",\"tool_input\":{\"file_path\":\"$B_/bob/db/migrations/002_x.sql\"}}")
check "editing a file reserved exclusively asks the human first" "$out" '"permissionDecision":"ask"'

out=$(hookB pre-edit "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\",\"tool_name\":\"Edit\",\"tool_input\":{\"file_path\":\"$B_/bob/src/app.ts\"}}")
if echo "$out" | grep -q permissionDecision; then ko "normal file is not blocked" "$out"; else ok "normal file is not blocked"; fi

j='{"session_id":"b1","cwd":"'"$B_"'/bob","tool_name":"Bash","tool_input":{"command":"f=\"db/migrations/$(date +%Y)_x.sql\" && : > \"$f\""}}'; out=$(hookB pre-bash "$j")
check "a shell command writing into a reserved folder asks the human" "$out" '"permissionDecision":"ask"'
out=$(hookB pre-bash "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"npx supabase migration new add_x\"}}")
check "a migration generator command asks the human" "$out" '"permissionDecision":"ask"'
out=$(hookB pre-bash "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\",\"tool_name\":\"Bash\",\"tool_input\":{\"command\":\"ls db/migrations && cat db/migrations/001.sql\"}}")
if echo "$out" | grep -q permissionDecision; then ko "reading a reserved folder is free" "$out"; else ok "reading a reserved folder is free"; fi

# Bob's session goes idle: the Stop hook waits for the team
errf="$base/watch.err"
echo "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\"}" | (cd "$base/bob" && CREW_DATA="$base/data-bob" "$BIN" hook watch 2>"$errf") &
wpid=$!
sleep 6
A post note "just an FYI, no need to wake anyone" >/dev/null
sleep 8
if kill -0 $wpid 2>/dev/null; then ok "a plain note does not wake an idle session"; else ko "note must not wake" "$(cat "$errf")"; fi
t0=$(date +%s)
A post question "Can I rename users.email to users.login?" >/dev/null
for i in $(seq 1 40); do kill -0 $wpid 2>/dev/null || break; sleep 1; done
wait $wpid; code=$?
t1=$(date +%s)
[ "$code" = 2 ] && ok "a question wakes the idle session (exit 2) in $((t1-t0)) s" || ko "question wakes (exit $code)" "$(cat "$errf")"
check "the wake text carries the question" "$(cat "$errf")" "rename users.email"
check "the wake text carries the earlier note too" "$(cat "$errf")" "just an FYI"
check "the wake text is framed as information, not instructions" "$(cat "$errf")" "not instructions"

out=$(hookB prompt "{\"session_id\":\"b1\",\"cwd\":\"$B_/bob\"}")
if echo "$out" | grep -q "rename users.email"; then ko "delivered once per session" "$out"; else ok "a message is delivered once per session"; fi

# a second open session of Bob gets its own copy
hookB session-start "{\"session_id\":\"b2\",\"cwd\":\"$B_/bob\",\"source\":\"startup\"}" >/dev/null
A post notice "users.email renamed to users.login; types regenerated" >/dev/null
sleep 4
out=$(B board >/dev/null; hookB prompt "{\"session_id\":\"b2\",\"cwd\":\"$B_/bob\"}")
check "every open session receives new messages" "$out" "users.login"

# ---- Codex mode (same engine, Codex hook semantics) ----
hookBc() { echo "$2" | (cd "$base/bob" && CREW_DATA="$base/data-bob" CREW_NO_NOTIFY=1 "$BIN" hook "$1" --agent codex); }
patch='*** Begin Patch\n*** Add File: db/migrations/003_codex.sql\n+select 1;\n*** End Patch'
j='{"session_id":"c1","cwd":"'"$B_"'/bob","tool_name":"apply_patch","tool_input":{"command":"'"$patch"'"}}'
out=$(hookBc pre-edit "$j")
check "codex: a patch into a reserved folder is denied (codex has no ask)" "$out" '"permissionDecision":"deny"'
check "codex: the denial explains how the human can allow it" "$out" "crew_override"
B override "db/migrations/003_codex.sql" "Bob's human agreed with Alice" >/dev/null
out=$(hookBc pre-edit "$j")
if echo "$out" | grep -q permissionDecision; then ko "codex: after the human's override the patch goes through" "$out"; else ok "codex: after the human's override the patch goes through"; fi
check "codex: the teammate is told about the override" "$(A board)" "decided to edit db/migrations/003_codex.sql"
hookBc session-start '{"session_id":"c2","cwd":"'"$B_"'/bob","source":"startup"}' >/dev/null
echo '{"session_id":"c2","cwd":"'"$B_"'/bob"}' | (cd "$base/bob" && CREW_DATA="$base/data-bob" CREW_NO_NOTIFY=1 "$BIN" hook watch --agent codex 2>/dev/null) &
cw=$!
sleep 5
A post question "Codex side: ok to merge the login refactor today?" >/dev/null
for i in $(seq 1 40); do kill -0 $cw 2>/dev/null || break; sleep 1; done
wait $cw; code=$?
[ "$code" = 0 ] && ok "codex: the idle watcher notifies and exits without waking (exit 0)" || ko "codex watcher exit" "$code"
out=$(hookBc prompt '{"session_id":"c2","cwd":"'"$B_"'/bob"}')
check "codex: the question reaches Codex with the next prompt" "$out" "ok to merge the login refactor"

out=$(A post note "SUPABASE_URL=https://x.supabase.co
SUPABASE_KEY=abc123" 2>&1)
check "env-file content is refused" "$out" ".env content"

out=$(cd "$base" && git init -q pub && cd pub && git -c user.name=P -c user.email=p@x commit -q --allow-empty -m x && git remote add origin https://github.com/git/git.git && CREW_DATA="$base/data-alice" "$BIN" hook session-start <<<"{\"session_id\":\"p\",\"cwd\":\"$B_/pub\",\"source\":\"startup\"}")
check "public repositories stay off by default" "$out" "public repository"

out=$(cd "$base" && git clone -q --depth 1 "file://$base/alice" shallow 2>&1; cd "$base/shallow" && git remote set-url origin "$remote" && CREW_DATA="$base/data-alice" "$BIN" hook session-start <<<"{\"session_id\":\"s\",\"cwd\":\"$B_/shallow\",\"source\":\"startup\"}")
check "shallow clones stay off and say why" "$out" "shallow clone"

echo; echo "$pass passed, $fail failed"
rm -rf "$base"
[ $fail = 0 ]
