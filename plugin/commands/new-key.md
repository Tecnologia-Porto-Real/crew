---
description: Create a new room key so people who lost access to the repo stop seeing new team messages
---
Explain in two lines that this creates `.claude/crew.key`, which must be committed and pushed, and that teammates join the new room after they pull. If the user confirms, call the `crew_settings` tool with `{"action": "new_key"}` and show the result. Do not commit or push unless the user asks.
