# Agent Setup

guppi tracks AI coding agents running inside your tmux sessions. Each agent needs a hook configured so it can notify guppi of status changes (active, waiting for input, completed, error).

The easiest way to configure all detected agents at once:

```bash
guppi agent-setup
```

Use `--dry-run` to preview changes without writing files:

```bash
guppi agent-setup --dry-run
```

## Supported Agents

### Claude Code

**Auto-configured by `guppi agent-setup`.**

guppi adds hooks to `~/.claude/settings.json` that fire on tool use, notifications (permission prompts, input dialogs), and task completion.

**Manual setup:** Add to `~/.claude/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "guppi notify -t claude -s active -m 'Using tool'" }]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "guppi notify -t claude -s active -m 'Working'" }]
      }
    ],
    "Notification": [
      {
        "matcher": "permission_prompt",
        "hooks": [{ "type": "command", "command": "guppi notify -t claude -s waiting -m 'Permission needed'" }]
      },
      {
        "matcher": "elicitation_dialog",
        "hooks": [{ "type": "command", "command": "guppi notify -t claude -s waiting -m 'Needs input'" }]
      }
    ],
    "Stop": [
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "guppi notify -t claude -s completed -m 'Task complete'" }]
      }
    ]
  }
}
```

### Codex

**Auto-configured by `guppi agent-setup`.**

Codex supports a `notify` key in `~/.codex/config.toml`. This fires when the agent needs user attention and passes the last assistant message as a JSON blob in `$1`.

**Manual setup:** Add to `~/.codex/config.toml`. The `notify` line **must appear before any table headers** (like `[sandbox]`) or Codex's TOML parser will misinterpret it:

```toml
model = "o4-mini"
notify = ["guppi", "notify", "-t", "codex", "--event-data"] # guppi-agent-hook

[sandbox]
# ... rest of config
```

**How it works:**

Codex passes a JSON blob as an additional argument when the agent completes a turn and needs user input. The `--event-data` flag tells guppi to parse this JSON natively. Fields extracted:

- `type` — event type (currently `agent-turn-complete`)
- `last-assistant-message` — truncated to 200 chars and used as the notification message
- `thread-id`, `turn-id`, `cwd` — available for context

The `agent-turn-complete` event maps to guppi's `waiting` status, which triggers an alert in the UI and a push notification.

No external dependencies required (no bash, no jq).

### GitHub Copilot CLI

**Auto-configured by `guppi agent-setup`.**

Copilot CLI supports global hooks in `~/.copilot/hooks/` as JSON files. guppi writes `~/.copilot/hooks/guppi.json` covering session start/end, tool use, and error events. Hooks receive event context as JSON on stdin.

**Note:** Repository-level hooks in `.github/copilot/hooks.json` take precedence over global hooks. Both run — values are concatenated across levels.

**Manual setup:** Create `~/.copilot/hooks/guppi.json`:

```json
{
  "version": 1,
  "hooks": {
    "sessionStart": [{ "type": "command", "bash": "guppi notify -t copilot -s active -m 'Session started'", "comment": "guppi agent hook" }],
    "sessionEnd": [{ "type": "command", "bash": "guppi notify -t copilot -s completed -m 'Session ended'", "comment": "guppi agent hook" }],
    "preToolUse": [{ "type": "command", "bash": "guppi notify -t copilot -s active -m 'Using tool'", "comment": "guppi agent hook" }],
    "postToolUse": [{ "type": "command", "bash": "guppi notify -t copilot -s active -m 'Working'", "comment": "guppi agent hook" }],
    "userPromptSubmitted": [{ "type": "command", "bash": "guppi notify -t copilot -s active -m 'Thinking'", "comment": "guppi agent hook" }],
    "errorOccurred": [{ "type": "command", "bash": "guppi notify -t copilot -s error -m 'Error occurred'", "comment": "guppi agent hook" }]
  }
}
```

### OpenCode

**Auto-configured by `guppi agent-setup`.**

guppi writes a hook script to `~/.config/opencode/guppi-hook.sh` that handles OpenCode's event types.

**Manual setup:** Create `~/.config/opencode/guppi-hook.sh`:

```bash
#!/bin/sh
EVENT_TYPE="${1:-unknown}"
case "$EVENT_TYPE" in
  approval_needed) guppi notify -t opencode -s waiting -m "Approval needed" ;;
  task_start)      guppi notify -t opencode -s active -m "Working" ;;
  task_complete)   guppi notify -t opencode -s completed -m "Done" ;;
  error)           guppi notify -t opencode -s error -m "Error occurred" ;;
esac
```

Then configure OpenCode to use this hook script in its settings.

## The `notify` command

Under the hood, all agent hooks call `guppi notify`. You can also use it directly:

```bash
# Basic usage
guppi notify -t claude -s waiting -m "Needs approval"
guppi notify -t codex -s active
guppi notify -t claude -s completed

# Read event JSON from stdin (used by some agent hooks)
echo '{"hook_event_name":"Stop","last_assistant_message":"Done"}' | guppi notify -t codex --stdin
```

**Flags:**

| Flag | Alias | Description |
|------|-------|-------------|
| `--tool` | `-t` | Agent name: `claude`, `codex`, `copilot`, `opencode` |
| `--status` | `-s` | Status: `active`, `waiting`, `completed`, `error` |
| `--message` | `-m` | Human-readable message |
| `--stdin` | | Read hook event JSON from stdin |
| `--session` | | tmux session name (auto-detected) |
| `--window` | | tmux window index (auto-detected) |
| `--pane` | | tmux pane ID (auto-detected) |
| `--server` | | guppi server URL (default: `http://localhost:7654`) |
| `--socket` | | Unix socket path (auto-detected) |

**Communication:** `guppi notify` tries the Unix socket first (zero-config when guppi server is running locally), then falls back to HTTP.

## Inactivity-based waiting detection

Claude Code sends explicit "waiting" events when it needs input (permission prompts, input dialogs). Other tools (Copilot, Codex, OpenCode) don't have equivalent hooks.

To bridge this gap, guppi includes an **inactivity promoter**: if a non-Claude tool sends "active" events but then goes quiet for 30 seconds, guppi automatically generates a synthetic "waiting" event with the message "May need attention". This surfaces the alert in the UI and triggers push notifications, just like a native waiting event would.

This only applies to tools without native waiting support — Claude's explicit hooks are always trusted and never overridden.

The timeout is 30 seconds by default. This balances catching idle agents quickly against false positives during normal pauses between tool calls.

## Status values

| Status | Meaning | UI behavior |
|--------|---------|-------------|
| `active` | Agent is working normally | Badge in sidebar |
| `waiting` | Agent needs user input | Alert banner + push notification |
| `error` | Agent hit an error | Alert banner + push notification |
| `completed` | Agent finished its task | Clears alerts |
