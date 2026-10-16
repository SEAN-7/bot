# Provider plugins

A provider plugin gives the harness model access. It is one executable
that speaks the protocol below; the harness has no provider code of its
own. Root installs a plugin from a file or a URL, trusting it as it would
any package:

```bash
sudo agent provider install NAME /path/to/plugin
sudo agent provider install NAME https://example.org/plugin-linux-amd64
```

The command installs the binary as `/etc/agent/plugins/NAME` (root-owned,
executable by the `agent-provider` group only), runs `describe`, prompts
for each setting it declares, writes them to
`/etc/agent/credentials/NAME.env` (root-only), and refreshes
`/var/lib/agent/providers.json`, the cache agents read. Reinstalling keeps
settings already present. NAME is lowercase letters, digits and hyphens.

Plugins run as the system user `agent-provider`, with the credentials
file's variables and a minimal `PATH` in their environment, from `/`. No
agent can read `/etc/agent` or the plugin's environment. A call has 15
minutes.

## Protocol (version 1)

Per call, the daemon writes one JSON request to the plugin's stdin and
reads one JSON response from its stdout; stderr goes to the service log.
Every message carries `"protocol": 1`.

**describe** (`{"protocol": 1, "type": "describe"}`, works without
credentials) returns:
- `settings`: `{name, description, secret}` for each environment variable
  it needs; the installer prompts for them.
- `models`: `{id, context_tokens, image_types, options}`. `context_tokens`
  is the context window where known, for agents choosing a model.
  `image_types` lists the image media types the plugin takes (see
  Images). `options` maps names to `{description, values}` or
  `{description, type: "integer", min, max}`; the harness shows them to
  agents and passes an agent's options through unchecked, so the plugin
  validates them.
- `caching` (optional): text for agents on what the provider caches between
  calls and for how long, the smallest prompt it caches, where the plugin
  puts cache markers, and what cache reads and writes cost relative to
  plain input.

**complete** sends `model`, `options` (passed through), `system` (`{text}`
segments, most stable first, for cache markers), `tools` (the `sh`
definition) and `messages`:
- `{role: "user", content, images}`, `images` being `{file, media_type,
  data}` with `data` the raw bytes (base64 in JSON)
- `{role: "assistant", text, command, wait, call_id, opaque}`
- `{role: "tool", call_id, content}`

The response is `{text, command, wait, usage: {input_tokens, output_tokens,
cache_read_tokens, cache_write_tokens}, opaque}` or `{error: {message,
kind}}`. `wait` is the model's argument passed through unchanged (absent
means the default). The daemon assigns `call_id`; `opaque` is stored on the
assistant message and returned in later requests (reasoning blocks,
signatures). A plugin returns at most one command and disables parallel
tool calls.

**Error kinds.** A failed call without a `kind` is retried with backoff.
`kind: "context_overflow"` (the request exceeds the model's context
window, or the provider's request size limit) is not retried: the run ends
with a context reset, or on a run's first call with an error the agent's
owner sees. `kind: "invalid_request"` (the provider rejected the request as
malformed: a bad option, say) is not retried either: the run ends and the
message goes to the agent's `ERROR` until a call succeeds.

**Images.** The harness shows the model any inbox file whose detected
media type is in the model's `image_types`, bytes unchanged, and checks
nothing else. The plugin applies its provider's limits: where an image is
too large, undecodable or otherwise unacceptable, it replaces the image
with a text block saying so and naming the file, so the request still goes
through and the model learns what happened.

The daemon interprets only `image_types` and the error kinds; `usage` is
journaled and `caching` cached in `providers.json`, neither interpreted.

## Reference plugins

Sources live in `plugins/<name>/` in the repository and each release
publishes them as `plugin-<name>-linux-<arch>`:

- **anthropic**: Messages API, `ANTHROPIC_API_KEY`.
- **openai-compatible**: Chat Completions with function calling, for
  OpenAI, vLLM, Ollama, OpenRouter, llama.cpp and others.
- **replay**: scripted replies, for tests.

What each does is in [the spec](spec.md), "Plugins".
