# Changelog

## [4.4.21] - Session Shortcut Fix

### Fixes
- **navigation:** session cycling now uses Cmd/Ctrl+Shift+. for next and Cmd/Ctrl+Shift+/ for previous, avoiding macOS arrow-key conflicts.

## [4.4.20] - Terminal File Link Fixes

### Fixes
- **terminal:** file paths now open in the wiki only with Cmd/Ctrl-click, preserving plain and double-click text selection.
- **terminal:** stale async file checks no longer leave file highlights under the wrong terminal row.

## [4.4.19] - Terminal Resize Scroll Fix

### Fixes
- **terminal:** keep terminals pinned to the bottom after width reflow unless the user actually scrolled up.

## [4.4.18] - Standalone Split Drop Fix

### Fixes
- **groups:** dropping New session onto a standalone terminal no longer crashes when the active group rank is empty.

## [4.4.15] - Alt+Arrow Key Fix

### Fixes
- **terminal:** Alt+Arrow keys now send the standard Alt-modified CSI sequences to the terminal instead of Ctrl+Arrow (xterm.js 5.5.0 non-Mac remap workaround).

## [4.4.14] — Reconnect Replay Fix

### Fixes
- **terminal:** reset the xterm buffer when a replay starts. The daemon resends its entire ring buffer on every reconnect and the frontend used to append it on top of the existing content, so scrollback accumulated duplicated history and an idle pane could come back from a hidden tab sitting far up in old output instead of at the prompt.

## [4.4.13] — Compose Dictation & Default Shell

### Features
- **compose:** added Web Speech API speech-to-text dictation to the compose input modal.
- **sessions:** leaving the shell command field empty in the new session dialog now launches the default shell.

## [4.4.12] — In-App Completion Signals

### Features
- **notifications:** agent completion now also surfaces inside the app, not just via browser/push notifications. When `completed` is enabled, finishing a turn raises an info toast ("<tool> finished") and the sidebar row shows a transient green "done" badge and border highlight for ~6s before settling back to idle. Previously `completed` cleared the turn instantly to a plain idle row with no in-UI signal.

## [4.4.11] — Notify on Agent Completion

### Features
- **notifications:** the `completed` transition (agent finishes its turn, working -> idle) now fires a "<tool> finished" browser notification and web push, not just `waiting`/`error`/`stuck`. `completed` was already a default-on, toggleable status in Settings but neither the browser-notification hook nor the push sender acted on it, so a finished agent was silently unsurfaced.

## [4.4.10] — iOS PWA Push Notifications

### Features
- **web:** added the web app manifest (`display: standalone`) and a root-scope service worker (`/sw.js`) so Termyard installs as a PWA and can receive web push on iOS 16.4+ and Android. The frontend already registered `/sw.js` and the backend already sent VAPID push, but both files were missing, so push silently failed on installed PWAs. The service worker renders `push` payloads as notifications and focuses/opens the app on click.

## [4.4.9] — New Group Creation Fix

### Bug Fixes
- **groups:** dropping the new-session button onto a standalone session pushed the group tree before the session existed, so the server pruned the one-leaf group and tombstoned it. The tree and rank push now happens after the session create resolves, and a guard clears stale snapshots during the pending window.

## [4.4.8] — File Link Highlight Fix

### Bug Fixes
- **web:** terminal file-path link highlight had an off-by-one in its end column; a trailing quote or closing paren after the path is no longer visually highlighted.

## [4.4.7] — Server-Authoritative Group Membership

### Bug Fixes
- **groups:** a session can now belong to at most one live layout group; the server enforces exclusive membership on every group write, ending duplicate session rows and phantom UNNAMED group brackets in the sidebar.
- **groups:** groups with fewer than two members are dissolved server-side; leaves pointing at sessions that no longer exist are pruned on load, healing previously corrupted group stores.
- **groups:** dissolved groups stay dissolved; stale clients can no longer resurrect deleted groups (writes to tombstoned ids are rejected).
- **web:** the client no longer writes layout trees back to the server on group switch or passive divergence; trees are pushed only after actual user edits, so server-side cleanup is never overwritten by stale tabs or devices.
- **groups:** membership changes caused by enforcement now propagate to peers and trigger AI naming, so newly formed groups get names instead of lingering unnamed.

## [4.4.6] — Session Reconnect Fix

### Bug Fixes
- **pty:** sessions with more than 10 MiB of scrollback could never reconnect (permanent "DISCONNECTED — RECONNECTING", usually after a server restart). All daemon socket frame caps now exceed the 32 MiB replay ring buffer.
- **pty:** large pastes no longer kill the daemon connection; silence-monitor capture no longer fails on big buffers.

## [4.4.5] — Mobile UX & Group Sync

### Features
- **terminal:** scroll scrubber on the right edge for fast scrollback navigation; drag maps directly onto the full buffer, always visible while scrollable.
- **mobile:** compose input button in the key bar opens a textarea modal; Send fills the terminal without pressing Enter.
- **mobile:** long-press on the terminal opens the capture modal for text selection.
- **mobile:** swipe from the left edge opens the sidebar.
- **settings:** Roboto Mono terminal font preset.

### Bug Fixes
- **groups:** reinstated membership-fingerprint dedupe backstop; duplicate groups with identical sessions are healed on every sync.
- **groups:** name/rank updates for unknown group ids are rejected instead of materializing phantom empty groups (late AI-naming race).
- **mobile:** wiki panel renders as a full-screen overlay instead of overflowing off the right edge.
- **mobile:** key bar keeps safe-area bottom padding when the on-screen keyboard is hidden, avoiding the navigation pill.

## [4.4.4] — Bug Fixes

### Bug Fixes
- **terminal:** stray characters like `62;4;9;22c` no longer leak into the shell after reconnect/replay. The auto-reply suppression window now closes only after xterm has finished parsing the replayed buffer, so late DA1/DA2/DSR replies from replayed queries are still filtered.

## [4.4.3] — Bug Fixes

### Bug Fixes
- Sidebar: a session left alone in a layout group after other members were killed now returns to standalone display; groups dissolve (locally and on the server) when they drop below 2 members.

## [4.4.2] — Bug Fixes

### Bug Fixes

- **wiki:** clicking a relative or `~` file path in the terminal now always resolves server-side against the pane's working directory, fixing links that were intermittently treated as absolute or opened against the wrong root.
- **artifacts:** the detected-files panel now tracks real writes only. Read-style tool calls no longer show up, files written anywhere on disk (not just inside the session cwd) are accepted, deleted files disappear on refresh instead of lingering with a badge, and a session's list is cleared when the session is killed, with entries older than 7 days dropped on server load.
- **terminal:** replaying buffered output no longer sends stray auto-replies (DA/DSR/CPR/OSC responses) back to the shell.
- **terminal:** scroll-jump rework and a 32MiB scrollback ring matching the browser replay cap.

## [4.4.1] — Bug Fixes

### Bug Fixes

- **sessions:** exited shells now disappear from the sidebar and pane layout within a second, no page reload needed. The whole session lifecycle moved from periodic disk scanning to an event-driven design: the server watches each session daemon over its socket and reacts the moment the shell exits, while daemons carry a unique instance identity (nonce plus process start time) so a dead session can never be confused with a new one reusing its name. Live shells still survive server restarts through a one-time adoption scan at boot.
- **sessions:** removed sessions no longer resurrect after switching groups or reloading, and transient connection loss to a live daemon shows the session as offline instead of dropping it.
- **ui:** locally originated session-removed and session-added events now carry the host id, so the sidebar and split panes update immediately instead of waiting for a reload.
- **cleanup:** the crash-recovery panel and its API routes were removed along with the discovery polling they depended on; session create and list from the CLI now route through the running server and only fall back to direct spawn when no server is up.

## [4.4.0] — Features & Bug Fixes

### Features

- **terminal:** make Unicode graphemes always-on; remove the setting toggle.
- **theme:** trim built-in presets to 3 and add a user-defined custom color theme.
- **overview:** remove Grid mode — Board is now the only layout.
- **settings/terminal:** remove predictive echo, default to the WebGL renderer, stage AI-naming saves, and support custom terminal fonts.

### Bug Fixes

- **wiki:** resolve relative and `~/`-aliased paths correctly when opening a file from the terminal. Server-side path resolution now expands `~` against the home directory instead of joining it onto the session cwd as a literal segment. The wiki panel surfaces open failures (bad path, no active pane cwd, unreachable peer) with a visible message or toast instead of a silent blank panel, and the terminal's file-link highlighter now checks existence (via a new read-only `GET /file/exists`) before highlighting a path, so links to files that don't exist are no longer clickable.
- **settings:** allow an empty AI naming endpoint (env-var fallback is valid).
- **peer:** remove dead peer-protocol code (unused bootstrap route, message types, dead browser branch).

## [4.3.0] — Features

### Features

- **groups:** automatic AI naming for synced session groups. When a persisted layout group first reaches two or more member sessions, or its membership changes (session added/removed), the server generates a name via the existing AI namer and syncs it through the group peer channel. Pane ratio, split direction, reorder, rank, and unrelated metadata changes do not trigger renaming. Manual names are preserved via a new name_mode (auto/manual) field until the user clears the name or presses the AI-name button, which now forces an immediate server-side regeneration and returns the group to AI-managed mode. Automatic attempts share the same cooldown/concurrency/backoff gate as session naming (extracted to pkg/namer.AutomaticGate), debounce bursty tree writes, and discard stale results if the group is deleted, renamed, or its membership changes before the model responds.

## [4.0.5] — Bug Fixes

### Bug Fixes

- **peer:** keep the hub<->host data connection alive on idle remote terminals. `SpliceConns` answered the browser heartbeat ping locally but never forwarded it to the peer data conn, so NAT/proxy idle timeouts silently killed idle remote tabs and forced a visible reconnect flap. The ping is now replied to locally (fast ack) AND forwarded to the peer conn so the host echoes a pong back through the data->browser pump, keeping the link bidirectionally busy.
- **state:** unblock killing the last session on non-systemd hosts (e.g. macbook). `UpdateSessions` Guard 1 skipped every refresh where discovery returned empty while sessions were tracked, assuming all empty discoveries are transient — so a genuinely dead last session lingered as "disconnected — reconnecting" forever. Added `pty.Registry.IsSessionDead` (true for cleanly_ended / termination_requested / dismissed from the durable LifecycleStore) and taught Guard 1 to remove sessions when every vanished session is individually confirmed dead.
- **frontend:** prune dead session panes when the live session list becomes empty. The prune effect bailed on `sessions.length === 0`, so the dead pane stayed mounted and showed "disconnected — reconnecting". Safe because the backend Guard 1 keeps `/api/sessions` populated during transient empties.

## [4.0.4] — Bug Fixes

### Bug Fixes

- **frontend:** route remote daemon sessions through the peer relay on switch. `useTerminal` built the terminal WS URL from the session `backend` field; the `backend === "daemon"` branch used `/ws/daemon-session?name=...` without `&host=`, so remote daemon sessions hit the hub's LOCAL daemon handler, dialed a local socket for a remote name, failed, and the tab looped "disconnected — reconnecting". Why cmd+R reattached but in-app switch did not: on a fresh page load the sessions list was still fetching when `connect()` first fired, so `backend` was undefined and the else branch (with `&host=`) picked the correct peer-relay route. The WS stayed attached (effect dep is `[sessionName]`). On in-app switch the list was already loaded, `backend="daemon"` was known, and the wrong route was selected. Include `&host=` in the daemon-backend branch when `hostId` is set.

## [4.0.1] – [4.0.3] — Bug Fixes & Performance

### Bug Fixes

- **pty:** clean up orphaned session scopes when their daemon exits out of band. `cleanUpOrphanedSessionScopes_no_function` (best-effort) now runs alongside socket-scan discovery so a crash + later restart does not leave systemd scopes holding zombie processes.
- **session:** reflect daemon death instantly instead of lagging up to 10s. `bridgeSessionWithCB` now calls `RefreshSessions` on teardown so a dead session disappears from the sidebar and its terminal view unmounts promptly.
- **namer:** use `SetDisplayName` for manual rename instead of `ApplyRename`, fixing the AI-name button silently no-oping on remote peer sessions.
- **terminal:** force bracket paste wrapping for multiline pastes when the application hasn't enabled bracket paste mode (DECSET 2004). Before v4 tmux handled this transparently; with direct PTY sessions, apps like Pi that don't enable bracket paste would see each pasted line as a separate Enter.
- **pty:** deduplicate session names on every create, fixing a split-view mirroring bug from missing session name dedup.
- **session:** make new session creation non-blocking — the viewer returns immediately while the daemon cold-starts, instead of stalling on the socket dial.
- **update:** handle "text file busy" (ETXTBSY) when replacing the binary during a self-update by retrying the rename.

### Performance

- **terminal:** increase scrollback to an 8MB ring buffer / 50k line xterm default.

### Frontend Scroll Fixes (v4.0.1 – v4.0.3)

- **terminal:** preserve scroll position on resize instead of forcing `scrollToBottom`; snap to bottom after replay and resize.
- **terminal:** two-phase scroll restore — `requestAnimationFrame` catches xterm async viewport updates the synchronous pass misses.
- **terminal:** settle-timer replay scroll — all panes scroll to bottom after replay.
- **terminal:** scroll-preserve `doFit` and font-load refits now route through the shared `fit()` callback (was bypassing it).
- **terminal:** scroll guard interval reliably keeps terminals at the bottom for ~10s after connect.
- **terminal:** extract shared `fitPreservingScroll`, remove dead scroll code.

## [4.0.0] — Breaking Changes

### ⚠ BREAKING CHANGES

- **backend:** tmux is no longer required. The daemon PTY backend is now the only session backend. All sessions run as independent daemon processes that survive server crashes, restarts, and OOM events.

### Features

- **daemon:** daemon is now the default and only backend for all sessions
- **daemon:** sessions survive server crashes — each session runs as an independent process with its own process group (`Setsid`)
- **daemon:** automatic session rediscovery on server restart via socket directory scanning
- **daemon:** ring buffer replay — reconnecting clients receive the last 1MB of terminal output

### Session Reliability

- **registry:** verify daemon process is alive (PID check via `/proc`) before removing stale sockets — prevents accidentally orphaning running sessions
- **registry:** increase liveness failure threshold from 3 to 5 consecutive failures for more tolerance under load
- **registry:** mass-removal protection — if all sessions appear stale simultaneously, skip cleanup (likely transient system event)
- **state:** mass-removal guard — refuse to remove >50% of tracked sessions in one update cycle
- **state:** refuse to clear all sessions when discovery returns empty (likely transient failure)
- **daemon:** panic recovery in all daemon goroutines (`pumpPTY`, `handleClient`, accept loop) — a panic in one client connection doesn't crash the daemon
- **daemon:** cap DaemonSession client buffer at 4MB to prevent server OOM from output flooding

### Removed

- **tmux:** removed tmux dependency entirely (2,794 lines deleted)
- **tmux:** removed tmux control mode, session creation, and all tmux-specific code paths
- **recovery:** removed tmux session rebuilder (no longer needed — daemon sessions persist independently)

## [2.2.2](https://github.com/anh-chu/termyard/compare/v2.2.1...v2.2.2)

### Bug Fixes

- **namer:** make the AI-name button work for remote peer sessions. The name is now generated on the hub (using the remote session's prompt, agent message, project, and sibling names) and sent to the peer to apply, so it no longer silently no-ops when the peer process has no namer configured

## [2.2.1](https://github.com/anh-chu/termyard/compare/v2.2.0...v2.2.1)

### Bug Fixes

- **namer:** wire distinct names + latest user prompt into the manual regenerate button, which still used the first prompt and ignored sibling names

## [2.2.0](https://github.com/anh-chu/termyard/compare/v2.1.1...v2.2.0)

### Features

- **namer:** make AI session names distinct and current — feed sibling session names into the prompt so labels differ by wording instead of numeric suffixes, name by what differs when sessions share a project/branch/agent, use the latest user prompt for naming (the sidebar still shows the first), re-name on a fresh user prompt, and give reasoning models token headroom by taking the final output line

## [1.3.0](https://github.com/anh-chu/termyard/compare/v1.2.1...v1.3.0)

### Performance

- **peer:** make remote sessions hyper-performant — split the control channel into hi/lo priority lanes so bulky state snapshots never block keystroke echoes, ship PTY data as raw binary frames (no base64/JSON per chunk), move marshaling off the single writer, deepen the interactive queue, and raise WebSocket buffers to 32KB. Eliminates typing latency, jitter, and head-of-line blocking on remote peer sessions.

## [1.2.0](https://github.com/anh-chu/termyard/compare/v1.1.0...v1.2.0)

### Features

- **terminal:** add opt-in coding ligature support (Fira Code / JetBrains Mono) via `@xterm/addon-ligatures`, gated behind a Settings → Terminal toggle (default off)

## [0.5.0](https://github.com/ekristen/guppi/compare/v0.4.0...v0.5.0) (2026-06-13)

### Bug Fixes

- **sidebar:** use !important to ensure selected session text color overrides base ([fbfada9](https://github.com/ekristen/guppi/commit/fbfada9))

## [0.1.1-beta.2](https://github.com/ekristen/guppi/compare/v0.1.0-beta.2...v0.1.1-beta.2) (2026-03-15)

### Features

- better font/size ([a607c16](https://github.com/ekristen/guppi/commit/a607c162761eac26e2dec4eaebf637d07b0cca61))
- better font/size ([a5cf00b](https://github.com/ekristen/guppi/commit/a5cf00bc68d50fd4d78fb121d8c2520210df6f77))
