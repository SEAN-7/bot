# Go Agent Harness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Node bot in this repo with a minimal, always-on Go agent harness whose only model tool is the shell, with provider access through plugins.

**Architecture:** A root daemon scans an agent directory tree once a second and runs one loop per agent. All file access inside agent directories goes through an unprivileged helper process (`agent _fsio`). Shell commands run as the `agent` user, and provider plugins run as `agent-provider`. Each run is journaled as root-owned JSONL. Plugins are separate static binaries speaking a versioned JSON protocol over stdin/stdout.

**Tech Stack:** Go (standard library only, toolchain pinned in `go.mod`), POSIX sh for `install.sh` and `scripts/release.sh`, GitHub Actions.

**Spec:** `docs/spec.md`. Read it before starting any task; this plan argues from it.

## Global Constraints

- Go standard library only. No third-party modules anywhere, including plugins and tests.
- Module path `github.com/SEAN-7/bot`. Toolchain pinned with a `toolchain go1.27.0` line in `go.mod`.
- Static builds: `CGO_ENABLED=0 go build -trimpath -ldflags "-buildid= -X github.com/SEAN-7/bot/internal/version.Version=$VERSION"` for linux/amd64 and linux/arm64.
- Defaults: poll 1s, command timeout 120s, retry 2s doubling to a 10m ceiling, `~/ERROR` after 3 consecutive provider failures, output clip 16 KiB (first 8 KiB + last 8 KiB), inbox file clip 64 KiB, `max_agents` 16, repository `SEAN-7/bot`.
- Exact strings live in `internal/daemon/messages.go` (Task 7) and must match the spec verbatim:
  - System prompt: the spec's "System prompt (verbatim)" section.
  - Interrupted: `You were interrupted. Resume from TASK.md.`
  - Reset: `Context reset. Continue from TASK.md. Previous journal: <path>`
  - Heartbeat: `Heartbeat: anything pending?`
- The daemon never opens a path inside an agent directory itself. Every such access goes through `fsio.Client`.
- No `max_steps` anywhere.
- Every commit message ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Push after each task (AGENTS.md workflow).
- The Node code stays in place until Task 17. Go ignores it.
- Tests never touch the live VM agent. Root tests use a temporary root and a throwaway user.

## Review Focus

1. **A command leaves a background child holding stdout** (`sleep 300 & echo started`). The command should return as soon as the shell exits, not hang until the timeout. Pinned in Task 3 (`TestBackgroundChildDoesNotBlock`).
2. **Binary or invalid UTF-8 output or inbox content** (`head -c 4096 /bin/ls`). The run should continue and the model should receive valid UTF-8, with no rune split by clipping. Pinned in Task 3 (`TestClipKeepsValidUTF8`, `TestBinaryOutput`).
3. **A child directory created before its `config.json` exists, or with half-written JSON.** This should be a normal config error (ERROR file, parent told once, retried when the file changes), never a crash or a busy loop. Pinned in Task 6 (`TestParseConfigInvalid`) and Task 9 (`TestChildWithoutConfigWaits`).
4. **The agent breaks harness paths.** It deletes or replaces `~/journal`, or makes `~/ERROR` a symlink to a privileged file. The daemon should repair the symlink, and writes should replace the link rather than its target. Pinned in Task 7 (`TestJournalSymlinkRepaired`) and Task 11 (`TestPrivilegeErrorSymlink`).
5. **An owner message is replayed by copying it back into the inbox** from `inbox/processed/`. It should be labelled `data` the second time. Pinned in Task 10 (`TestReplayedOwnerFileIsData`).

---

## File Structure

```
go.mod
cmd/agent/main.go                 subcommand dispatch, incl. hidden _fsio and _seed
internal/version/version.go       Version var (ldflags)
internal/layout/layout.go         VM paths and settings.json
internal/proto/proto.go           plugin protocol v1 types, ShTool, plugin-side Serve
internal/osuser/osuser.go         user lookup → syscall.Credential
internal/shell/shell.go           run one command (timeout, process group, clip)
internal/fsio/server.go           helper side: JSON-lines file operations
internal/fsio/client.go           daemon side: Start, typed methods
internal/journal/journal.go       per-run JSONL writer, Latest
internal/plugin/runner.go         execute plugins as provider user with credentials
internal/plugin/config.go         config.json parse + validation against describe
internal/plugin/env.go            KEY=value credentials files
internal/daemon/daemon.go         Config, Run: poll loop, discovery, owner spool, max_agents
internal/daemon/agent.go          per-agent loop: wake, config gate, removal
internal/daemon/run.go            one run: context, steps, thresholds, retries
internal/daemon/inbox.go          collect/label/move inbox files, note writing
internal/daemon/owner.go          Delivered record (delivered.jsonl)
internal/daemon/messages.go       exact strings
internal/daemon/*_test.go         replay-driven integration tests
internal/seed/seed.go, README.md, TASK.md   embedded seed files
internal/cli/send.go, log.go, describe.go, install.go
internal/release/download.go      GitHub API asset download
plugins/index.go, index.json      embedded plugin index
plugins/replay/main.go
plugins/anthropic/main.go
plugins/openai-compatible/main.go
scripts/release.sh
install.sh
.github/workflows/ci.yml, release.yml
README.md, AGENTS.md, docs/philosophy.md
```

---

### Task 1: Module, layout and protocol types

**Files:**
- Create: `go.mod`, `internal/version/version.go`, `internal/layout/layout.go`, `internal/proto/proto.go`
- Test: `internal/layout/layout_test.go`, `internal/proto/proto_test.go`

**Interfaces:**
- Produces:
  - `version.Version string` (default `"dev"`).
  - `layout.Layout{Home, Etc, Lib, Log string}`.
  - `layout.Default() Layout` returns `/home/agent`, `/etc/agent`, `/var/lib/agent`, `/var/log/agent`.
  - Layout methods: `Plugins()` (`Etc/plugins`), `Credentials()` (`Etc/credentials`), `SettingsFile()`, `OwnerSpool()` (`Lib/owner`), `DeliveredFile()`, `ProvidersCache()` (`Lib/providers.json`) and `JournalDir(agentDir string) string` (`filepath.Join(Log, agentDir)`).
  - `layout.Settings{MaxAgents int "max_agents"; Repository string "repository"}` and `layout.LoadSettings(path) (Settings, error)`. A missing file or zero fields get the defaults 16 and `SEAN-7/bot`.
  - `proto` types, with exactly the spec's JSON field names: `Version = 1`, `Segment{Text}`, `Tool{Name, Description, InputSchema json.RawMessage "input_schema"}`.
  - `Message{Role, Content, Text string; Command *string; CallID string "call_id"; Opaque json.RawMessage}`.
  - `Usage{InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens int}` with `Total() int` (the sum).
  - `CompleteRequest{Protocol, Type, Model, Options map[string]any, System []Segment, Tools []Tool, Messages []Message}`.
  - `CompleteResponse{Protocol, Text, Command *string, Usage, Opaque, Error *Error}` and `Error{Message}`.
  - `Setting{Name, Description, Secret}`, `Option{Description, Values []any, Type string, Min, Max *int}`, `Model{ID, ContextTokens int "context_tokens", Options map[string]Option}`, and `DescribeResponse{Protocol, Settings, Models, Error}`.
  - `proto.ShTool() Tool`: name `sh`, schema `{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`.
  - `proto.Handler` interface: `Describe() DescribeResponse` and `Complete(context.Context, CompleteRequest) CompleteResponse`.
  - `proto.Serve(h Handler)`: reads one request from stdin, dispatches on `type`, writes one response with `Protocol` set, and exits 0. An unknown `type` or protocol gets an `Error` response.

- [ ] **Step 1: Write failing tests**

```go
func TestLoadSettingsDefaults(t *testing.T) { // missing file
	s, err := layout.LoadSettings(filepath.Join(t.TempDir(), "settings.json"))
	// err == nil, s.MaxAgents == 16, s.Repository == "SEAN-7/bot"
}
func TestJournalDir(t *testing.T) {
	// layout.Default().JournalDir("/home/agent/agents/r") == "/var/log/agent/home/agent/agents/r"
}
func TestUsageTotal(t *testing.T) { // Usage{1,2,3,4}.Total() == 10
}
func TestShToolSchema(t *testing.T) { // ShTool().Name == "sh"; schema requires "command"
}
func TestMessageRoundTrip(t *testing.T) {
	// marshal Message{Role:"assistant",Command:&cmd,CallID:"c1",Opaque:`{"x":1}`} → unmarshal → equal;
	// JSON contains "call_id" and "opaque"; a nil Command omits "command"
}
func TestServeRejectsUnknownProtocol(t *testing.T) {
	// serveIO(h, strings.NewReader(`{"protocol":2,"type":"describe"}`), &out) → out has error.message
}
```
Expose `proto.ServeIO(h Handler, r io.Reader, w io.Writer) error` for testing; `Serve` wraps it with stdin/stdout.

- [ ] **Step 2: Run tests and verify they fail**

Run: `go test ./internal/...`
Expected: FAIL (packages missing).

- [ ] **Step 3: Implement the files above.** `go.mod` declares `module github.com/SEAN-7/bot`, `go 1.27` and `toolchain go1.27.0`.

- [ ] **Step 4: Run tests and verify they pass**

Run: `go vet ./... && go test ./internal/...`
Expected: PASS

- [ ] **Step 5: Commit and push** ("Add Go module, layout and plugin protocol types")

---

### Task 2: Replay plugin

**Files:**
- Create: `plugins/replay/main.go`
- Test: `plugins/replay/main_test.go`

**Interfaces:**
- Consumes: `proto.Handler`, `proto.ServeIO`.
- Produces: the binary `replay`, used by every daemon test. The script format is used by Task 7 onwards.
  - `REPLAY_SCRIPT`: JSONL. Line 1 is a header `{"models":[proto.Model...]}`. Each later line is a step `{"match":"optional substring","text":"...","command":"...","usage":{...},"opaque":...,"error":"optional message"}`.
  - `REPLAY_LOG`: JSONL. One line per complete call: `{"index":N,"request":CompleteRequest}`, where `index` is the step consumed and -1 when none.
  - `describe`: returns the header's models, plus `Settings` for `REPLAY_SCRIPT` and `REPLAY_LOG`.
  - `complete`: picks the first step not yet consumed (consumed means its index appears in `REPLAY_LOG`) whose `match` is empty or appears in the request JSON. It logs the request, then returns the step. A step with `error` returns `Error`. With no step left, it returns empty text, no command and zero usage.

- [ ] **Step 1: Write failing tests** (drive `handler` through `proto.ServeIO` with env set via `t.Setenv`)

```go
func TestDescribeFromHeader(t *testing.T)      // models[0].ID == "replay-1", ContextTokens == 1000
func TestStepsConsumedInOrder(t *testing.T)    // two calls → commands "echo 1", "echo 2"; log has indexes 0,1
func TestMatchSelectsStep(t *testing.T)        // step0 match "child-task", step1 no match; request without "child-task" gets step1
func TestErrorStep(t *testing.T)               // resp.Error.Message == "boom"; the step is still consumed
func TestExhaustedEndsRun(t *testing.T)        // resp.Command == nil, resp.Text == ""
```

- [ ] **Step 2: Run** `go test ./plugins/replay/`. Expected: FAIL.
- [ ] **Step 3: Implement** `main()` (calls `proto.Serve(handler{})`) and `handler`, opening `REPLAY_LOG` with `O_APPEND`.
- [ ] **Step 4: Run** `go test ./plugins/replay/`. Expected: PASS.
- [ ] **Step 5: Commit and push** ("Add replay provider plugin")

---

### Task 3: User lookup and shell runner

**Files:**
- Create: `internal/osuser/osuser.go`, `internal/shell/shell.go`
- Test: `internal/shell/shell_test.go`

**Interfaces:**
- Produces:
  - `osuser.Lookup(name string) (*syscall.Credential, error)`. It returns uid, gid and supplementary groups via `os/user`, and `(nil, nil)` when `name == ""` (meaning run as the current user).
  - `shell.Options{Dir, Home string; Env []string; User *syscall.Credential; Timeout time.Duration}`.
  - `shell.Result{Output string; ExitCode int; TimedOut bool}`.
  - `shell.Run(ctx context.Context, command string, o Options) (Result, error)`. It runs `/bin/bash -c` when `/bin/bash` exists, otherwise `/bin/sh -c`. It sets `Setpgid` and `Credential`, stdin `/dev/null`, combined stdout and stderr, and `cmd.WaitDelay = 1s`. On timeout or ctx cancel it sends SIGKILL to `-pgid`. The output is converted with `strings.ToValidUTF8(…, "�")`.
  - `shell.Env(home string, uid int) []string` returns `HOME`, `USER=agent`, `LOGNAME=agent`, `PATH=<home>/bin:/usr/local/bin:/usr/bin:/bin`, `XDG_RUNTIME_DIR=/run/user/<uid>`, `DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/<uid>/bus` and `TERM=dumb`.
  - `shell.Clip(s string, limit int) (out string, clipped bool)`. Under the limit it returns `s` unchanged. Over it, it returns the first `limit/2` bytes, then the marker `\n[... N bytes clipped ...]\n`, then the last `limit/2` bytes. Both cuts move inward to rune boundaries.

- [ ] **Step 1: Write failing tests**

```go
func TestRunCapturesOutputAndExit(t *testing.T)       // "echo out; echo err >&2; exit 3" → Output has both, ExitCode 3
func TestRunUsesHomeAndDir(t *testing.T)               // "pwd; echo $HOME" with Dir=Home=tmp → both lines == tmp
func TestTimeoutKillsProcessGroup(t *testing.T)        // Timeout 300ms, "sleep 30 & sleep 30; echo never" → TimedOut, returns < 2s, and the background sleep is gone (pgrep -g pgid finds nothing)
func TestBackgroundChildDoesNotBlock(t *testing.T)     // "sleep 30 & echo started" → returns < 3s, Output contains "started", !TimedOut
func TestCancelKillsCommand(t *testing.T)              // cancel ctx after 100ms on "sleep 30" → returns < 2s
func TestBinaryOutput(t *testing.T)                    // "printf '\\377\\376ok'" → utf8.ValidString(Output) && strings.Contains(Output,"ok")
func TestClipUnderLimit(t *testing.T)                  // Clip("abc",10) == ("abc",false)
func TestClipKeepsHeadAndTail(t *testing.T)            // 20000×"a" + "END", limit 16384 → clipped, HasSuffix "END", contains "bytes clipped", len < 16500
func TestClipKeepsValidUTF8(t *testing.T)              // 9000×"é" at limit 16384 → utf8.ValidString(out)
```

- [ ] **Step 2: Run** `go test ./internal/shell/`. Expected: FAIL.
- [ ] **Step 3: Implement** `osuser.Lookup`, `shell.Run`, `shell.Env` and `shell.Clip` per the interfaces above.
- [ ] **Step 4: Run** `go test ./internal/shell/`. Expected: PASS (macOS and Linux).
- [ ] **Step 5: Commit and push** ("Add shell runner with process-group timeout and clipping")

---

### Task 4: Unprivileged file helper

**Files:**
- Create: `internal/fsio/server.go`, `internal/fsio/client.go`
- Test: `internal/fsio/fsio_test.go` (its `TestMain` runs `fsio.Serve(os.Stdin, os.Stdout)` when `os.Args[1] == "_fsio"`)

**Interfaces:**
- Consumes: `osuser.Lookup`.
- Produces:
  - `fsio.Entry{Name string; Type string /* "file"|"dir"|"symlink"|"other" */; Size int64; ModTime time.Time}`.
  - `fsio.Start(self string, user *syscall.Credential) (*Client, error)` runs `self _fsio` with that credential (nil means the current user).
  - Client methods, all safe for concurrent use (one request at a time, under a mutex):
    - `Close() error`
    - `Scan(home string) ([]string, error)`
    - `List(dir string) ([]Entry, error)`
    - `Read(path string, limit int64) ([]byte, int64, error)` returns at most `limit` bytes plus the full size, and follows symlinks.
    - `Write(path string, data []byte) error` writes a dot-prefixed temp file in the same directory, then renames it. Mode 0644.
    - `Rename(from, to string) error`
    - `Mkdir(path string) error` (MkdirAll, 0755)
    - `Remove(path string) error` (RemoveAll)
    - `Symlink(target, path string) error`, which replaces an existing entry
    - `Stat(path string) (Entry, error)` (lstat)
  - Errors for a missing path satisfy `errors.Is(err, fs.ErrNotExist)`.
  - `fsio.Serve(r io.Reader, w io.Writer) error`.
  - Wire format: request `{"op","path","path2","data"(base64),"limit"}`, response `{"err","notexist":bool,...result}`.
  - Scan rule: `home` is always first. Then breadth-first, for each agent dir D, every entry `D/agents/<name>` that is a real directory (not a symlink) and contains an `inbox` directory, sorted by name.

- [ ] **Step 1: Write failing tests** (all as the current user)

```go
func TestWriteIsAtomicAndReadBack(t *testing.T)     // Write then Read → same bytes; List(dir) shows no leftover ".*" temp
func TestReadLimitReportsFullSize(t *testing.T)     // 100-byte file, limit 10 → len 10, size 100
func TestNotExist(t *testing.T)                     // Read(missing) → errors.Is(err, fs.ErrNotExist)
func TestListTypes(t *testing.T)                    // file, dir, symlink → Types "file","dir","symlink"
func TestSymlinkReplaces(t *testing.T)              // existing regular file at path → Symlink replaces it; Stat Type "symlink"
func TestScanDiscovery(t *testing.T)
	// home/agents/a (inbox) ✓, home/agents/a/agents/b (inbox) ✓,
	// home/agents/noinbox ✗, home/agents/link → symlink to a dir with inbox ✗
	// want []string{home, home/agents/a, home/agents/a/agents/b}
func TestConcurrentRequests(t *testing.T)           // 50 goroutines Write+Read distinct files → no errors
```

- [ ] **Step 2: Run** `go test ./internal/fsio/`. Expected: FAIL.
- [ ] **Step 3: Implement** the server and client.
- [ ] **Step 4: Run** `go test ./internal/fsio/`. Expected: PASS.
- [ ] **Step 5: Commit and push** ("Add unprivileged file helper")

---

### Task 5: Journal

**Files:**
- Create: `internal/journal/journal.go`
- Test: `internal/journal/journal_test.go`

**Interfaces:**
- Produces:
  - `journal.Create(dir string, now time.Time) (*Writer, error)`. It runs MkdirAll with 0755 and creates `<now UTC as 20060102T150405.000000000Z>.jsonl` with mode 0644, adding `-1`, `-2` and so on if the name exists.
  - `(*Writer).Path() string`.
  - `(*Writer).Record(typ string, fields map[string]any) error` writes one line with `"t"` (RFC3339Nano UTC), `"type"` and the fields, then fsyncs.
  - `(*Writer).End(reason string) error` writes `{"type":"end","reason":…}` and closes the file.
  - `journal.Latest(dir string) (path string, clean bool, err error)`. It picks the newest `.jsonl` by name. `clean` means the last line has `"type":"end"`. If there are no files, it returns `("", false, nil)`.
  - `journal.MarkInterrupted(path string) error` appends `{"type":"end","reason":"interrupted"}`.

- [ ] **Step 1: Write failing tests**

```go
func TestRecordsAndEnd(t *testing.T)       // Record("assistant",{"text":"hi"}), End("done") → 2 lines; line 2 type "end" reason "done"; file mode 0644
func TestCreateAvoidsCollision(t *testing.T) // two Create with same now → different paths
func TestLatest(t *testing.T)               // none → ("",false); unclean newest → false; after MarkInterrupted → true
```

- [ ] **Step 2: Run** `go test ./internal/journal/`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/journal/`. Expected: PASS.
- [ ] **Step 5: Commit and push** ("Add per-run JSONL journal")

---

### Task 6: Plugin runner and config validation

**Files:**
- Create: `internal/plugin/runner.go`, `internal/plugin/config.go`, `internal/plugin/env.go`
- Test: `internal/plugin/plugin_test.go` (`TestMain` builds `plugins/replay` into a temp dir)

**Interfaces:**
- Consumes: `proto.*`, `osuser.Lookup`.
- Produces:
  - `plugin.Runner{PluginDir, CredDir string; User *syscall.Credential; Timeout time.Duration /* 15m */; Stderr io.Writer}`.
  - `(*Runner).Describe(ctx, provider string) (proto.DescribeResponse, error)`.
  - `(*Runner).Complete(ctx, provider string, req proto.CompleteRequest) (proto.CompleteResponse, error)`. It sets `Protocol`/`Type`. It returns a Go error when the process fails, the JSON is invalid, or the response has an `Error`. The environment is the credentials file's variables plus `PATH=/usr/bin:/bin` and `HOME=/`.
  - `(*Runner).Installed() ([]string, error)`.
  - `plugin.Config{Provider string "provider"; Model string "model"; Options map[string]any "options"}`.
  - `plugin.ParseConfig(data []byte) (Config, error)` uses strict JSON (unknown top-level keys are rejected) and requires provider and model.
  - `plugin.Validate(c Config, installed []string, d proto.DescribeResponse) error`. Messages:
    - `unknown provider "X" (installed: a, b)`
    - `unknown model "M" for provider "P" (available: …)`
    - `unknown option "O" for model "M"`
    - `option "O": value V not allowed (allowed: …)`
    - integers out of `[min,max]`: `option "O": value V out of range [min, max]`
  - `plugin.ContextTokens(d proto.DescribeResponse, model string) int`.
  - `plugin.ParseEnv(data []byte) (map[string]string, error)` and `plugin.FormatEnv(map[string]string) []byte`, handling `KEY=value` lines, `#` comments and blank lines.

- [ ] **Step 1: Write failing tests**

```go
func TestDescribeReplay(t *testing.T)         // creds file replay.env with REPLAY_SCRIPT → models[0].ID "replay-1"
func TestCompleteReplay(t *testing.T)         // step command "echo hi" → *resp.Command == "echo hi"
func TestCompleteErrorIsGoError(t *testing.T) // error step → err != nil containing "boom"
func TestValidate(t *testing.T)               // table: unknown provider/model/option/value/out-of-range → exact messages above; valid → nil
func TestParseConfigInvalid(t *testing.T)     // `{"provider":"replay"` (truncated), `{}`, `{"provider":"r","model":"m","max_steps":5}` → each errors
func TestEnvRoundTrip(t *testing.T)           // ParseEnv(FormatEnv(m)) == m; comments ignored
```

- [ ] **Step 2: Run** `go test ./internal/plugin/`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/plugin/`. Expected: PASS.
- [ ] **Step 5: Commit and push** ("Add plugin runner and config validation")

---

### Task 7: Daemon: basic run, commands and mid-run inbox

**Files:**
- Create: `internal/daemon/daemon.go`, `agent.go`, `run.go`, `inbox.go`, `messages.go`, `harness_test.go`, `run_test.go`

**Interfaces:**
- Consumes: Tasks 1–6.
- Produces:
  - `daemon.Config`, which embeds `layout.Layout` and adds:
    - `AgentUser, ProviderUser string` (`""` means the current user)
    - `Self string`
    - `Poll, CommandTimeout, RetryBase, RetryMax time.Duration`
    - `ErrorAfter int`
    - `OutputClip, InboxClip int`
    - `Logger *log.Logger`
  - `daemon.DefaultConfig(self string) Config` uses the Global Constraints values.
  - `daemon.Run(ctx context.Context, cfg Config) error` returns when ctx is cancelled, after ending runs and closing the helper.
  - `messages.go` constants: `SystemPrompt`, `MsgInterrupted`, `MsgResetFmt`, `MsgWarn80` (`Context is 80% full. Save your state to TASK.md and notes now.`), `MsgTimeoutFmt` (`Command timed out after %s; its process group was killed. Run long jobs with systemd-run --user.`), `MsgOutputSavedFmt` (`[full output: %s]`).
  - `inbox.go`:
    - `noteName(kind string, now time.Time) string` returns `<UTC 20060102T150405.000000000Z>-<kind>.md`.
    - `writeNote(fs *fsio.Client, agentDir, kind, text string) error` writes `inbox/<noteName>` atomically.
    - `collectInbox(...) ([]inboxRecord, error)` lists regular non-dot files in name order and reads each clipped at `InboxClip` (an over-limit file gets its path in the clip marker). It computes the SHA-256 of the full content, records each to the journal and renames it into `inbox/processed/`, suffixing `-1`, `-2` on a name clash. Owner labelling is added in Task 10. Until then, `from` is always `"data"`.
    - `inboxRecord{File string "file"; From string "from"; Content string "content"}`, encoded one JSON object per line.
  - The run's message flow:
    - System segments `[SystemPrompt, README.md, TASK.md]`, read via the helper. A missing file gives an empty segment. Any other read error gives `(README.md could not be read: <err>)`.
    - Messages: user (the records) → assistant → tool → [user (new records)] … .
    - Each step's `call_id` is `call-<n>`.
    - An empty or whitespace-only command counts as no command.
    - Tool content is `exit status N\n<clipped output>`. If clipped, `MsgOutputSavedFmt` is appended with `~/.outputs/<run>-<n>.txt`, written via the helper. On timeout, the content is `MsgTimeoutFmt` followed by the output.
  - Journal records: `start{config}`, `inbox{file,from,size,content}`, `assistant{text,command,call_id,usage,opaque}`, `result{call_id,exit_code,timed_out,output,full_output}`, `note{text}`, `end{reason}`.
  - On every run start, `~/journal` is (re)pointed at `JournalDir(agentDir)` via the helper.
  - `harness_test.go`:
    - `newHarness(t, script ...string) *harness` builds replay once in `TestMain` (which also serves `_fsio`). It creates `root/{home/agent,etc/agent/plugins,etc/agent/credentials,lib,log}`, writes `replay.env` and seeds `home/agent` with README.md, TASK.md and `config.json` (`{"provider":"replay","model":"replay-1"}`). Its config uses Poll 10ms, CommandTimeout 2s, RetryBase 10ms, RetryMax 50ms and ErrorAfter 3.
    - Methods: `start()`, `stop()`, `drop(agentDir, name, text)` (atomic inbox write), `waitEnds(agentDir string, n int) []map[string]any` (waits up to 10s for n ended runs and returns the latest run's records), and `requests() []proto.CompleteRequest`.
    - Script helper: `step(command string, totalTokens int) string` builds a step line whose usage `InputTokens == totalTokens`. `done()` builds a no-command step.

- [ ] **Step 1: Write failing tests** in `run_test.go`

```go
func TestBasicRun(t *testing.T)
	// script: step("echo hello",100), done(); drop "hi.md" "hello there"
	// requests()[0]: System texts == [SystemPrompt, readme, task]; Messages[0].Content has {"file":"inbox/processed/hi.md","from":"data","content":"hello there"}
	// requests()[1]: tool message content starts "exit status 0\nhello"
	// journal end reason "done"; hi.md now in inbox/processed/; ~/journal → JournalDir
func TestOutputClipping(t *testing.T)
	// step("head -c 40000 /dev/zero | tr '\\0' a",100), done()
	// tool content contains "bytes clipped" and "[full output: "; the saved file has 40000 bytes
func TestCommandTimeout(t *testing.T)
	// CommandTimeout 300ms; step("sleep 30",100), done() → tool content starts "Command timed out after 300ms"; run ends "done"
func TestMidRunInboxDelivery(t *testing.T)
	// step("sleep 0.3",100), done(); drop "late.md" while step 1 runs
	// requests()[1].Messages ends with a user message containing "late.md"
func TestWhitespaceCommandEndsRun(t *testing.T)
	// step("   ",100) → exactly 1 request; end reason "done"
func TestDotFilesIgnored(t *testing.T)
	// drop ".partial" only → no run within 200ms; rename to "x.md" → run starts
func TestJournalSymlinkRepaired(t *testing.T)
	// replace ~/journal with a regular file, drop a message → after the run ~/journal is a symlink to JournalDir
```

- [ ] **Step 2: Run** `go test ./internal/daemon/ -run 'Basic|Clipping|Timeout|MidRun|Whitespace|DotFiles|Symlink'`. Expected: FAIL.
- [ ] **Step 3: Implement** `Run` (start the helper, poll, scan, start one goroutine per discovered agent), the agent loop (wake on `collectInbox`-eligible files) and the run steps. Leave the context-size, retry, config-gate, removal, owner and recovery hooks as small named functions for Tasks 8–10.
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit and push** ("Add daemon loop with journaled shell steps")

---

### Task 8: Daemon: context thresholds and provider retries

**Files:**
- Modify: `internal/daemon/run.go`
- Test: `internal/daemon/context_test.go`

**Interfaces:**
- Consumes: Task 7 harness. The replay model has `context_tokens` 1000.
- Produces:
  - After each response, `used := resp.Usage.Total()`.
  - At `used*100 >= 80*ctx`, once per run, `MsgWarn80` is prepended to the next user content. If no inbox files are pending, it's a user message of its own after the tool message.
  - At `used*100 >= 95*ctx`, the run ends with reason `"context"` and `writeNote(…,"reset", fmt.Sprintf(MsgResetFmt, journalPath))`.
  - The first-call guard: if step 1 is at or over 95%, the daemon writes to `~/ERROR` and the log the message `Context is over 95% full at the start of a run (U of C tokens); not starting. Reduce README.md, TASK.md or inbox files.` It writes no reset note, ends the run with `"context-guard"`, and wakes only on new inbox files.
  - Provider failures: the same request is retried with a delay of `RetryBase` doubling to `RetryMax`. After `ErrorAfter` consecutive failures, `~/ERROR` gets `Provider failing since <RFC3339>: <message>` and the log gets the same. On the next success, `~/ERROR` is removed if it holds a provider error. No inbox file is ever written for these.

- [ ] **Step 1: Write failing tests**

```go
func TestWarningAt80(t *testing.T)     // step("true",800), done() → requests()[1] last user message contains MsgWarn80; it appears only once even with a 2nd step at 850
func TestResetAt95(t *testing.T)       // step("true",100), step("true",960) → run ends "context"; inbox has *-reset.md with "Previous journal: " + that run's journal path; next run starts and sees it
func TestFirstCallGuard(t *testing.T)  // step("true",990) → end "context-guard"; ~/ERROR contains "over 95%"; no *-reset.md; no second run within 300ms
func TestProviderRetry(t *testing.T)   // error, error, error, step("echo ok",10), done() → ~/ERROR contained "Provider failing" at some point, is gone after; inbox received no harness notes; the tool result is "exit status 0\nok"
```

- [ ] **Step 2: Run** `go test ./internal/daemon/ -run 'Warning|Reset|Guard|Retry'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/daemon/`. Expected: PASS (including Task 7's tests).
- [ ] **Step 5: Commit and push** ("Add context thresholds and provider retry")

---

### Task 9: Daemon: config gate, subagents, removal and max_agents

**Files:**
- Modify: `internal/daemon/agent.go`, `internal/daemon/daemon.go`
- Test: `internal/daemon/agents_test.go`

**Interfaces:**
- Produces:
  - **Config gate.** At wake, the daemon reads `config.json` via the helper, runs `ParseConfig`, then `Installed`, `Describe` and `Validate`.
    - On failure, it leaves the inbox untouched, writes `~/ERROR` with `config.json: <reason>` and logs it. For a subagent, it calls `writeNote(parent,"subagent-error", "Subagent <rel path> did not start: config.json: <reason>")` once per distinct config content (keyed by SHA-256, in memory). It retries only when the `config.json` content hash changes.
    - On success, it removes `~/ERROR` if that file starts with `config.json:`.
    - A `Describe` process failure counts as a provider failure (Task 8 backoff), not a config error.
  - **Removal.** Each poll, agents missing from `Scan` have their context cancelled. The running command is killed, the journal ends with `"removed"` and the goroutine exits.
  - **max_agents.** `LoadSettings` is read at each poll. Agents beyond `MaxAgents` in scan order are not started. Each gets `~/ERROR` with `Agent limit reached (max_agents=N); this agent was not started.` and a one-time parent note `Subagent <rel> was not started: agent limit reached (max_agents=N).`
  - The parent of `D` is the directory two levels up (`D/../..`), for any `D` other than `Home`.

- [ ] **Step 1: Write failing tests**

```go
func TestConfigValidationFailure(t *testing.T)
	// main config.json model "nope"; drop msg → ~/ERROR contains `unknown model "nope"`; no request made; inbox file still in inbox/
	// rewrite config.json valid → run happens; ~/ERROR gone
func TestChildWithoutConfigWaits(t *testing.T)
	// mkdir home/agents/c/inbox, drop task (no config.json) → c/ERROR contains "config.json"; parent inbox gets exactly one *-subagent-error.md over 300ms
func TestParentChildReport(t *testing.T)
	// script (match-keyed): parent step creates child: "mkdir -p agents/c/inbox && cp config.json agents/c/ && echo 'child-task: say hi' > agents/c/inbox/.t && mv agents/c/inbox/.t agents/c/inbox/task.md" (match "parent-readme"), parent done;
	// child step (match "child-task"): "printf 'report: hi' > ../../inbox/.r && mv ../../inbox/.r ../../inbox/report.md", child done;
	// parent step (match "report: hi"): done
	// home README contains "parent-readme" → parent's final request has a record {"file":"inbox/processed/report.md","from":"data","content":"report: hi"}
func TestRemovalStopsAgent(t *testing.T)
	// child running "sleep 30"; RemoveAll(child dir) → child's journal ends "removed" within 2s
func TestMaxAgents(t *testing.T)
	// settings max_agents 1; child with valid config + task → child/ERROR "Agent limit reached"; no child request
```

- [ ] **Step 2: Run** `go test ./internal/daemon/ -run 'Config|Child|Parent|Removal|MaxAgents'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/daemon/`. Expected: PASS.
- [ ] **Step 5: Commit and push** ("Add config gate, subagent lifecycle and agent limit")

---

### Task 10: Daemon: owner messages and crash recovery

**Files:**
- Create: `internal/daemon/owner.go`
- Modify: `internal/daemon/daemon.go`, `internal/daemon/inbox.go`
- Test: `internal/daemon/owner_test.go`, `internal/daemon/recovery_test.go`

**Interfaces:**
- Produces:
  - `owner.go`:
    - `loadDelivered(path string) (*delivered, error)`.
    - `(*delivered).add(agent, name, sum string) error` appends `{"agent","name","sha256"}`.
    - `(*delivered).take(agent, name, sum string) bool` returns true once, then appends `{"consumed":true,…}`. Consumed records reload as consumed.
  - **Spool delivery.** Each poll, before scan, the daemon handles each regular non-dot file in `OwnerSpool()`, which it reads as root (that path is root-owned). In order: `add(Home, noteName("owner"), sum)`, write `Home/inbox/<that name>` via the helper, then remove the spool file.
  - `collectInbox` sets `From = "owner"` iff `take(agentDir, name, sha256(full content))`.
  - **Recovery.** At `Run` start, after the helper starts and before polling, for each scanned agent: if `journal.Latest(JournalDir(agent))` returns an unclean path, the daemon calls `writeNote(agent,"interrupted",MsgInterrupted)` and then `journal.MarkInterrupted(path)`.

- [ ] **Step 1: Write failing tests**

```go
func TestOwnerMessageLabelled(t *testing.T)     // write spool file "hi" (as cli.Send will) → request record from "owner", content "hi"; spool empty
func TestForgedOwnerIsData(t *testing.T)        // drop "20990101T000000Z-owner.md" directly → from "data"
func TestReplayedOwnerFileIsData(t *testing.T)  // after delivery, copy inbox/processed/<owner file> back to inbox/ with the same name → from "data"
func TestCrashRecovery(t *testing.T)
	// create an unclean journal in JournalDir(home) (start record, no end); start daemon
	// → inbox gets *-interrupted.md with MsgInterrupted; Latest is clean; restarting the daemon again adds no second note
```

- [ ] **Step 2: Run** `go test ./internal/daemon/ -run 'Owner|Forged|Replayed|Recovery'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/daemon/`. Expected: PASS.
- [ ] **Step 5: Commit and push** ("Verify owner messages and recover interrupted runs")

---

### Task 11: Privilege-separation tests and CI

**Files:**
- Create: `internal/daemon/privilege_test.go`, `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: the harness. Root mode sets `AgentUser`/`ProviderUser` to `$AGENT_TEST_USER` (default `agent-test`) and chowns the agent home to that user. Every temp path component must be 0755 so that user can traverse it.
- Produces: `TestPrivilege*` tests. They skip unless `os.Geteuid() == 0 && os.Getenv("AGENT_ROOT_TESTS") == "1"`.

- [ ] **Step 1: Write the tests**

```go
func TestPrivilegeSymlinkedReadme(t *testing.T)
	// root-owned 0600 file with "TOPSECRET"; home/README.md → symlink to it; drop msg
	// run completes; README segment contains "could not be read"; "TOPSECRET" appears in no request (REPLAY_LOG) and no journal file
func TestPrivilegeErrorSymlink(t *testing.T)
	// home/ERROR → symlink to root-owned 0644 file "ORIGINAL"; invalid config → root file still "ORIGINAL"; home/ERROR is now a regular file
func TestPrivilegeJournalNotWritable(t *testing.T)
	// after a run: step command "echo x >> ~/journal/*.jsonl" → exit status != 0; journal unchanged apart from records
func TestPrivilegeCommandsRunAsAgent(t *testing.T)
	// step "id -un" → tool content contains AGENT_TEST_USER
```

- [ ] **Step 2: Write `ci.yml`.** On push and pull_request, on `ubuntu-latest`:
  1. `actions/setup-go` with `go-version-file: go.mod`.
  2. `go vet ./...`
  3. `go test ./...`
  4. `sudo useradd --system --no-create-home agent-test`
  5. `sudo AGENT_ROOT_TESTS=1 "$(which go)" test ./internal/daemon -run Privilege -v`
- [ ] **Step 3: Verify locally** with `go test ./...`. Expected: PASS, with the privilege tests SKIP on macOS.
- [ ] **Step 4: Commit and push**, then check the CI run with `gh run watch`. Expected: all steps pass, and the privilege tests report PASS, not SKIP.

---

### Task 12: Seed files and CLI (send, log, describe, daemon)

**Files:**
- Create: `internal/seed/seed.go`, `internal/seed/README.md`, `internal/seed/TASK.md`, `internal/cli/send.go`, `internal/cli/log.go`, `internal/cli/describe.go`, `cmd/agent/main.go`
- Test: `internal/seed/seed_test.go`, `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: `layout`, `plugin.Runner`, `fsio.Serve`, `daemon.Run`.
- Produces:
  - `seed.Write(dir string, configJSON []byte) error` writes README.md, TASK.md (`No task yet.`) and config.json, plus the empty dirs `inbox/processed` and `agents`. It never overwrites existing files.
  - The seed README covers every bullet in the spec's "Seed files" section and the brief's "SEED README.md" list, phrased as suggestions the agent may revise. That includes `systemd-run --user` for long jobs, `"from": "owner"` as the verified owner label, and `~/archive/old-bot/`.
  - `cli.Send(l layout.Layout, text string, now time.Time) error` requires euid 0 (an error otherwise: `agent send needs root (sudo)`). It writes `OwnerSpool()/.<name>` then renames it to `<ts>.md`.
  - `cli.Format(line []byte) string` renders one journal record as one or more lines:
    - `HH:MM:SS assistant: <text>` and `  $ <command>`
    - `HH:MM:SS result (exit N): <first 20 lines>`
    - `HH:MM:SS inbox <file> [owner|data]`
    - `HH:MM:SS note: …`
    - `HH:MM:SS end (<reason>)`
  - `cli.Log(ctx, l layout.Layout, name string, w io.Writer) error` resolves the agent dir (`Home`, or `Home/agents/<name>`). It prints `<dir>/ERROR` first if present, then follows the newest journal (polling every 500ms) and switches to newer files as they appear.
  - `cli.Describe(ctx, l layout.Layout, r *plugin.Runner, root bool, w io.Writer) error`. As root, it describes every installed plugin and atomically writes `ProvidersCache()` (`{"providers":{"name":DescribeResponse}}`, 0644). Otherwise it reads the cache. It prints `provider / model (N tokens) / option: allowed values`.
  - `cmd/agent/main.go` dispatches:
    - `daemon`
    - `send TEXT`
    - `log [NAME]`
    - `provider describe`
    - `provider install NAME|URL [--sha256 HEX]` (Task 13)
    - `version`
    - `_fsio` (`fsio.Serve`)
    - `_seed DIR` (seed with config read from stdin)
  - `daemon` uses `layout.Default()` and `DefaultConfig(os.Executable())`, and cancels on SIGTERM/SIGINT.

- [ ] **Step 1: Write failing tests**

```go
func TestSeedDoesNotOverwrite(t *testing.T)  // existing README kept; TASK.md == "No task yet.\n"; inbox/processed exists
func TestSeedReadmeCoversTopics(t *testing.T) // README contains "TASK.md", "archive", "journal", "systemd-run --user", "agent provider describe", `"from": "owner"`
func TestSendRequiresRoot(t *testing.T)      // skip if root; error mentions "sudo"
func TestFormat(t *testing.T)                // table: assistant with command, result, inbox owner, end → expected strings
func TestLogFollowsNewRuns(t *testing.T)     // write run1 then run2 into a temp journal dir; Log output (ctx 1.5s) contains both runs' records in order
func TestDescribeReadsCacheAsNonRoot(t *testing.T) // cache file with replay models → output contains "replay-1 (1000 tokens)"
```

- [ ] **Step 2: Run** `go test ./internal/seed/ ./internal/cli/`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./... && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/agent`. Expected: PASS and a successful build.
- [ ] **Step 5: Commit and push** ("Add seed files and agent CLI")

---

### Task 13: Provider install and GitHub download

**Files:**
- Create: `internal/release/download.go`, `internal/cli/install.go`, `plugins/index.go`, `plugins/index.json`
- Test: `internal/release/download_test.go`, `internal/cli/install_test.go`

**Interfaces:**
- Produces:
  - `plugins/index.json`: `{"version":"dev","plugins":{"<name>":{"linux-amd64":"<sha256>","linux-arm64":"<sha256>"}}}`. It is initially empty and filled by Task 16.
  - `plugins.Index() (Index, error)` parses the embedded file.
  - `release.Client{API string /* https://api.github.com */; Token string}`.
  - `(*release.Client).Asset(ctx, repo, tag, name string) ([]byte, error)`:
    - `GET /repos/{repo}/releases/tags/{tag}`, then find the asset by name.
    - `GET /repos/{repo}/releases/assets/{id}` with `Accept: application/octet-stream`.
    - A missing token returns `GITHUB_TOKEN is required to download from the private repository`.
  - `cli.Install(ctx, l layout.Layout, target, sha string, opts InstallOptions) error`, with `InstallOptions{Release *release.Client; Repo, Version, Arch string; ProviderGroup string /* agent-provider */; Prompt func(s proto.Setting) (string, error); Runner *plugin.Runner}`:
    - `cmd/agent` checks euid 0 before calling `Install`. `Install` itself does no check, so tests can call it with a temp layout.
    - A name is fetched as asset `plugin-<name>-linux-<arch>` from tag `v<Version>` and verified against `plugins.Index()`. A URL is fetched with `net/http` and verified against `--sha256`, which is required. On a checksum mismatch it fails with `checksum mismatch for <name>: got X, want Y` and writes nothing.
    - It writes `Plugins()/<name>` with mode 0750 and owner root:ProviderGroup, via temp file and rename.
    - It runs describe. For each setting it uses an existing value from the creds file or the environment, otherwise `Prompt`. Secrets are read from `/dev/tty` with `stty -echo` in production.
    - It writes `Credentials()/<name>.env` with mode 0600, then refreshes `ProvidersCache()`.

- [ ] **Step 1: Write failing tests** (an `httptest.Server` emulating the GitHub API)

```go
func TestAssetDownload(t *testing.T)        // API returns release JSON with asset id 7; asset endpoint checks Accept + Bearer → bytes returned
func TestAssetNeedsToken(t *testing.T)      // Token "" → error mentions GITHUB_TOKEN
func TestInstallVerifiesChecksum(t *testing.T) // wrong index checksum → error "checksum mismatch"; Plugins() dir has no file
func TestInstallWritesPluginAndCreds(t *testing.T) // replay binary served; Prompt returns values → plugin exists, creds env has REPLAY_SCRIPT, cache lists replay-1
func TestInstallURLRequiresSha(t *testing.T) // URL target with sha "" → error mentions --sha256
```

- [ ] **Step 2: Run** `go test ./internal/release/ ./internal/cli/`. Expected: FAIL.
- [ ] **Step 3: Implement**, and wire up `provider install` in `cmd/agent`.
- [ ] **Step 4: Run** `go test ./...`. Expected: PASS.
- [ ] **Step 5: Commit and push** ("Add verified provider install from releases")

---

### Task 14: Anthropic plugin

**Files:**
- Create: `plugins/anthropic/main.go`
- Test: `plugins/anthropic/main_test.go`

**Interfaces:**
- Consumes: `proto.*`.
- Produces:
  - Settings: `ANTHROPIC_API_KEY` (secret) and optional `ANTHROPIC_BASE_URL` (default `https://api.anthropic.com`).
  - `describe` works offline from a built-in model table.

- [ ] **Step 1: Research current docs. Do not rely on memory.** Read the Anthropic models overview, Messages API, tool use, prompt caching and extended thinking / effort pages at docs.claude.com. Record in a comment at the top of `main.go`, with the date:
  - current model IDs and their context windows and max output
  - the `anthropic-version` header
  - the cache-control syntax and breakpoint limit
  - the reasoning parameters each model accepts, with allowed values
  - the parallel-tool-use disable flag

  Build the model table and per-model `Options` from that, including `max_tokens` as an integer option and the reasoning options as enums or ranges.
- [ ] **Step 2: Write failing tests** (an `httptest.Server`)

```go
func TestRequestShape(t *testing.T)
	// 3 system segments → system blocks in order; cache_control on the LAST system block and the LAST content block of the final message; total breakpoints ≤ the documented limit
	// tools == [sh]; tool_choice disables parallel tool use; x-api-key and anthropic-version headers present
func TestMessageMapping(t *testing.T)
	// assistant{call_id c1, command} without opaque → tool_use id c1 input {"command":…}; tool{c1} → user tool_result tool_use_id c1
	// consecutive user-role content is merged into one message
func TestOpaqueRoundTrip(t *testing.T)
	// response with thinking + tool_use blocks → resp.Opaque holds the blocks; sending that assistant message back → the identical blocks in the next request
func TestResponseParsing(t *testing.T)
	// tool_use {"command":"ls"} → *Command "ls"; usage input/output/cache_read/cache_creation → Usage fields
func TestHTTPErrorBecomesError(t *testing.T) // 429 body → resp.Error.Message contains "429"
func TestOptionsPassThrough(t *testing.T)    // options map values land in the request per the documented parameter names
```

- [ ] **Step 3: Run** `go test ./plugins/anthropic/`. Expected: FAIL.
- [ ] **Step 4: Implement.**
- [ ] **Step 5: Run** `go test ./plugins/anthropic/`. Expected: PASS.
- [ ] **Step 6: Commit and push** ("Add Anthropic provider plugin")

---

### Task 15: OpenAI-compatible plugin

**Files:**
- Create: `plugins/openai-compatible/main.go`
- Test: `plugins/openai-compatible/main_test.go`

**Interfaces:**
- Produces:
  - Settings: `OPENAI_BASE_URL`, `OPENAI_API_KEY` (secret), and `OPENAI_MODELS` (comma-separated `id=context_tokens`).
  - `describe` lists those models. Each model gets the options `max_tokens` (integer 1–1000000) and `reasoning_effort` (values `low`, `medium`, `high`).
  - `complete` posts to `{base}/chat/completions`:
    - The system segments are joined with blank lines into one system message.
    - An assistant message replays `opaque` (the raw provider message) when present. Otherwise it is built with `tool_calls[{id:call_id,type:"function",function:{name:"sh",arguments}}]`.
    - A tool message becomes `{role:"tool",tool_call_id,content}`.
    - The request sets `parallel_tool_calls:false`.
    - Usage: `prompt_tokens - cached_tokens` goes to input, `prompt_tokens_details.cached_tokens` to cache read and `completion_tokens` to output.
- [ ] **Step 1: Research** the current OpenAI Chat Completions docs for the tool-call, `parallel_tool_calls`, `reasoning_effort` and usage fields, and adjust the field names above if the docs differ. Record them in a comment with the date.
- [ ] **Step 2: Write failing tests**

```go
func TestDescribeFromSetting(t *testing.T)   // OPENAI_MODELS "a=8000,b=128000" → two models with those context sizes
func TestRequestShape(t *testing.T)          // one system message; tools [sh]; parallel_tool_calls false; Authorization Bearer
func TestToolCallMapping(t *testing.T)       // tool_calls → *Command; tool message tool_call_id round trip
func TestOpaqueReplay(t *testing.T)          // a response message with an extra field "reasoning_content" is replayed verbatim
func TestUsageMapping(t *testing.T)          // prompt 100, cached 40, completion 10 → {60,10,40,0}
func TestBadModelsSetting(t *testing.T)      // "a=notanumber" → describe Error
```

- [ ] **Step 3: Run** `go test ./plugins/openai-compatible/`. Expected: FAIL.
- [ ] **Step 4: Implement.**
- [ ] **Step 5: Run** the same command. Expected: PASS.
- [ ] **Step 6: Commit and push** ("Add OpenAI-compatible provider plugin")

---

### Task 16: Release tooling and installer

**Files:**
- Create: `scripts/release.sh`, `install.sh`, `.github/workflows/release.yml`
- Test: `scripts/release_test.sh` (run in CI)

**Interfaces:**
- Produces:
  - `scripts/release.sh VERSION [--verify]` builds into `dist/`, in this order:
    1. the three plugins for both architectures (`plugin-<name>-linux-<arch>`);
    2. `plugins/index.json`, written from their checksums;
    3. `agent-linux-<arch>`, which embeds the index;
    4. `install.sh`, whose `VERSION=` line and the block between `# BEGIN CHECKSUMS` and `# END CHECKSUMS` (lines like `SHA_agent_linux_amd64=<hex>`) are rewritten;
    5. `dist/SHA256SUMS`.

    It uses `sha256sum` or `shasum -a 256`, whichever exists. With `--verify`, it rewrites nothing and exits non-zero if any committed checksum differs.
  - `install.sh [--sudo]` is POSIX sh. Every prompt reads from `/dev/tty`. It requires root and `GITHUB_TOKEN`, and runs these steps in order:
    1. Detect the architecture (`x86_64`→amd64, `aarch64`→arm64).
    2. Download `agent-linux-<arch>` through the GitHub API (release by tag → asset id → octet-stream), using `sed` to extract the id. Verify it against the pinned checksum, then install it to `/usr/local/bin/agent`.
    3. Create the users `agent` (home `/home/agent`, shell `/bin/bash`) and `agent-provider` (system user, no home, nologin).
    4. Create the directories with the spec's owners and modes, and write `settings.json`.
    5. Ask which plugin to install (anthropic or openai-compatible) and run `agent provider install <name>`.
    6. Show `agent provider describe`, then prompt for the model id.
    7. Run `printf '{"provider":"%s","model":"%s","options":{}}' | sudo -u agent agent _seed /home/agent`.
    8. Run `loginctl enable-linger agent`.
    9. Write the unit file (content below), then run `systemctl daemon-reload` and `systemctl enable --now agent`.
    10. Install the heartbeat crontab for `agent` (content below).
    11. With `--sudo`: write `/etc/sudoers.d/agent` (`agent ALL=(ALL) NOPASSWD:ALL`, mode 0440, checked with `visudo -cf`) and print the warning: `WARNING: the agent has passwordless sudo. Credential, journal and owner-message protections are now conventions only. Set a spend limit at your provider.`
    12. Print next steps: `sudo agent send "..."` and `agent log`.
  - Unit file `/etc/systemd/system/agent.service`:
    ```
    [Unit]
    Description=Agent harness
    After=network-online.target
    Wants=network-online.target

    [Service]
    ExecStart=/usr/local/bin/agent daemon
    Restart=always
    RestartSec=2

    [Install]
    WantedBy=multi-user.target
    ```
  - Crontab line:
    ```
    0 9 * * * f="$HOME/inbox/.heartbeat"; printf 'Heartbeat: anything pending?\n' > "$f" && mv "$f" "$HOME/inbox/$(date -u +\%Y\%m\%dT\%H\%M\%SZ)-heartbeat.md"
    ```
  - `release.yml`: on a `v*` tag, run `scripts/release.sh "${GITHUB_REF_NAME#v}" --verify`, then `gh release create "$GITHUB_REF_NAME" dist/* --title "$GITHUB_REF_NAME" --notes "Agent harness $GITHUB_REF_NAME"`.

- [ ] **Step 1: Write `scripts/release_test.sh`.** It runs `sh -n install.sh`, then `scripts/release.sh 0.0.0-test` twice and asserts that `dist/SHA256SUMS` is identical both times (reproducibility). It then runs `--verify`, which must pass, and finally corrupts one checksum in a copy of `install.sh` and asserts that `--verify` fails. Add this as a step in `ci.yml`.
- [ ] **Step 2: Run** `sh scripts/release_test.sh`. Expected: FAIL (scripts missing).
- [ ] **Step 3: Implement** `release.sh`, `install.sh` and `release.yml`.
- [ ] **Step 4: Run** `sh scripts/release_test.sh`. Expected: PASS. Then restore any test changes to `install.sh` and `plugins/index.json` with `git checkout`.
- [ ] **Step 5: Commit and push** ("Add reproducible release tooling and installer"). Check the CI run.

---

### Task 17: Documentation and removal of the Node bot

**Files:**
- Create or replace: `README.md` (operator guide), `AGENTS.md`, `docs/philosophy.md`
- Delete: everything belonging to the Node bot (`apps/`, `src/`, `install/templates`, `install/*.mjs`, `install/*.json`, `install/bootstrap.sh`, `test/`, `examples/`, `scripts/build-bundle.py`, `package.json`, `package-lock.json`, the old `.github/workflows/release.yml` if still present, and `docs/app-adoption.md`, `contract.md`, `linux-install.md`, `plugins.md`, `skills.md`, `telegram.md`)

**Interfaces:**
- Produces:
  - **Operator `README.md`**, covering:
    - installation (the one-liner from the spec, and how to create a fine-grained read-only token)
    - setting a spend limit on the provider key
    - the first `sudo agent send`
    - `agent log` and `journalctl -u agent`
    - `systemctl stop agent`
    - what is protected and what isn't: subagents are not isolated, the effect of `--sudo`, and `systemd-run --user` jobs surviving restarts
    - an example first message asking the agent to set up a Telegram channel for itself from a bot token
  - **`AGENTS.md`**:
    - Read `docs/spec.md` and `docs/philosophy.md` first.
    - Commit and push by default.
    - Deploy to SSH host `bot` with the release and installer.
    - Never send synthetic messages to the live agent; use temporary roots for tests.
    - Releases go through `scripts/release.sh`.
  - **`docs/philosophy.md`**: about one page restating the three ideas, the guarantee/policy split and how to judge a change.

- [ ] **Step 1: Delete the Node files and write the docs.**
- [ ] **Step 2: Verify:**
  - `git ls-files | grep -E '\.(mjs|js|py)$|package.json'` prints nothing.
  - `go vet ./... && go test ./... && sh scripts/release_test.sh` passes.
  - Every command named in `README.md` exists in `cmd/agent/main.go`.
- [ ] **Step 3: Commit and push** ("Replace the Node bot with the Go agent harness"). Check the CI run.

---

### Task 18: Release, VM migration and verification (with the user)

This task touches the live VM and needs the user at three points: approving the release, uninstalling the old bot, and entering provider credentials (Claude never types real API keys).

- [ ] **Step 1: Prepare the release.** Run `scripts/release.sh 0.1.0`, commit `plugins/index.json` and `install.sh`, and push. Ask the user to approve tagging, then `git tag v0.1.0 && git push origin v0.1.0`, and watch `release.yml` pass.
- [ ] **Step 2: Run the root tests on the VM.** Run them in a throwaway directory: copy the repo to `/tmp/agent-test` on `bot`, create the user `agent-test`, run `sudo AGENT_ROOT_TESTS=1 go test ./internal/daemon -run Privilege`, then delete the directory and the user. If Go isn't on the VM, run the release's test binary built with `go test -c` for linux instead. Expected: PASS.
- [ ] **Step 3: Archive the old bot.** With the user's go-ahead, on `bot`:
  1. `sudo bot stop`
  2. Export the journal: `sqlite3 ~bot/.bot/control/journal.sqlite "SELECT json_object('seq',seq,'kind',kind,'payload',json(payload),'timestamp',timestamp) FROM journal ORDER BY seq" > journal.jsonl`, or a small Go/Node one-off if `sqlite3` is missing.
  3. Copy `journal.jsonl`, `~bot/.bot/workspace/` and the memory files into `/home/agent/archive/old-bot/` (created after Step 4's user exists), owned by root, directories 0755 and files 0644.
  4. `sudo bot uninstall`, which keeps the data.
- [ ] **Step 4: Install.** Give the user the exact one-liner for `v0.1.0`. They run it on the VM and enter the provider choice, key and model themselves.
- [ ] **Step 5: Verify read-only. Send no messages.**
  - `systemctl is-active agent` prints `active`.
  - `journalctl -u agent -n 50` shows no errors.
  - The modes of `/etc/agent` and `/var/log/agent` match the spec.
  - `sudo -u agent cat /etc/agent/credentials/*` fails.
  - `sudo -u agent agent provider describe` lists the models.
  - `crontab -u agent -l` shows the heartbeat line.
  - `ls -l /home/agent/journal` is valid after the first genuine run.
  - The user sends the first message themselves.
- [ ] **Step 6: Report** the outcome, including anything skipped or blocked.
