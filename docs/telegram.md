# Telegram app

Telegram is a separate conversation. The app owns its inbox, paired account,
receive cursor and send receipts in `workspace/telegram/telegram.sqlite`.
Credentials live in `workspace/telegram/credentials.json` with mode 0600.
Terminal chat uses its own `workspace/chat/chat.sqlite`. Neither channel forwards
its replies to the other; the core journals reference notifications and bot work.

Create a bot with [BotFather](https://t.me/BotFather), then configure its token
on the VM using the app's operator command (the prompt hides the token):

```sh
ssh -t bot 'sudo -u bot /opt/bot/current/runtime/node/bin/node /opt/bot/current/app/apps/telegram/main.mjs configure --home /var/lib/bot/.bot'
```

Enable the `telegram` draft app, add `telegram` to `apps.context.projections`
and `["bot-app","telegram","watch"]` to `apps.agent.services`, and apply the controller startup configuration or deploy through the managed
installer. Keep the corresponding chat projection and watch command for the terminal app. Configuration returns
a one-time private pairing link that expires in 24 hours. Open it and press Start
from the Telegram account that should control the bot. Only that private account
can wake the bot; group chats and other users are ignored. Re-running configure
renews an unused pairing link. It does not rebind an already paired account.

For mobile clients where a pairing command is difficult to copy, the operator
can authorize the human's exact Telegram username instead:

```sh
sudo -u bot /opt/bot/current/runtime/node/bin/node /opt/bot/current/app/apps/telegram/main.mjs authorize --home /var/lib/bot/.bot --username HUMAN_USERNAME
```

The next genuine private message from that username establishes its immutable
Telegram user/chat IDs. Other accounts cannot pair through this authorization.

The controller starts the long-polling receiver as an ordinary background app.
Incoming messages are stored before notification delivery; stable Telegram update
IDs deduplicate retries. A receive cursor advances only after durable acceptance.
Saved terminal inputs also survive the frontend closing before notification delivery.
Channel state persists independently of app revisions and core rollback.

Use `bot-app telegram read '{}'`, `bot-app telegram send '{"text":"reply","replyTo":123}'`
and `bot-app telegram status '{}'` through the bot's shell. Plain-text replies are
limited to 4096 characters per send. Uncertain send outcomes are recorded and
never blindly repeated. This version supports text/captions and attachment metadata.
It does not download or transcribe media. The Bot API supplies new updates, not
arbitrary old chat history; the inbox retains messages received since connection.
Unreceived Telegram updates expire after 24 hours according to the Bot API.

The operator's `inspect --home ...` command performs read-only Telegram identity
and webhook checks and reports receiver state. Existing webhooks are never deleted
automatically. Production verification must not generate messages in the real
bot conversation. Use isolated homes and fake API responses for conversation tests.
