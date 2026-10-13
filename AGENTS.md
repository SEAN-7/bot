# Project instructions

Read [the spec](bot/docs/spec.md) and [the philosophy](bot/docs/philosophy.md) before
making design or implementation changes. Keep the harness small: if the agent
could build something as a script, it does not belong in the harness.

The harness lives in `bot/`, the Go module root; the website lives in
`website/`. Paths below are relative to `bot/`, and Go commands run there.

## Workflow

- Go standard library only. Run `go vet ./...` and `go test ./...` before
  committing; CI also runs the root privilege tests and the reproducible
  release check (`scripts/release_test.sh`).
- Work on `main` directly; don't create feature branches.
- Commit and push completed changes by default. Verify the working tree is
  clean and the branch matches its remote before reporting completion.
- Record agent-facing changes in `internal/notes/notes.md` under the release's
  version (facts, not instructions). The file is embedded and `agent notes`
  shows it. Mirror anything an agent should know in the seed README.
- Releases: run `scripts/release.sh X.Y.Z`, commit the updated
  `plugins/index.json` and `install.sh`, then tag `vX.Y.Z` and push the tag.
  The release workflow rebuilds and refuses to publish if any checksum differs.
- Deploy to the VM (SSH host `bot`) by running the release's `install.sh`
  there; on an existing installation it upgrades in place and keeps the
  agents' files, journals and credentials. An installation older than 0.4.1
  upgrades with the v0.4.1 `install.sh` first.
- Verify service health after deploying (`systemctl is-active agent`,
  `journalctl -u agent`) with read-only checks.
- Keep the cached prompt prefix stable: the system prompt, the `sh` tool
  definition and how plugins build requests. Changing any of them rebuilds
  every agent's prompt cache once; change them rarely and deliberately.
- Never send synthetic messages, test prompts or diagnostic instructions to
  the live agent. Messages labelled as the owner's must come from the human.
  Run tests in temporary roots; the VM has no Go, so cross-compile test
  binaries (`GOOS=linux go test -c`) and set `REPLAY_BIN` when running them
  there, using a throwaway user and directory.
