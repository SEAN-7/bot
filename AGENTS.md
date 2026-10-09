# Project philosophy

Read [the project philosophy](docs/philosophy.md) before making design or implementation changes. Preserve one durable conversation, keep the protected core minimal and unopinionated, and put evolving behavior in replaceable modules. Bot-managed changes must preserve continuity and recovery.

# Project workflow

- Commit and push all completed repository changes to GitHub by default. Verify the working tree is clean and the local branch matches its remote before reporting completion.
- Deploy completed changes to the bot VM by default. The SSH target is `bot` in the operator's SSH configuration.
- Build and validate the Linux bundle, then use the managed installer to update the VM. Preserve conversation data, credentials, and the previous release for rollback.
- Verify service health and the changed behavior on the VM before reporting completion. State explicitly if deployment or verification is blocked.
- Never send synthetic deployment checks, test prompts, or diagnostic instructions to the real bot conversation. Messages shown as the user must come from the human. Run conversation and reconnect tests with a temporary, isolated bot home; use read-only production health checks. Operator core updates complete startup validation without conversation traffic; bot-managed controller trials settle through genuine work.
