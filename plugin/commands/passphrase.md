---
description: Set a team passphrase so crew works in a public repository
argument-hint: "<passphrase shared with the team outside the repo>"
---
Public repositories are off by default because anyone could derive their room key. If the user gave a passphrase in "$ARGUMENTS", call the `crew_settings` tool with `{"action": "passphrase", "passphrase": "<it>"}`; otherwise ask for one (12+ characters, agreed with the team outside the repository, never committed). Remind them that every teammate must set the same passphrase. Do not repeat the passphrase back in your reply.
