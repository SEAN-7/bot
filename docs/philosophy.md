# Project philosophy

We are building a persistent bot that can develop its own capabilities while
retaining a small, protected foundation. It should evolve through use: the user
and bot work together in one continuous conversation, and the bot can build shell-accessible applications and change its behavior as their needs become clearer.

## One continuous conversation

The bot has one durable primary conversation. Closing chat, restarting the
service, changing models, or replacing applications should preserve that continuity.
The complete journal remains available even when the model needs a shorter
context. Accepted messages and work belong to the persistent bot, independently
of any particular frontend connection.

This is the bot's working conversation, not the storage for every external chat.
Communication applications own their conversation history and delivery state.
Terminal chat and Telegram are separate conversations. Incoming messages wake
the bot with durable reference notifications; the bot reads and replies through
the relevant application's CLI. Keep channel message storage out of the core.

## A minimal, unopinionated core

The core provides the reliable machinery: durable history, a single event queue,
controlled execution, protected installation files, and recovery. Keep it small
and understandable. Decisions about memory, scheduling, context management,
application discovery, model access, chat, and working strategy belong in replaceable applications.

Default applications are useful starting points. They should be replaceable through
the same interfaces available to new applications. A particular model, memory
strategy, or task workflow should not become a permanent assumption of the core.

## The bot can change how it works

Self-development is part of the purpose of the project. The bot can edit application
programs, build new capabilities, and install or upgrade ordinary CLI tools. This lets useful
behavior emerge from actual work without requiring every future capability to
be designed into the application in advance.

Capabilities are ordinary applications paired with skills and invoked through one supervised shell tool. All replaceable behavior, including the conversation controller, runs in separate application processes. The core has no plugin loader or registration API. Applications and their guides should evolve together.

The protected core remains outside the bot's editing authority. Operator-managed
core updates are separate from bot-managed application changes.

## Experimentation with recovery

Ordinary applications behave like third-party CLI tools. Edits and upgrades take
effect on their next invocation, without mandatory validation, activation or
probation. Testing is an engineering choice appropriate to the change. Existing
processes use their loaded code until restarted. Preserve persistent data and
credentials; retain useful previous versions without making every app edit a
whole-bot update.

Replacing the running conversation controller or its startup configuration is a
separate operation. Switch it at a turn boundary, check startup and retain the
previous controller revision for recovery without losing the conversation.

Recovery must be honest about its limits. Restoring application state cannot undo an
external action. Record tool starts and outcomes durably, and inspect interrupted
actions with unknown outcomes before repeating them.

## Always available, with purposeful activity

The service lives independently of the chat window. User messages and configured
stimuli can wake the same conversation; idle time alone does not require a model
call. Scheduling and decisions about continued work remain replaceable behavior.

The agent is tool-only. Notifications and tool results enter its next model
context as soon as they are available. Human replies use communication tools;
plain assistant text does not finish an objective. The core does not classify
tasks as complete. With no new stimulus, it stays idle without a wait tool.

## How to judge a change

Ask: does this change protect continuity, execution, or recovery, or is it behavior
that should remain replaceable? Prefer the smallest practical mechanism that
meets the need. Keep behavioral policy in applications and preserve the bot's ability
to evolve without expanding the protected core unnecessarily.
