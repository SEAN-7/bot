---
name: telegram
description: Read and send messages in the human's separate Telegram conversation and handle incoming-message notifications.
---

Telegram is its own conversation. Read messages using `bot-app telegram read '{}'`
and reply using `bot-app telegram send '{"text":"Your reply","replyTo":123}'`.
Run these commands through shell. Never redirect a Telegram reply into `chat send`.
Ordinary model output is private; sending through this app communicates with the human.

Incoming Telegram messages produce durable notifications containing chat/message IDs,
not message content. Read the app inbox to obtain the actual human message. The app
stores its received messages, Telegram IDs, pairing and delivery cursors in
`workspace/telegram`; the core conversation stores notifications and execution history.
Telegram remains the user's conversation UI. Terminal chat is independent.

`read` returns the latest 20 messages by default; `after` and `limit` page forward.
Only messages received since this app was connected are available. The Bot API
does not supply arbitrary older history. Attachment metadata is listed; this first
version reads captions/text but does not transcribe or download attachments.

`send` accepts plain text up to 4096 characters. Split longer replies into separate
shell actions. Use `replyTo` to make the target message clear. The app restricts
communication to the private chat paired by the operator. Unknown send outcomes
are recorded and never automatically retried; inspect the real Telegram chat first.
Use `bot-app telegram status '{}'` to inspect receiver and delivery health.

The controller runs `watch` continuously via its `services` setting. Notifications
arrive while the bot is busy as well as idle and are processed by its existing
serialized queue. Receiving messages does not itself call a model when idle.
Credentials must be configured by the operator; never print the credential file.
