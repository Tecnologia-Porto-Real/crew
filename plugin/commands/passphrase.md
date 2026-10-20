---
description: Set a team passphrase so crew works in a public repository
---
Public repositories are off by default because anyone could derive their room key. A team passphrase fixes that,
but it must never pass through this chat: do not ask for it and do not accept it here.

Tell the user, in their language, to run this command in their own terminal, inside this repository, type the
passphrase agreed with the team outside the repository (12+ characters), and then open a new Claude session.
Every teammate must set the same passphrase. Show the command exactly as below, in a code block:

```
git -c 'alias.crew-run=!sh' crew-run "${CLAUDE_PLUGIN_ROOT}/runtime/run.sh" passphrase
```
