---
name: chat
description: Read and reply in the terminal chat app, which owns its conversation state.
---

Use `bot-app chat read '{}'` to read recent human and bot messages. Use `{"after":0,"limit":20}` to read from the beginning, then continue with nextAfter; hasMore indicates another page. Use `bot-app chat send '{"text":"your reply"}'` for progress, questions and final replies. Ordinary model output stays private. Send only messages warranted by the human's work.

The app owns its messages and delivery state in `workspace/chat/chat.sqlite`.
Incoming messages generate reference notifications in the bot's working journal;
read this app to obtain the actual human text. Terminal chat and Telegram are
separate conversations: answer Telegram notifications through the Telegram app.
Chat replies are stored in this app and streamed to connected frontends independently
of the core journal. Reconnecting or restarting preserves app history.

Keep each send in its own shell call; a single action can send one message. A
repeated action ID returns the existing message rather than sending a duplicate.
Read history after an interruption before retrying a send. The controller's `services`
setting runs `chat watch` to deliver saved incoming notifications even if the
frontend closes during submission. A schedule's wake-up is internal work, not
a human chat message. Legacy messages are imported without removing journal history.
