# UX Contracts

Canonical inventory of user-facing features, behaviors, triggers, and outcomes in Termyard. This document records what the app **actually does today**, verified against `web/src` and `pkg/` source. It survives refactors because it describes user-visible contracts, not implementation details.

## Why this exists

Feature behavior is fragile during refactors. A 400ms hover delay, a two-step kill confirmation, a 5-minute peer prune window, or an Esc-key exception can silently disappear and nobody remembers we had it. This document is the checklist.

## How to use this

- **Before removing or restructuring a feature:** find it here, confirm nothing depends on the specific behavior, and update or delete its entry.
- **Before adding a feature:** add an entry here in the same PR. Undocumented = invisible to refactors.
- **When behavior and doc disagree:** code is ground truth; fix the doc and check `README.md` for the same drift.

## Table of Contents

- [Why this exists](#why-this-exists)
- [How to use this](#how-to-use-this)
- [1. Dashboard / Overview](#1-dashboard-overview)
  - [1.1 Layout (Board only)](#11-layout-board-only)
  - [1.2 Board mode structure](#12-board-mode-structure)
  - [1.3 Session cards](#13-session-cards)
  - [1.4 System stats / Yard health](#14-system-stats-yard-health)
  - [1.5 localStorage persistence (Overview)](#15-localstorage-persistence-overview)
- [2. Sidebar](#2-sidebar)
  - [2.1 Sidebar grouping modes](#21-sidebar-grouping-modes)
  - [2.1a Layout group dissolution](#21a-layout-group-dissolution)
  - [2.2 Sidebar collapse](#22-sidebar-collapse)
  - [2.2a Mobile swipe-from-left to open](#22a-mobile-swipe-from-left-to-open)
  - [2.3 Sidebar width adjustment](#23-sidebar-width-adjustment)
  - [2.4 Session row (Sidebar)](#24-session-row-sidebar)
- [3. Top bar](#3-top-bar)
  - [3.1 Primary alert banner](#31-primary-alert-banner)
- [4. Tiled view (split panes)](#4-tiled-view-split-panes)
- [5. Quick Switcher](#5-quick-switcher)
- [6. New Session Modal](#6-new-session-modal)
- [7. Session Actions Menu](#7-session-actions-menu)
- [8. Settings](#8-settings)
  - [8.1 Settings mismatches found](#81-settings-mismatches-found)
- [9. Terminal](#9-terminal)
  - [9.1 Terminal connection lifecycle](#91-terminal-connection-lifecycle)
  - [9.2 Terminal copy / paste](#92-terminal-copy-paste)
  - [9.3 Clipboard menu (mobile)](#93-clipboard-menu-mobile)
  - [9.4 Drag-drop / file uploads](#94-drag-drop-file-uploads)
  - [9.5 Mobile key bar](#95-mobile-key-bar)
  - [9.5a Compose Input Modal](#95a-compose-input-modal)
  - [9.6 Predictive echo](#96-predictive-echo)
  - [9.7 File links in terminal](#97-file-links-in-terminal)
  - [9.8 Artifacts / detected files](#98-artifacts-detected-files)
  - [9.9 Pop-out window (Picture-in-Picture)](#99-pop-out-window-picture-in-picture)
  - [9.10 Terminal keyboard shortcuts](#910-terminal-keyboard-shortcuts)
  - [9.11 Terminal resize / scrollback](#911-terminal-resize-scrollback)
  - [9.12 Terminal fonts / rendering](#912-terminal-fonts-rendering)
  - [9.13 DEC 2026 synchronized updates](#913-dec-2026-synchronized-updates)
  - [9.14 Terminal toolbar](#914-terminal-toolbar)
  - [9.15 Scroll scrubber](#915-scroll-scrubber)
  - [9.16 Wiki Panel mobile](#916-wiki-panel-mobile)
- [10. Session Lifecycle & Status](#10-session-lifecycle-status)
  - [10.1 Session states](#101-session-states)
  - [10.2 Tool events & agent tracking](#102-tool-events-agent-tracking)
  - [10.3 Activity tracking](#103-activity-tracking)
  - [10.4 Session discovery & pruning](#104-session-discovery-pruning)
  - [10.4a Session "unreachable" state](#104a-session-unreachable-state)
  - [10.5 Session attributes (background / hidden)](#105-session-attributes-background-hidden)
  - [10.5a Groups sync & exclusive membership](#105a-groups-sync--exclusive-membership)
  - [10.6 Crash & recovery](#106-crash-recovery)
- [11. Keyboard Shortcuts (complete)](#11-keyboard-shortcuts-complete)
- [12. Auth & Lock](#12-auth-lock)
  - [12.1 TLS & security](#121-tls-security)
- [13. Notifications & Toasts](#13-notifications-toasts)
- [14. Multi-host / Peering](#14-multi-host-peering)
  - [14.1 Per-terminal connections (ADR verified)](#141-per-terminal-connections-adr-verified)
  - [14.2 Peer status polling](#142-peer-status-polling)
- [15. Agent Detection (docs/agent-detection.md)](#15-agent-detection-docsagent-detectionmd)
- [16. CLI (commands & flags)](#16-cli-commands-flags)
  - [server](#server)
  - [install / uninstall](#install-uninstall)
  - [update](#update)
  - [notify](#notify)
  - [agent-setup](#agent-setup)
  - [session (internal subcommands)](#session-internal-subcommands)
- [17. Backend API](#17-backend-api)
  - [Public routes (no session auth)](#public-routes-no-session-auth)
  - [Protected routes (session auth when enabled)](#protected-routes-session-auth-when-enabled)
  - [WebSocket routes](#websocket-routes)
- [18. WebSocket message types](#18-websocket-message-types)
  - [Browser hub (`/ws/events`)](#browser-hub-wsevents)
  - [Terminal WS (`/ws/session`)](#terminal-ws-wssession)
  - [Peer protocol (`/ws/peer`)](#peer-protocol-wspeer)
  - [Upload data (`/ws/peer-stream`, upload role)](#upload-data-wspeer-stream-upload-role)
- [19. Constants with user-visible effect](#19-constants-with-user-visible-effect)
  - [Connection / timing](#connection-timing)
  - [Terminal behavior](#terminal-behavior)
  - [Rate limiting](#rate-limiting)
  - [Sizes / limits](#sizes-limits)
  - [Peer / mesh](#peer-mesh)
- [20. Keyboard Shortcuts (see section 11 for complete table)](#20-keyboard-shortcuts-see-section-11-for-complete-table)
- [21. Theming](#21-theming)
- [22. Non-goals / explicitly out of scope](#22-non-goals-explicitly-out-of-scope)
- [23. Known gaps](#23-known-gaps)

---

## 1. Dashboard / Overview

### 1.1 Layout (Board only)

**Status: Grid mode removed.** The Overview dashboard now has a single layout: **Board mode** — four columns (Needs you / Working / Idle / Offline) with session cards, plus three collapsible rails below (Scheduled, Hidden, Backgrounded). The layout toggle button and the `'grid' | 'board'` mode state have been deleted; there is nothing left to switch between. Grid mode's per-host grouping view no longer exists in any form.

**Why it matters:** Maintaining two full dashboard layout systems (Board's columns+rails+tiled-pane folding vs Grid's host-grouping) doubled UI maintenance surface for a toggle most users never touched. Board mode's column organization and rail structure remain fully intact and are still the user-facing contract to preserve.

**Verification pointer:** `web/src/components/Overview.tsx`

### 1.2 Board mode structure

**Contract:** Four fixed-order columns (warning color → success green → mute → mute): Needs you, Working, Idle, Offline. Each contains session cards that match that state. Empty columns are hidden (no placeholders). Columns auto-fit width proportionally based on card count. Below the four columns are three collapsible rails: Scheduled (agent panes grouped by schedule), Hidden (background sessions), Backgrounded (same) — tabs when collapsed, full columns when expanded. Collapse/expand state per rail persists.

**Why it matters:** Users organize their sessions by visible state; hiding the column structure or the rails would make backgrounded/scheduled work inaccessible.

**Verification pointer:** `web/src/components/Overview.tsx`

### 1.3 Session cards

**Contract:** Displays session name, host name (when >1 host), project folder (truncated, full path in tooltip), uptime (e.g. "45m", "3h", "2d"), state dot (color per state: warning/success/mute), tool agent icon (hexagon, agent brand color when agent present), count of mate panes (⧉ badge). Click docked-view compatible terminal opens in right split (resizable, saved width); otherwise opens full-view. Right-click → context menu (Rename, AI rename, Hide, Background, Kill with two-step confirmation). Hover after 400ms delay → popover showing last 40 lines of terminal output (dismisses on pointer leave).

**Why it matters:** The 400ms hover delay, two-step kill confirmation, and 40-line glance depth are all measurable UX contracts; changing any would be noticeable.

**Verification pointer:** `web/src/components/Overview.tsx`, `web/src/components/SessionActionsMenu.tsx`

### 1.4 System stats / Yard health

**Contract:** Persistent open details section showing CPU %, Memory %, load average, memory used/total GB, uptime, CPU count + architecture. One card per host (when >1 host) or combined (single host). Color: >90% destructive, >70% warning, else default. Auto-fetches every 5s; errors silent. Collapsible; open by default.

**Why it matters:** Stats visibility and refresh rate are contracts; removing them would lose system observability.

**Verification pointer:** `web/src/components/Overview.tsx`

### 1.5 localStorage persistence (Overview)

**Contract:** Stores docked terminal split width in pixels (`overview_split_width`, default 520px, clamped 360–1200px), and per-rail collapse state (`overview_rail_hidden`, `overview_rail_bg`, `overview_rail_scheduled` = "open"/"closed"). The `overview_layout` key no longer exists — deleted along with Grid mode; there is only one layout to persist.

**Why it matters:** Users' layout preferences and split widths must survive page reloads; losing this breaks the persistent experience.

**Verification pointer:** `web/src/components/Overview.tsx`

---

## 2. Sidebar

**Contract:** Session list grouped by status or by custom layout groups. Sidebar can be collapsed to narrow column or completely hidden; width adjustable via drag handle. Per-group collapse toggles. Drag-drop sessions to reorder. Click selects session. Right-click → context menu. Toolbar buttons: filter by project, toggle status-view grouping, quick-shell button, collapse toggle.

**Why it matters:** The sidebar's grouping modes, collapse mechanism, drag-drop reordering, and toolbar controls are core navigation; removing them would disable session management.

**Verification pointer:** `web/src/components/Sidebar.tsx`

### 2.1 Sidebar grouping modes

**Contract:** **Default view** (when `termyard:view-mode` absent): host groups (if >1 host) or single "Sessions" group, then custom layout groups, then Hidden section, then Scheduled section. **Status view** (when `termyard:view-mode === 'status'`): five status buckets (Needs attention, Working, Idle, Shell, Process) with nested groups. Toggle via toolbar "Group by status" button. Persists to `termyard:view-mode`.

**Why it matters:** Status view is an optional grouping mode; removing it would eliminate an alternative organization strategy.

**Verification pointer:** `web/src/components/Sidebar.tsx`

### 2.1a Layout group dissolution

**Contract:** A layout group (custom pane grouping) is dissolved when its member count drops below 2. When a session is killed or removed, if its group is left with a single remaining member (or none), the group is tombstoned: its server record is marked deleted (persisted with updated timestamp), the Sidebar no longer displays it as a group header, and no group ever has exactly 1 leaf in a live tree. The remaining session returns to standalone display (not under a group header). This applies to both active and background groups.

**Exclusive membership:** After any group tree write (SetTree/ApplyRemote/ApplySnapshot/MigrateKey), a session key is an exclusive leaf in at most one live group — the most-recently-written group wins, and all other groups' references to that session are pruned immediately. This enforcement is server-side and automatic; clients adopt the server's exclusive membership via snapshot and never push partial divergent state back.

**Why it matters:** Single-leaf groups are UI clutter and confuse the grouping model (a group by definition has multiple members); dissolving them keeps the UI consistent and prevents stale group records from lingering after kills. Exclusive membership eliminates membership ambiguity and corruption that arises when a session appears in multiple groups, and ensures that the server is the single authoritative source of truth for group structure.

**Verification pointer:** `pkg/groupsync/groupsync.go` (enforce method, SetTree/ApplyRemote/ApplySnapshot/MigrateKey callers), `web/src/state/workspaceReducer.ts` (groups/snapshot case, push gating)

### 2.2 Sidebar collapse

**Contract:** Global collapse via `$mod+\` (Ctrl/Cmd+Backslash) toggles sidebar between full width (~288px), narrow 44px rail, or hidden (0px). In the 44px rail, each session row renders its first uppercase label character, centered. A small host-color dot appears at the top-right corner when multi-host is active. No avatar circles, status rings, badges, connector lines, or tooltips. Child element widths are clipped to the 44px rail; no row widens the layout slot. Hovering the collapsed rail temporarily overlays the main content at full width: opens over 220ms ease-out, closes after a 120ms grace period over 180ms ease-in. Hover expand is suppressed in hidden mode. Collapse never changes persisted state. Hidden mode remains width 0px and uses its explicit expand control. Per-group collapse: bracket icon toggles each group. Per-host collapse (multi-host only): header bracket toggles host group. Per-schedule: "Scheduled (N)" button arrow toggles schedule list. Per-hidden: "Hidden (N)" button arrow toggles hidden list. All collapse state persists except hidden list (ephemeral per session).

**Why it matters:** The collapse keyboard shortcut and per-group/host toggles enable flexible space management; removing them would lock the UI layout.

**Verification pointer:** `web/src/components/Sidebar.tsx`

### 2.2a Mobile swipe-from-left to open

**Contract:** On touch devices, swiping from the left edge of the screen opens the sidebar if collapsed. Gesture: single-touch `touchstart` at `clientX < 24`, then `touchmove` with `dx > 60` and `|dy| < 40` triggers `setSidebarCollapsed(false)` once per gesture. Fired only for gestures starting at the left edge; middle/right-edge swipes ignored. Passive listeners (no preventDefault). Gesture tracking cleared on `touchend`.

**Why it matters:** Mobile users with collapsed sidebars need an accessible way to reopen it without requiring UI screen real estate for a persistent button.

**Verification pointer:** `web/src/App.tsx` (swipe touch effect)

### 2.3 Sidebar width adjustment

**Contract:** Drag right-edge resize handle to adjust width (clamped 260–560px). Width persists across sessions via callback. Narrow mode (<260px) → arrows/dots only; full mode shows labels/uptime/agent icons/activity. Docked terminal split mode (right side of Overview) also constrained by available space.

**Why it matters:** User-adjustable sidebar width is an explicit feature; removing the resize handle or width persistence would remove control.

**Verification pointer:** `web/src/components/Sidebar.tsx`

### 2.4 Session row (Sidebar)

**Contract:** Displays session name, host color dot (multi-host only), optional AI-naming spinner, agent mark icon (if agent active), branch icon + project folder + session label. Uptime shown (e.g. "45m"). In collapsed mode, shows only the first uppercase label character centered in the 44px rail, with an optional host dot at the row's top-right corner; no avatar circle, no status ring, no tooltip. Second row is a single activity line with priority fallback: live tool event message (active tool activity, or the question text from a "waiting" event) → user prompt (while status is "working", since last agent message/prompt preview may be stale from a prior turn) → last agent message → prompt preview → user prompt (idle/waiting fallback) → "Waiting for prompt"/idle command; the "›" prefix appears whenever the user prompt is what's shown. Paired with a status badge (colored by status, pulsing when config says so). Drag to reorder: visual drop indicators (edge zones → reorder, middle → pair/split). Right-click → context menu. Selection highlights row. Enter/Space confirm selection. Inline rename on double-click or rename menu item (Enter/Escape to submit/cancel).

**Why it matters:** All visible session metadata (name, uptime, agent, branch, activity) and interaction modes (drag, right-click, inline rename) are user-facing contracts.

**Verification pointer:** `web/src/components/Sidebar.tsx`

---

## 3. Top bar

**Contract:** Fixed header showing Termyard logo + home button, session count, primary alert (if waiting/error/stuck event: tool name + status + session + message, dismissible, auto-dismiss if configured), system dials (CPU %, Memory %), Settings gear button, and overflow menu (Port Forwards, Schedules, Wiki, Help). Update banner shows when new version available (dismiss button, click to install).

**Why it matters:** The alert banner's auto-dismiss behavior and overflow menu are feature contracts; removing them would lose UI organization.

**Verification pointer:** `web/src/components/TopBar.tsx`

### 3.1 Primary alert banner

**Contract:** When a tool event reaches waiting/error/stuck status, banner shows pulsing dot + tool name (uppercase, brand color) + status label + `host: session` reference + optional message. Click jumps to that session. Dismiss × clears it. Optional footer button `+{n}` shows extras popover (each extra event clickable, "Clear all" button at bottom). Auto-dismiss timer configurable via `agent_banner.auto_dismiss_seconds` pref (0 = manual, >0 = seconds delay).

**Why it matters:** Alert auto-dismiss timing is a measurable UX contract; changing it silently would alter notification behavior.

**Verification pointer:** `web/src/components/TopBar.tsx`

---

## 4. Tiled view (split panes)

**Contract:** Click Overview card → docks terminal in right split (resizable, width persists). Right-split header has "Open full view" button (opens full session view) and close button (kills split, session stays alive). Multiple open panes show split layout: horizontally stacked, vertically stacked, or grids. Drag dividers to resize (min 200px per pane). Pane header buttons: Split H/V (new split direction), Pop out (external window if supported, otherwise toast warning reason), Detach (removes pane from split), Kill (two-step confirmation, auto-disarm on header leave). Drag-drop sessions onto edges or center: edge = split in that direction, center = swap or split, sides = move pane. Full-screen toggle per pane via `$mod+Shift+F` (exits with Esc).

**Why it matters:** The resizable split mechanism, pop-out button, drag-drop zones, and full-screen toggle are all observable features.

**Verification pointer:** `web/src/components/TiledView.tsx`, `web/src/lib/paneTree.ts`

---

## 5. Quick Switcher

**Contract:** Triggered by `$mod+Shift+K` (Ctrl/Cmd+Shift+K). Modal showing searchable list: waiting events (oldest first, colored by tool), then navigation (Overview), then actions (New Session), then sessions + windows (indented). Fuzzy case-insensitive search. Arrow up/down cycle, Enter selects, Escape closes. Placeholder text changes based on state. Footer hints: `↑↓ Navigate | ↵ Select | ESC Close`.

**Why it matters:** The keyboard trigger, search order (waiting first), and keybindings are observable contracts.

**Verification pointer:** `web/src/components/QuickSwitcher.tsx`

---

## 6. New Session Modal

**Contract:** Triggered by `$mod+Shift+Enter` (Ctrl/Cmd+Shift+Enter) or split pane flow. Input fields: location (required, placeholder `~`, recent-locations dropdown on focus), optional create-as-worktree checkbox (reveals branch input), 6 agent preset buttons (grid 3×2, star badges for default agent, click to select/deselect, click star to set default), command input (free text or preset), session name (auto-suggests based on location + branch), optional host select (when >1 host). "Create" button submits with error validation. Esc closes or clears dropdown if open. Enter submits.

**Why it matters:** The preset agent buttons, worktree flow, and auto-suggested naming are user-visible; removing them would change session creation.

**Verification pointer:** `web/src/components/NewSessionModal.tsx`

---

## 7. Session Actions Menu

**Contract:** Right-click context menu (or long-press on touch, or contextmenu keyboard). Items: **Rename** (inline input, Enter/blur submit, Esc cancel), **AI rename** (sparkle button, async), **Hide/Unhide**, **Background/Foreground**, divider, **Kill** (first click arms red, second click confirms), **Kill + remove worktree** (worktrees only, two-step confirmation). Menu positioned at cursor, z-1000, closes outside-click or Esc. Rename input auto-focuses and selects.

**Why it matters:** Two-step kill and the inline rename UX are explicit; removing them would change user confirmation flow.

**Verification pointer:** `web/src/components/SessionActionsMenu.tsx`

---

## 8. Settings

**Contract:** Opened via `$mod+,` (Ctrl/Cmd+Comma). Drawer with 4 buckets: Look (themes, terminal font family/size/scrollback, fullscreen alerts toggle), Yard (default view, sidebar collapse mode, AI naming endpoint/key/model), Alerts (push notifications toggle, alert statuses multi-select, auto-dismiss seconds, agent setup status), System (peer list + connect/forget/reconnect buttons, wiki install button, security auto-lock timeout, sign-out button, about + version/update rows). All settings apply instantly (no Save button) with "SAVING…" feedback during PUT — **except AI Naming's endpoint/API key/model fields**, which are staged locally and require an explicit Save button (added because the previous PUT-per-keystroke had no debounce, no validation, and no rollback on failure). The AI Naming Save button intentionally allows an empty endpoint even when naming is enabled — the backend (pkg/namer) falls back to TERMYARD_NAMER_*/TERMYARD_OPENAI_* env vars in that case, so an empty endpoint is a valid, supported configuration, not an error. Save shows its own "SAVING…" pulse during the request, and on failure reverts the staged fields to the last-known-good server values with an inline error message. The Predictive Echo toggle and the DOM/WebGL renderer select have been removed (see 9.6, 9.12).

**Why it matters:** Instant apply, the specific pref keys, and auto-lock timeout are all observable contracts.

**Verification pointer:** `web/src/components/SettingsDrawer.tsx`, `web/src/components/Settings.tsx`

### 8.1 Settings mismatches found

**Contract:** `terminal.scrollback` pref default: FE 50000 vs BE 5000 (mismatch documented). `notifications.statuses` default: FE `['waiting','stuck','error','completed']` vs BE `['waiting','error','completed']` without 'stuck' (mismatch documented). `default_agent` pref exists backend + FE but has **zero Settings UI** — only API-reachable and used by NewSessionModal. **Resolved:** AI Naming's endpoint/API-key/model fields no longer PUT on every keystroke and no longer silently keep bad optimistic state on a failed save (see 8 above) — this was the only Settings control with zero error UI on PUT failure; it is now the first (and only) Settings control with inline error feedback, scoped deliberately to just these 3 fields.

**Why it matters:** Settings defaults must be consistent between client and server; mismatches indicate data integrity bugs.

**Verification pointer:** `web/src/components/Settings.tsx`, `pkg/server/routes_preferences.go`

---

## 9. Terminal

### 9.1 Terminal connection lifecycle

**Contract:** Terminal connects via WebSocket (local: `/ws/session`; remote: `/ws/session?host=fingerprint`; peer data: separate dedicated conn per terminal). States: disconnected → connecting → open → replaying | live → backoff → connecting (cycle). Heartbeat ping every 10s (answered pong). No traffic ≥25s → timeout, reconnect. Replay buffer replayed within 250ms of open; exceeded → force live + flush. Reconnect backoff 2s (immediate if tab becomes visible). When hidden tab → defer reconnect until visible; no busy-loop.

**Why it matters:** Connection timing, ping/pong keepalive, and visibility-aware reconnect prevent spurious disconnects and battery drain.

**Verification pointer:** `web/src/lib/terminal/connectionMachine.ts`, `pkg/ws/pty_terminal.go`

### 9.2 Terminal copy / paste

**Contract:** Selection auto-copies to clipboard (normalizes: strips trailing spaces/tabs per line). `$mod+C`: if selection → copy + clear + swallow (no SIGINT); else → send raw Ctrl+C (0x03). `$mod+V`: clipboard text or image paste. Image paste via WS `paste-image` message (max 10 MiB, stored to disk, path typed into terminal). Right-click with selection → custom menu (Copy, Open file if text is path-like, Close with Esc). Right-click without selection → normal browser context menu.

**Why it matters:** Copy-on-select and Ctrl+C behavior are standard terminal UX; changing them would break muscle memory.

**Verification pointer:** `web/src/components/terminal/useTerminalInput.ts`, `web/src/components/terminal/SelectionMenu.tsx`

### 9.3 Clipboard menu (mobile)

**Contract:** Mobile/coarse-pointer viewports show 9-button bar at bottom. Clipboard button toggles menu: **Paste** (read clipboard text, paste into terminal), **Paste file…** (file-picker, upload to server, path typed into terminal), **Copy** (capture last 40 lines of terminal, modal for selection, copy via system). Menu closes on selection.

**Why it matters:** Mobile clipboard access is a platform-specific contract; removing it would break mobile usage.

**Trigger (also):** Long-press (~500ms, single touch, <10px movement) on the terminal opens the same capture modal directly.

**Verification pointer:** `web/src/components/terminal/useTerminalInput.ts`, `web/src/components/terminal/MobileKeys.tsx`, `web/src/components/Terminal.tsx` (long-press effect)

### 9.4 Drag-drop / file uploads

**Contract:** Drag files onto terminal → upload dialog with progress. Drop sends file to server, server stores with quoted path, path typed into terminal if connected (else injected later when reconnected). Visual indicator while dragging: `ring-2 ring-primary` outline. No client-side file-type or size filter; server validates. Upload progress: bar shows %, Cancel button, auto-dismiss 4s on success (or persist if terminal disconnected). Multiple files uploaded sequentially. Failed uploads show error message + Retry button (or Dismiss to discard).

**Why it matters:** Drag-drop is a discoverable interaction; auto-dismiss timing and sequential upload are behavioral contracts.

**Verification pointer:** `web/src/components/terminal/useTerminalInput.ts`, `web/src/hooks/useFileUpload.ts`, `web/src/components/UploadStatus.tsx`

### 9.5 Mobile key bar

**Contract:** Appears on coarse-pointer or viewport width <900px (visible only when terminal `keyBarEnabled = true`, i.e., pane is active). Portal-rendered into fixed bottom bar. 9 buttons: **Clipboard menu toggle**, **Compose input** (opens textarea modal on both mobile and desktop), **Ctrl sticky** (toggles, next letter → Ctrl+letter), **Alt sticky** (toggles, next letter → Alt+letter), **Esc** (sends ESC, 0x1b), **Tab** (sends TAB), **Backspace** (sends DEL, 0x7f), **Page Up/Down** (swipeable gesture key, vertical arrows), **Arrow cross** (swipeable gesture key, 4-direction). Sticky modifiers clear on next input; gesture keys auto-repeat if held >260ms, then every 80ms. Dead zone for swipe: 18px threshold before direction fires. **Keyboard visibility tracking:** Bar bottom padding adapts to on-screen keyboard: when `window.visualViewport.height < window.innerHeight * 0.8` (keyboard visible), padding-bottom is 3px; otherwise, padding-bottom is `calc(env(safe-area-inset-bottom, 0px) + 12px)` to avoid home indicator overlap.

**Why it matters:** The 260ms hold delay, 80ms repeat, and 18px dead zone are measurable UX parameters; changing them would affect mobile usability.

**Verification pointer:** `web/src/components/terminal/MobileKeys.tsx`

### 9.5a Compose Input Modal

**Contract:** Compose input modal allows composing multi-line terminal input without requiring Enter to submit. Accessible via:
  - **Mobile:** Compose button in key bar (keyboard icon, 2nd button from left).
  - **Desktop:** Compose button in terminal toolbar (top-right, keyboard icon) or keyboard shortcut `$mod+Shift+U`.

Modal behavior (both mobile and desktop): **Textarea** with monospace font, 160px height, word-wrap off. **Keyboard interactions:** Escape closes modal (refocuses terminal). Enter sends text with trailing newline (`\r`), closes modal, clears text. Cmd/Ctrl+Enter fills text without newline and closes. Shift+Enter inserts newline in textarea. Remounting a pane (group membership change) must not reopen the modal from a stale shortcut request. **Two buttons:** "Fill" (sends text without newline), "Send" (sends text with newline). **Speech-to-text:** When the browser supports the Web Speech API, a "Speak" button (left-aligned in footer) toggles voice dictation; finalized transcript chunks append to the textarea (space-separated), the button turns red and pulses while listening ("Stop"), and dictation stops automatically when the modal closes. Uses `navigator.language` as recognition language. Hidden entirely on unsupported browsers. Textarea autofocus on open. Modal uses fixed inset-0 overlay (black/70% background), centered dialog with max-width 2xl.

**Why it matters:** Multi-line input is a contract for pasted scripts and structured commands; modal behavior (Escape closes, Enter sends) is a consistency contract with other app modals.

**Verification pointer:** `web/src/components/Terminal.tsx` (composeOpen, composeText state; modal markup; keyboard handlers); `web/src/hooks/useSpeechToText.ts` (Web Speech API wrapper)

### 9.6 Predictive echo

**Status: removed.** Predictive echo (opacity-0.4 italic overlay of pending typed characters before server echo, ~500ms confirm timeout) was experimental, off by default, and a real maintenance surface for something almost nobody enabled. `web/src/lib/predictive-echo.ts`, its Settings toggle, and the `terminal.predictive_echo` preference key have all been deleted (frontend type, defaults, and backend struct field). There is no replacement — typed characters simply wait for authoritative PTY echo as with any plain terminal.

**Why it matters:** Removing dead optionality reduces surface area without changing default behavior for the overwhelming majority of users who never enabled it.

**Verification pointer:** `pkg/preferences/preferences.go`, `web/src/hooks/usePreferences.ts` (field absent)

### 9.7 File links in terminal

**Contract:** Underlined file paths (e.g., `/home/user/file.rs:12:3`, `./relative/path.js:1`) open in the wiki panel (or fallback new-tab artifact-viewer) only on Cmd/Ctrl-click. Plain clicks remain available for terminal text selection, including double-click selection, and never open the panel. Right-click selection menu also offers **Open file**. Hover → pointer cursor + underline + `Cmd/Ctrl-click to open file` tooltip. Heuristic: path with extension and optional line/column suffix (`:N:M`). URLs (http://, etc.) handled separately by WebLinksAddon. **All path resolution happens server-side:** relative and bare paths resolve against the session's active pane cwd via `POST /file/grant`; `~` / `~/...` resolves against the server's home dir (pkg/server/routes_files.go `resolveFilePath`). Before a path is highlighted, the client checks it against `GET /file/exists` (cached, fails open on error/remote-host) — paths confirmed not to exist are not underlined or clickable.

**Why it matters:** File link recognition heuristic is a contract; changing it would affect which paths are clickable. The existence check is a contract too: it must fail open (never hide a real link on a network hiccup) and must not mint a file-open capability just to check. Server-side resolution eliminates flaky client-side cwd guessing.

**Verification pointer:** `web/src/lib/fileLinkProvider.ts`, `web/src/lib/pathExists.ts`, `web/src/components/WikiPanel.tsx` (server-side grant flow), `pkg/server/routes_files.go` (`resolveFilePath`, `handleFileExists`, `handleFileGrant`)

**Directory paths:** A detected path that resolves to a directory is openable too: `POST /file/grant` returns `{path, is_dir: true}` (no token minted — a directory is not a readable capability), and the wiki panel opens in browse mode rooted at that directory instead of a file view. `GET /file/exists` reports `exists: true` for directories, so directory highlights are not filtered out. Limitation: directories on remote peers are not browsable — the remote file-read relay only materialises single files, so a remote directory grant still fails with "path is a directory" (surfaced as the usual open-failure toast).

**Wiki panel open failures:** If the resolved path can't be opened (not found, no active pane cwd, peer unreachable, etc.), the panel shows a "Could not open file" message in place of the blank viewer when nothing else is displayed, and a toast when a failed open would otherwise silently do nothing on top of an already-open file. wiki-viewer itself never sees a request it can't satisfy, so this message is generated by the panel, not wiki-viewer.

**Verification pointer:** `web/src/components/WikiPanel.tsx`

### 9.8 Artifacts / detected files

**Contract:** Tool events may include artifacts (file paths) only for write-ish tools (write, edit, multiedit, str_replace, apply_patch, notebook_edit). Detected files badge shown in terminal toolbar (count). Click badge → panel opens showing detected files (newest first, max 100 merged). Per file: path (truncated, title shows full), source/tool/kind labels. "Open full" button (opens in wiki or token new-tab), Download button (artifact token, temp `<a download>`), Preview button (inline preview if text/image). Refresh button re-fetches from `/api/artifacts?session=…` and evicts any files that were deleted from disk. Artifacts are scoped per session and cleared when the session is killed. Persisted artifacts older than 7 days are dropped on server load. **Note:** Pi extension currently reports write/edit/multiedit calls made directly by the agent via tool_execution_end events; file writes performed inside fabric_exec programs are not exposed by Pi API and thus not captured as artifacts.

**Why it matters:** Artifact detection, write-tool filtering, max 100 limit, and automatic eviction of deleted files are observable contracts.

**Verification pointer:** `web/src/components/Terminal.tsx`, `web/src/hooks/useArtifacts.ts`, `web/src/lib/artifactPreview.ts`

### 9.9 Pop-out window (Picture-in-Picture)

**Contract:** Pop-out button (absolute top-right, opacity-0 until hover/focus) opens terminal in floating window (Document PiP API). Requirements: secure context (HTTPS or localhost) + browser support (Chrome, Edge, Firefox 151+). Unavailable → toast warning with reason (e.g., "Pop-out needs secure connection. Open Termyard via localhost or serve with TLS (--tls)."). User closes PiP window → terminal auto-restores to tab + `onRestore` callback. Unmount while popped → idempotent restore. Terminal keeps all functionality in PiP.

**Why it matters:** Pop-out is opt-in and must handle close gracefully; missing the pagehide restore would leave users with no terminal.

**Verification pointer:** `web/src/lib/terminal/pip.ts`, `web/src/components/Terminal.tsx`

### 9.10 Terminal keyboard shortcuts

**Contract:** `$mod+Shift+F` (Ctrl/Cmd+Shift+F) → toggle fullscreen (only when active pane and `onToggleFullscreen` provided). `$mod+Shift+U` → open compose input modal on the active pane (Esc to close, Enter to send with newline, Shift+Enter for newline in textarea). Esc → exit fullscreen (only when fullscreen mode and quick-switcher not open). `$mod+C` → copy-or-SIGINT. `$mod+B` → Ctrl+B (tmux prefix). `$mod+V` → paste. Fullscreen window-level capture (capture-phase) intercepts keydown; suppresses to terminal only if fullscreen active. **Alt+Arrow keys** (Alt+Up/Down/Left/Right, no Ctrl/Meta) are intercepted before xterm's encoder and forward the standard Alt-modified CSI sequences (`\x1b[1;3A/B/C/D`) to the PTY; this works around xterm.js 5.5.0's hardcoded non-Mac hack that otherwise rewrites Alt+Arrow to Ctrl+Arrow (`\x1b[1;5*`). Applied uniformly on all platforms; also prevents the browser from treating Alt+Left/Right as history navigation.

**Why it matters:** Keyboard shortcuts are muscle-memory contracts; adding/removing one breaks workflows.

**Verification pointer:** `web/src/components/Terminal.tsx`

### 9.11 Terminal resize / scrollback

**Contract:** Resize message sent on viewport change (ResizeObserver). Terminal buffer scrollback depth configurable via `terminal.scrollback` pref (default FE 50000, note BE default 5000 mismatch). Daemon ring buffer (replay window) defaults to 32 MiB (`--buffer-size`, `pkg/pty/daemon.go`), sized to comfortably retain 50k lines of typical output so the FE scrollback pref is actually reachable after long/high-output sessions; bound is bytes, not lines, so extremely wide/dense output can still exceed it. Scroll-to-bottom on output and after width reflow unless user actually scrolled up. A wheel or touch gesture only pauses follow-output after the viewport moves above the bottom; a gesture at the bottom that produces no movement does not change scroll state. Wheel/touch scroll emulation for mouse-mode apps: single-touch drag → velocity-based wheel events (threshold 20px per line, multiplier by speed, inertia on release with 0.92 friction). Home/End keys handled. Mobile page-up/page-down → escape sequences.

**Why it matters:** Scrollback depth, scroll behavior, and touch scroll emulation are observable; changing them affects terminal responsiveness.

**Verification pointer:** `web/src/lib/terminal/connectionMachine.ts`, `web/src/components/Terminal.tsx`

### 9.12 Terminal fonts / rendering

**Contract:** Font family control: select from 10 presets (Space Mono [default], JetBrains Mono, Roboto Mono, Fira Code, Menlo, Monaco, Consolas, Courier New, Inconsolata, monospace) **or choose "Custom..." to reveal a free-text input** for any font family string (e.g. Cascadia Code, Berkeley Mono), with a live preview sample rendered in the chosen font. The `terminal.font_family` preference was already a free-form string server-side - this is a frontend-only UX addition, no new preference key. Font size 8-32px (default 13, clamped). Unicode grapheme clustering (`@xterm/addon-unicode-graphemes`) is always loaded, unconditionally, for every terminal - there is no user toggle. All settings apply instantly, xterm re-renders.

**Renderer:** WebGL is now the only rendering path attempted (default changed from DOM to WebGL); the DOM/WebGL Settings toggle has been **removed** — there is no more user-facing renderer choice. WebGL is attempted unconditionally on cold terminal checkout (and opportunistically on reconfigure if not yet loaded); if `WebglAddon` construction or `loadAddon` throws (no GPU/WebGL2 support), the terminal silently continues on xterm's default DOM/canvas renderer with no user-visible error. If the GPU context is lost at runtime, the addon's `onContextLoss` handler disposes it, which causes xterm to fall back to DOM rendering automatically and silently — this fallback path already existed and is preserved unchanged. The backend `renderer` preference key is kept (default changed from `"dom"` to `"webgl"`) for backward-compat with persisted preferences and the API contract, but is no longer read by any rendering-decision code on the frontend.

Terminal theme drives 21 ANSI colors + cursor + selection background.

**Why it matters:** Font and rendering choices are user preference contracts; maintaining two renderer backends was a real cost for a toggle almost nobody used deliberately, so WebGL (the empirically better default) is now unconditional with an automatic, invisible DOM safety net instead of a manual choice.

**Verification pointer:** `web/src/lib/terminalPool.ts` (WebGL load/fallback logic), `web/src/components/Settings.tsx` (font family control), `pkg/preferences/preferences.go` (renderer field comment)

### 9.13 DEC 2026 synchronized updates

**Contract:** PTY output between BSU marker `\x1b[?2026h` and ESU marker `\x1b[?2026l` buffered and written as single atomic write. Markers stripped from output. Straddling marker prefix carried over. Lookalike-prefix bytes pass through. ESU without BSU → no-op.

**Why it matters:** Sync marker support is a performance/UX contract; apps rely on atomic updates for smooth rendering.

**Verification pointer:** `web/src/lib/terminal/connectionMachine.ts`

### 9.14 Terminal toolbar

**Contract:** Session name shown. Ctrl/Alt sticky modifier buttons (mobile only). **Compose button** (keyboard icon, top-right toolbar; opens compose input modal; keyboard shortcut $mod+Shift+U). Artifact count badge (toggles preview panel). Fullscreen toggle button. Mobile key-bar toggle. Pop-out button (absolute positioned top-right). Compose, artifact badge, pop-out, and fullscreen buttons sit in a single glassy pill cluster (top-right, translucent blurred background); cluster idles at 60% opacity, full opacity on pane hover or keyboard focus. Disconnect overlay: pulsing red dot + "Disconnected — Reconnecting" when not connected (position: absolute inset-0 z-10, pointer-events-none, doesn't block input).

**Why it matters:** Toolbar buttons and disconnect overlay are observable; removing them hides status and controls.

**Verification pointer:** `web/src/components/Terminal.tsx`

---

### 9.15 Scroll scrubber

**Contract:** When the buffer is scrollable (length > rows), a thin scrubber track overlays the terminal's right edge. Thumb reflects viewport position/size; dragging (pointer or touch) maps track fraction linearly to the full scrollback via `scrollToLine`, so any depth is reachable in one gesture. Always visible at 50% opacity while scrollable; full opacity while scrolling/dragging (dims back ~1.2s after scrolling stops). 40px touch target; thumb widens and highlights while dragging, follows the finger immediately with scroll jumps coalesced to one `scrollToLine` per frame. Thumb minimum height 10% of track (24px floor).

**Why it matters:** xterm's native scrollbar is nearly unusable on mobile for deep (50k-line) scrollback; the scrubber is the fast-navigation path.

**Verification pointer:** `web/src/components/terminal/ScrollScrubber.tsx`

### 9.16 Wiki Panel mobile

**Contract:** On mobile/coarse-pointer (viewport <900px or touch device), wiki panel enters full-screen modal mode: `fixed inset-0 z-40 bg-canvas flex flex-row`. The drag-resize handle is hidden. The close button in the header remains visible, allowing dismissal. All other UI is hidden behind the panel. On desktop, panel renders as a side dock (resizable, collapsible as before).

**Why it matters:** Mobile screens lack space for a docked file viewer; full-screen modal is the only usable layout on small viewports.

**Verification pointer:** `web/src/components/WikiPanel.tsx` (isMobile state, conditional className)

## 10. Session Lifecycle & Status

### 10.1 Session states

**Contract:** Four states (ranked 0–3): **needs_you** (loud event present: waiting/stuck/error status, pulsing warning dot), **working** (active turn or active tool event, success dot), **idle** (no loud/active events, hollow mute dot), **offline** (host unreachable, stone hollow dot). Board view sorts sessions by state column membership; sidebar uses state for visual indicators.

**Why it matters:** State ranking and visual indicators are observable; removing them would lose session prioritization.

**Verification pointer:** `web/src/lib/sessionState.ts`

### 10.2 Tool events & agent tracking

**Contract:** Initial snapshot from `/api/tool-events` + `/api/active-turns`. Live via WS `tool-event` messages. Tool statuses: **active/running** (success green), **waiting** (warning ●), **error** (! destructive), **stuck** (◴ destructive), **completed** (✓ success). Events include tool name, status, host, session, window, pane, message, timestamp, optional artifacts. Events auto-dismissed on session kill or status change to completed. Waiting events trigger primary alert banner (if configured auto-dismiss).

**Why it matters:** Tool event lifecycle and status indicators are user-visible contracts.

**Verification pointer:** `web/src/hooks/useToolEvents.ts`, `pkg/toolevents/tracker.go`

### 10.3 Activity tracking

**Contract:** Per-session activity snapshot: idle_seconds, total_bytes. Initial fetch `/api/activity`, live via WS `activity` messages every 5s. Used for idle display and (backend) waiting promotion after silence. Not persisted; lost on server restart.

**Why it matters:** Activity metrics drive idle time display; removing them would hide user session state.

**Verification pointer:** `web/src/hooks/useActivity.ts`, `pkg/activity/tracker.go`

### 10.4 Session discovery & pruning

**Contract:** Snapshot from `/api/sessions` (local + peer merged). Live events: `sessions-changed`, `session-added`, `session-removed`, `session-renamed`. **Session removal:** triggered by either a `session-removed` broadcast or absence from an authoritative reconnect snapshot (no N-consecutive-snapshot prune, no polling). Removal is immediate and bounded best-effort to ~1s under normal operating load. When removed, the session is pruned from UI (list, pane tree, terminal pool, persisted groups); disconnected (failed snapshot fetch) → nothing pruned, filters protected. Connection state (`connection.live`) drives "offline" display when events WS down. URL rewritten if current session renamed.

**Why it matters:** Session discovery and prune timing are observable; removing them would break session visibility.

**Verification pointer:** `web/src/state/workspaceReducer.ts`, `pkg/state/sessions.go`

### 10.4a Session "unreachable" state

**Contract:** When a local session's daemon PID is alive but the server's watch connection to it is broken (socket error, dial failure, output read error), the session enters an "unreachable" state and is displayed with the same visual treatment as a disconnected/offline terminal — opacity-60, muted, not responsive to input. The session is never removed (live PID is sacred). The server reconnects to the daemon with bounded exponential backoff (250ms → 5s, capped, forever) until the watch succeeds. On reconnect, the `unreachable` flag clears and the session becomes interactive again. Peers propagate unreachable status normally over the sync protocol. The terminal render of an unreachable session shows the disconnect overlay (pulsing dot + "Disconnected — Reconnecting" if applicable).

**Why it matters:** Transport-level failures must never remove a session with a live daemon; visual indication is required for safety (user awareness of actual connectivity loss vs. momentary network blip).

**Verification pointer:** `web/src/components/Terminal.tsx`, `web/src/lib/sessionState.ts`, `pkg/pty/registry.go` (Watch backoff logic)

### 10.5 Session attributes (background / hidden)

**Contract:** Server-authoritative last-write-wins store (mesh-wide). `hidden` excludes session from main view, shown in Hidden section. `background` runs quietly, excluded from glance counts, shown in Hidden/Background sections. Persist across peers. Optimistic local update + WS reconcile.

**Backgrounding a tiled session:** When `background=true` for a session in any saved group layout, the backend atomically removes it from every saved group tree (leaf nodes deleted, split panes collapse to remaining sibling, emptied groups tombstoned), sets the background bit, and persists both group trees and attrs. Either the whole operation succeeds (session removed from layout, background bit set, broadcasts fire) or nothing persists—no partial state. Foregrounding (`background=false`) only clears the bit; it never re-adds the session to any layout (clients are responsible for re-tiling it if desired).

**Why it matters:** Background/hidden persistence is a contract; losing it would reset user organizational choices on peer reconnect. Backgrounding a tiled session is an atomic cross-store operation; losing atomicity would orphan sessions in dead layout positions or inconsistently set bits across peers.

**Verification pointer:** `web/src/hooks/useSessionAttrs.ts`, `pkg/sessionattrs/sessionattrs.go`, `pkg/groupsync/groupsync.go`

### 10.5a Groups sync & exclusive membership

**Contract:** Group trees are server-authoritative; enforcement of exclusive membership and dissolution happens on the backend on every write path (SetTree, ApplyRemote, ApplySnapshot, MigrateKey). After any write, a session key is a leaf in at most one live group — the most-recently-written group wins, others' leaves are pruned, and groups falling below 2 leaves are tombstoned (marked deleted, timestamp updated, persisted to `groups.json` but not removed). Peer tree deltas with timestamps older than a tombstone timestamp are rejected by last-write-wins comparison, ensuring tombstones are never resurrected. Clients adopt the server's exclusive membership via `groups/snapshot` (authoritative) and never push partial divergent state back; only user-initiated edits (split/close/resize/drag/pair) trigger PUTs, not group switches or passive reconciliation. Dead sessions (owner host online, session absent from `/api/sessions`) are pruned opportunistically on GET `/api/groups`, healing corrupt group trees. When a group's membership is enforced (exclusive winner chosen, other groups' leaves pruned), the naming coordinator is notified via `ObserveTreeMutation` to detect and clean up stale UNNAMED placeholder names.

**Why it matters:** Exclusive membership eliminates ambiguity (session in multiple groups), corruption (stale state on peers), and ghosts (dead sessions lingering in trees). Server-authoritative enforcement and LWW tombstoning ensure all peers converge to the same group structure. Opportunistic dead-session healing maintains correctness over time without requiring administrative intervention. Client push gating prevents local reconciliation from being misinterpreted as user edits, avoiding amplification of stale state across the mesh.

**Verification pointer:** `pkg/groupsync/groupsync.go` (enforce method, Reconcile method, SetTree/ApplyRemote/ApplySnapshot/MigrateKey), `pkg/server/routes_groups.go` (GET /api/groups calls Reconcile), `web/src/state/workspaceReducer.ts` (groups/snapshot case, push gating on user edits, skipTreeAdoptFor guard)

### 10.6 Crash & recovery

**Status: Removed.** Crash recovery — daemon respawn UI, `/api/crashed-sessions` endpoint, `RecoveryPanel` component, and all associated lifecycle tracking — has been deleted. Sessions are added when launched or adopted at boot, and removed only when confirmed dead. A killed daemon is removed like any normal exit: no separate "crashed" state exists.

---

## 11. Keyboard Shortcuts (complete)

All context: terminal or global App.tsx.

| Shortcut | Context | Action |
|---|---|---|
| `$mod+Shift+K` | Global | Quick Switcher (README says `Ctrl+K` — stale) |
| `$mod+Shift+Enter` | Global | New Session / Split Pane |
| `$mod+Shift+.` / `/` | Global | Next / Previous session (cycle) |
| `$mod+Shift+H` | Global | Overview (README says `Ctrl+H` — stale) |
| `$mod+,` | Global | Settings |
| `$mod+/` | Global | Help toggle |
| `$mod+\` | Global | Toggle sidebar |
| `$mod+Shift+G` | Global | Toggle wiki panel (wiki enabled only) |
| `$mod+Shift+F` | Terminal | Toggle fullscreen |
| `$mod+Shift+U` | Terminal | Open compose input modal |
| `Esc` | Terminal (fullscreen) | Exit fullscreen (skip if quick-switcher open) |
| `$mod+C` | Terminal | Copy selection or send SIGINT (0x03) |
| `$mod+B` | Terminal | Tmux prefix (0x02) |
| `$mod+V` | Terminal | Paste clipboard |
| Sticky Ctrl + letter | Terminal (mobile) | Raw Ctrl+letter byte (0x01–0x1a) |
| Sticky Alt + letter | Terminal (mobile) | ESC+letter |
| `Esc` | Modal (quick switcher, settings, etc.) | Close modal |
| `Enter` | Form | Submit form |

**Note:** `$mod` = Ctrl (Windows/Linux) or Cmd (macOS).

**Stale README shortcuts:** Quick Switcher (README: Ctrl+K, actual: Ctrl+Shift+K), New Session (README: Ctrl+N, actual: Ctrl+Shift+Enter), Overview (README: Ctrl+H, actual: Ctrl+Shift+H). All noted in-doc.

---

## 12. Auth & Lock

**Contract:** Startup: check `/api/auth/status` → if `auth_required: false` → enter; if `needs_setup: true` → Setup mode (password entry ≥8 chars, confirm match); else login (password entry). Session cookie `termyard_session` (HttpOnly, SameSite=Strict, 24h sliding TTL). Any 401 on non-auth endpoints → auto-logout. **Lock screen:** `lock_timeout_minutes` auto-lock (inactivity triggered, password re-entry to unlock). Lock state keeps WS alive (notifications work while locked).

**Why it matters:** Auth timeout and lock screen are security contracts; changing them affects threat model.

**Verification pointer:** `web/src/hooks/useAuth.ts`, `pkg/auth/auth.go`

### 12.1 TLS & security

**Contract:** `--tls` → self-signed HTTPS (in-memory ECDSA cert, 1yr, SANs: localhost/127.0.0.1/::1/hostname/LAN IPs). `--tls-cert`/`--tls-key` → real certs. Secure context required for features: clipboard write, Document PiP, service-worker push notifications. Local Unix socket → unauthenticated (loopback trust). Rate limits: setup 5/min, login 10/min, bootstrap 5/min per-IP; exceeds → 429 + Retry-After.

**Why it matters:** TLS mode and rate limits are security contracts; removing them would expose brute-force attacks.

**Verification pointer:** `pkg/auth/middleware.go`, `pkg/server/server.go`

---

## 13. Notifications & Toasts

**Contract:** WS `notice` events → bottom-right stacked toasts (max 4 visible, newest on top). Severity: error 12s auto-dismiss, else info/warn 8s. Manual × dismiss anytime. Browser notifications: on waiting/error/stuck/completed status transition (if enabled in prefs), closed on status change to active/completed. The `completed` transition (agent finished its turn, working -> idle) fires a "<tool> finished" notification so a done agent is surfaced, not just a waiting one. All four statuses (waiting/stuck/error/completed) are individually toggleable in Settings and default to on. On completion (when `completed` is enabled) the app also raises an in-UI info toast ("<tool> finished / Completed in session ...") and the sidebar row shows a transient green "done" badge and border highlight for ~6s before settling back to idle (`useToolEvents` `isSessionRecentlyDone`), so a finished agent is visible in-app even without OS notification permission. Push subscriptions (service worker): HTTPS or localhost, in-memory (lost on server restart; browser re-subscribes on load). Push fires for waiting/error/stuck/completed (backend `pkg/webpush/sender.go`), matching the browser-notification statuses. Fullscreen mode suppresses banners (not toasts). **PWA push (iOS/Android):** app is an installable PWA via `/manifest.webmanifest` (`display: standalone`) and a root-scope service worker `/sw.js` that handles `push` (shows `payload.title`/`body`, icon `apple-touch-icon.png`, badge `favicon-48.png`, tag `session:window`) and `notificationclick` (focuses an existing window or opens `/`). On iOS 16.4+, push requires: home-screen install (Add to Home Screen), standalone manifest, a user-gesture-triggered permission prompt (the Settings/Setup ENABLE button calls `subscribe()`), HTTPS, and the active service worker.

**Why it matters:** Auto-dismiss timing and max-visible count are observable; changing them affects notification UX. Without the manifest and `/sw.js`, iOS PWAs cannot receive push at all.

**Verification pointer:** `web/src/components/Toasts.tsx`, `web/src/hooks/useNotifications.ts`, `web/src/hooks/usePushNotifications.ts`, `web/src/hooks/useToolEvents.ts` (`isSessionRecentlyDone`), `web/src/components/Sidebar.tsx` (done badge/highlight), `web/src/App.tsx` (completion toast), `web/public/sw.js`, `web/public/manifest.webmanifest`, `web/index.html` (manifest link)

---

## 14. Multi-host / Peering

**Contract:** Symmetric P2P mesh (no hub). Any node can dial any peer. Bootstrap pairing: `/api/peers/bootstrap` with password (rate-limited 5/min). Once paired, auto-reconnect default on with backoff 1s→30s + jitter. **Peer statuses:** idle, dialing, connected, backoff (retry countdown + last seen + last error), listener. Forget removes peer (propagates over live link). Peer identity: fingerprint + ed25519 public key. **Remote terminals:** per-terminal dedicated WS connection (no head-of-line blocking; ADR-per-terminal-connections.md). Session/upload/group/attrs/order sync fanned via control connection. No transitivity (B must pair C directly).

**Why it matters:** Auto-reconnect backoff, per-terminal connections, and control-connection fanout are architectural contracts; removing them would break remote multiplexing.

**Verification pointer:** `pkg/peer/*`, `pkg/server/routes_peers.go`, docs/adr-per-terminal-connections.md

### 14.1 Per-terminal connections (ADR verified)

**Contract:** Each remote terminal gets dedicated WS connection (one-to-one with browser WebSocket). Single persistent control connection per peer carries no bulk PTY output, only state/presence/session list/layout sync. Control connection cannot be starved. Remote PTY data flows over separate connection per terminal, same reliability model as local.

**Why it matters:** This is the foundational ADR for remote reliability; removing per-terminal connections would reintroduce head-of-line blocking.

**Verification pointer:** `pkg/peer/session_stream.go`, `pkg/server/routes_sessions.go`, docs/adr-per-terminal-connections.md

### 14.2 Peer status polling

**Contract:** `/api/hosts` polled every 30s. Returns host list: id, name, version, local flag, online/offline, session count, system stats, last_seen timestamp. Used to show peer availability and sync version info.

**Why it matters:** Poll interval and returned fields are observable; changing them affects peer status freshness.

**Verification pointer:** `web/src/hooks/useHosts.ts`, `pkg/server/routes_peers.go`

---

## 15. Agent Detection (docs/agent-detection.md)

**Contract:** Five detection layers (most→least precise): (1) Hook-based (native agent API, instant/exact, instant notification, e.g. Claude Code PreToolUse/PostToolUse), (2) Process tree (~5s, high precision), (3) Silence + capture-pane (~10–20s, medium), (4) Inactivity promoter (30s no hook activity → "waiting", low), (5) Reconciler (clears stale events on process exit, ~3s reconciliation). Per agent: unique hook support (Claude Code full native, OpenCode full native, Copilot/Codex partial). **Setup:** `termyard agent-setup` detects agents, writes hooks; `--dry-run` previews; `--block` makes hook failures propagate (default resilient: ` || true`). Status: `GET /api/agent-status` shows installed/configured per agent.

**Why it matters:** Detection latency and per-agent precision are observable; removing detection layers would slow responsiveness.

**Verification pointer:** `pkg/agentcheck/agentcheck.go`, `pkg/commands/agent_setup.go`, docs/agent-detection.md

---

## 16. CLI (commands & flags)

All commands below are the entire HTTP surface exposed to integrations and the desktop.

### server

Starts web dashboard server.

| Flag | Type | Default | Env | Purpose |
|---|---|---|---|---|
| `--port`, `-p` | int | 7654 | `TERMYARD_PORT` | HTTP server port |
| `--socket` | string | auto | `TERMYARD_SOCKET` | Unix socket path (local notify CLI) |
| `--no-auth` | bool | false | `TERMYARD_NO_AUTH` | Disable auth |
| `--tls` | bool | false | `TERMYARD_TLS` | Self-signed HTTPS |
| `--tls-cert` | string | "" | `TERMYARD_TLS_CERT` | Real cert PEM path |
| `--tls-key` | string | "" | `TERMYARD_TLS_KEY` | Real key PEM path |
| `--debug-pprof` | bool | false | `TERMYARD_DEBUG_PPROF` | Mount `/debug/pprof` |

### install / uninstall

Linux: `~/.config/systemd/user/termyard.service` + enable. Darwin: `~/Library/LaunchAgents/com.termyard.server.plist` + launchctl. Prints "Web UI: https://localhost:7654" on success.

### update

Self-update from GitHub Releases.

| Flag | Type | Default | Purpose |
|---|---|---|---|
| `--channel` | string | auto-detect | `stable` or `nightly` |
| `--version` | string | "" | Pin to tag |
| `--repo` | string | `anh-chu/termyard` | GitHub repo |
| `--check` | bool | false | Check only (dry-run) |
| `--force` | bool | false | Reinstall even if match |

Atomic swap: old → .bak, new → original; restore on fail.

### notify

Send tool event to server.

| Flag | Type | Default | Required | Purpose |
|---|---|---|---|---|
| `--tool`, `-t` | string | — | **yes** | `claude`, `codex`, `opencode`, `pi` |
| `--status`, `-s` | string | "" | if no `--event-data` | `active`, `waiting`, `completed`, `error` |
| `--message`, `-m` | string | "" | no | Human message |
| `--user-prompt` | string | "" | no | User's first message |
| `--agent-message` | string | "" | no | Agent's last response |
| `--session` | string | auto-detect | no | Session name |
| `--window` | int | auto-detect | no | Window index |
| `--pane` | string | auto-detect | no | Pane ID |
| `--server` | string | `http://localhost:7654` | no | Server URL |
| `--socket` | string | auto | no | Unix socket |

Session auto-detect: `TERMYARD_SESSION`/`TERMYARD_PANE` env first, then tmux, else active pane. HTTP `POST /api/tool-event` with bearer token. Unix socket first, HTTP fallback.

### agent-setup

Detect + configure agent hooks.

| Flag | Type | Default | Purpose |
|---|---|---|---|
| `--server` | string | `http://localhost:7654` | Informational only |
| `--dry-run` | bool | false | Preview only |
| `--block` | bool | false | Hook failure propagation (default resilient) |

Writes hooks for: Claude Code (~/.claude/settings.json), Codex (~/.codex/config.toml + hooks.json), OpenCode (~/.config/opencode/plugins/termyard.js), Pi (~/.pi/agent/extensions/termyard.ts).

### session (internal subcommands)

`session create`, `session list`, `session kill`, `session capture` — low-level session lifecycle (used internally by daemon; not primary UX).

**Routing (`create` and `list`):**
- `create`: Attempts server API (`POST /api/session/create`) first. If the server is confirmed absent (no socket, connection refused, flock held by boot lock), acquires flock and spawns daemon directly (fallback). A fallback-spawned session has no watcher and appears at next server boot with adoption. HTTP errors from a reachable server (5xx, timeout) are treated as user errors (no fallback).
- `list`: Attempts `GET /api/sessions` first, falls back to a read-only socket-directory scan when server is down.

**Contract:** Both paths JSON-output compatible. Lock coordination prevents direct-spawn/boot-adoption race.

---

## 17. Backend API

Complete route surface below. Adding/removing routes breaks external integrations.

### Public routes (no session auth)

| Path | Method | Purpose | Notes |
|---|---|---|---|
| `/api/auth/status` | GET | Check auth requirement | `{auth_required, needs_setup, authenticated}` |
| `/api/version` | GET | Server version | `{version, commit}` |
| `/api/auth/setup` | POST | Initial password | Rate-limited setup 5/min |
| `/api/auth/login` | POST | Password login | Rate-limited login 10/min |
| `/api/auth/logout` | POST | Logout | — |
| `/api/auth/check` | GET | Validate session | 200 if valid |
| `/api/peers/bootstrap` | POST | Pair with peer | Rate-limited bootstrap 5/min |
| `/api/tool-event` | POST | Send tool event | Bearer token when auth enabled |

### Protected routes (session auth when enabled)

| Path | Method | Request | Response | Notes |
|---|---|---|---|---|
| `/api/sessions` | GET | — | `[]Session` | Tracker-enriched |
| `/api/hosts` | GET | — | `[]Host` | Or `[]` if none |
| `/api/session/create` | POST | `{name, host?, path, command, agent_type?, worktree_branch?}` | `{name}` | Fallback name `shell-<ms>` |
| `/api/session/display-name` | POST | `{session, display_name?, clear?, host?}` | 204 | Friendly label (never renames tmux) |
| `/api/session/regenerate-name` | POST | `{session, host?}` | `{name}` | 12s timeout |
| `/api/session/kill` | POST | `{id, name, host?, remove_worktree?}` | 204 | Worktree removal non-fatal |
| `/api/session/select-window` | POST | `{session, window, host?, pane?}` | 204 | Switch active window |
| `/api/tool-events` | GET | query `session?` | `[]Event` | Auto-detected agents merged |
| `/api/active-turns` | GET | — | `[{host?, session}]` | Currently active turns |
| `/api/tool-event` (bulk) | DELETE | — | 204 | `ClearAll()` |
| `/api/tool-event` (single) | DELETE | `{host?, session, window?, pane?}` | 204 | Single event clear |
| `/api/artifacts` | GET | query `session`, `host?` | `{artifacts:[...]}` | Deleted files evicted; cleared on session kill; >7d dropped at load |
| `/api/activity` | GET | query `session?` | `[]Activity` | Peer-merged |
| `/api/stats` | GET | — | System stats | CPU, Memory, load, uptime |
| `/api/pane-capture` | GET | query `session`, `lines?` (default 40), `host?` | `{text}` | Last N lines; 504 3s timeout |
| `/api/session-attrs` | GET | — | `{Sets}` | Background/hidden map |
| `/api/session-attrs` | POST | `{key, background?, hidden?}` | `{Sets}` | Broadcasts + peer fanout |
| `/api/session-order` | GET | — | `{Ranks}` | Fractional rank map |
| `/api/session-order` | POST | `{key, rank}` | `{Ranks}` | Broadcasts + peer fanout |
| `/api/groups` | GET | — | `{Live}` | Live group tree |
| `/api/groups` | POST | `{id, op, tree?, name?, rank?}` | `{Live}` | op ∈ tree/name/ai-name/rank/delete |
| `/api/schedules` | GET | — | `[]Job` | Scheduled jobs |
| `/api/schedules` | POST | `{Job}` | 201 + job | Create job |
| `/api/schedules/{id}` | PUT | `{Job}` | `{Job}` | Update job |
| `/api/schedules/{id}` | DELETE | — | 204 | Delete job |
| `/api/schedules/{id}/run` | POST | — | `{Job}` | Mark + run |
| `/api/peers` | GET | — | `{self, peers:[...]}` | Peer list |
| `/api/peers` | POST | `{address, password, auto_reconnect}` | 201 + snapshot | Add peer |
| `/api/peers/{fp}` | PATCH | `{enabled?:bool}` | `{Snapshot}` | Enable/disable peer |
| `/api/peers/{fp}` | DELETE | — | 204 | Forget peer |
| `/api/peers/{fp}/reconnect` | POST | — | 204 | Force reconnect |
| `/api/portforwards` | GET | — | `[]PortForward` | Active forwards |
| `/api/portforwards` | POST | `{port, label?, mode, external_port?}` | 201 + list | mode: proxy or socat |
| `/api/portforward/{port}` | DELETE | — | 204 | Remove forward |
| `/api/wiki/status` | GET | — | `{status}` | wiki-viewer-lite status |
| `/api/wiki/install` | POST | — | 202 | Start install |
| `/api/push/vapid-key` | GET | — | `{public_key}` | Web Push key |
| `/api/push/subscribe` | POST | `{Subscription}` | 204 | Subscribe to push |
| `/api/push/unsubscribe` | POST | `{endpoint}` | 204 | Unsubscribe from push |
| `/api/preferences` | GET | — | `{Preferences}` | All prefs (key masked) |
| `/api/preferences` | PUT | `{Preferences}` | `{Preferences}` | Update all prefs |
| `/api/agent-status` | GET | — | `{agentcheck}` | Agent install status |
| `/api/update` | GET | — | `{Status}` | Update availability |
| `/api/update/check` | POST | — | `{Status}` | Check for update |
| `/api/update/apply` | POST | — | `{ok, new_version, restarting?}` | Apply update |
| `/file/grant` | POST | query `path`, `session?`, `host?` | `{token, path, root?}` or `{path, is_dir: true}` for directories | Artifact download token |
| `/file/exists` | GET | query `path`, `session?`, `host?` | `{exists}` | Read-only path check (no token minted); used by terminal file-link highlighting |
| `/file` | GET | query `token` | 200 inline; 403 invalid token | Download/view artifact |
| `/api/upload` | POST | query `session`, `host?`, `filename` | `{path, quotedPath}` | File upload (30s deadline) |
| `/proxy/{port}/*` | ALL | — | Proxied response | HTTP reverse proxy; WS supported |

**API Breaking Change:** `/api/crashed-sessions` and related recover/dismiss endpoints have been removed. Crash recovery as a user-facing feature is deleted; a killed daemon is removed like any normal exit via `session-removed` event. This is an intentional external API break.

### WebSocket routes

| Path | Query | Payload | Notes |
|---|---|---|---|
| `/ws/events` | — | JSON envelope | Hub event stream (browser) |
| `/ws/session` | `host?`, `cols?`, `rows?` (default 120×40) | binary PTY / text commands | Terminal (local or remote relay) |
| `/ws/direct-session` | — | binary PTY | Direct PTY (no daemon) |
| `/ws/daemon-session` | `name`, `cols?`, `rows?`, `replay?` | binary PTY | Daemon connection; replay gating |
| `/ws/peer` | — | JSON framed protocol | Peer control link (peer auth) |
| `/ws/peer-stream` | — | binary or JSON | Peer data link (upload/terminal) |

---

## 18. WebSocket message types

### Browser hub (`/ws/events`)

Entire protocol: raw JSON `{type: ...}`. Server→browser only; browser frames read+discarded.

| type | Payload | Meaning | Trigger |
|---|---|---|---|
| `welcome` | `{type, version, commit}` | Server info | On connect |
| `tool-event` | `{type, tool, tool_name?, status, host, session, window, pane, message, timestamp, artifacts}` | Agent status change | Tool event hook or detector |
| `artifacts` | `{type, host, session, artifacts}` | File batch | Artifact-kind tool event |
| `activity` | `{type, snapshots:[...]}` | Activity snapshot | 5s ticker |
| `session-attrs-updated` | `{type, origin?, key?}` | Background/hidden updated | Remote attrs applied |
| `session-order-updated` | `{type, ...}` | Session rank changed | Remote order applied |
| `groups-updated` | `{type, ...}` | Group tree changed | Remote group applied / local update |
| `update-status` | `{type, current_version, latest_version, update_available, pending_restart, channel}` | Update status | Check completed or apply |
| `sessions-changed` | `{type, host?, host_name?}` | Session list changed | Peer session sync |
| `session-added` | `{type, host?, session:[...]}` | Session(s) created/adopted | Authoritative launch or adoption signal (exactly-once per instance) |
| `session-removed` | `{type, host?, session}` | Session removed | Authoritative removal signal (exactly-once per instance; daemon exit, kill, or PID death) |

**Note on `session-added` / `session-removed`:** These are exactly-once authority signals for instance lifecycle. Frontend uses them alongside authoritative reconnect snapshots for reconciliation. A `session-removed` is emitted once per removed instance (eager UI-initiated kill and later watch-EOF confirmation do not double-broadcast). `peer-connected` / `peer-disconnected` were confirmed dead on the browser path (hub only ever subscribed to state.Manager, never peer.Manager) and the dead `web/src/App.tsx` branch handling them has been deleted (see §23). They still exist and are sent on the peer-to-peer wire protocol between peers (`forwardPeerStateChanges`) — only the never-reachable browser-hub receive path was removed.

### Terminal WS (`/ws/session`)

Bidirectional; browser→server (ping, PTY bytes, resize, paste-image, paste-file) and server→browser (pong, replay-start, replay-end, PTY output, close code 4000).

| type | Direction | Meaning | Trigger |
|---|---|---|---|
| `ping` | browser→server | Heartbeat | 10s timer |
| `pong` | server→browser | Heartbeat reply | Ping received |
| `resize` | browser→server | `{type, cols, rows}` | Viewport change |
| `replay-start` | server→browser | Delimit replay | Daemon ring buffer start |
| `replay-end` | server→browser | Delimit live | Replay done / live start |
| `paste-image` | browser→server | Base64 image | Image paste |
| `paste-file` | browser→server | Base64 file | File paste |

### Peer protocol (`/ws/peer`)

Peer→hub: `auth`, `state-update`, `state-event`, `tool-event`, `activity-update` (5s), `stats` (30s), `capture-pane-result`.

Hub→peer: `challenge`, `auth-ok`/`auth-fail`, `peer-state`, `session-action`, `forget`, `session-attrs-snapshot`/`delta`, `session-order-snapshot`/`delta`, `group-snapshot`/`delta`, `capture-pane`, `open-terminal`.

**Note:** `MsgRequestState`, `MsgPeerConnected`, `MsgPeerDisconnected` were dead protocol surface — handled in code but never sent anywhere in the repo (leftovers from a superseded asymmetric hub/listener protocol design, per `docs/plans/archive/symmetric-peering.md`). Deleted; `request-state` removed from the Hub→peer list above accordingly.

### Upload data (`/ws/peer-stream`, upload role)

Hub→peer: `upload-eof` (text "EOF") or `upload-abort` (text "abort").
Peer→hub: result frame with no type field: `{path, quotedPath}` on success, `{error}` on failure.

---

## 19. Constants with user-visible effect

### Connection / timing

- **Session removal SLA:** ~1s bounded best-effort under normal operating load (kernel EOF delivery immediate; callback scheduling may exceed 1s only under CPU starvation). Removal triggered by `session-removed` broadcast or absence from authoritative reconnect snapshot.
- **Session unreachable backoff:** Watch reconnect retry with exponential backoff: 250ms → 5s (cap 5s, indefinite); attempt initial dial ≤2s. Cleared on successful reconnect.
- **Boot adoption:** One-time socket directory scan at startup before `Ready()` closes, then no scanning again for session membership during the lifetime of the process. Flock held during adoption to prevent CLI direct-spawn race.
- Heartbeat (peer): session loop ping every 15s (write 5s deadline, read 30s).
- Heartbeat (terminal): 10s browser→server ping, no-traffic ≥25s → timeout/reconnect.
- Activity broadcast ticker: 5s.
- Replay fallback: 250ms max wait for replay-start; exceeded → force live.
- On `replay-start` the frontend fully resets xterm (buffer + scrollback) before the daemon ring snapshot is written, so the replay REPLACES prior content. Without the reset, every reconnect appended a duplicate copy of the ring: scrollback accumulated repeats and a frozen viewport was pushed up into old history ("pane jumped to top after returning from a hidden/idle tab").
- Peer reconnect backoff: 1s → 30s (double per failure, cap 30s, reset after up >30s), ±25% jitter.
- Offline peer prune: 5 minutes before sessions hidden.
- Inactivity waiting promotion: 30s no hook activity → "waiting" status (low precision fallback).

### Terminal behavior

- Mobile key bar: HOLD_DELAY_MS = 260ms (tap-vs-hold threshold), HOLD_REPEAT_MS = 80ms (repeat interval).
- Swipe gesture dead zone: 18px threshold before direction fires.
- Artifact panel: max 100 artifacts merged (newest first, oldest dropped if exceed).
- Upload auto-dismiss: 4s on success.
- Glance popover: 400ms enter delay (hover before show).
- DEC 2026 sync buffer: 32 MiB cap before overflow flush + passthrough.

### Rate limiting

- Setup 5/min per-IP; login 10/min per-IP; bootstrap 5/min per-IP.
- Exceeds → 429 + Retry-After header.

### Sizes / limits

- Max artifact preview text: 2000 chars OR 40 lines (whichever first).
- Max artifact preview in-panel: 100 artifacts (newest first); overflow dropped.
- Max artifact download: 10 MiB per file (peer cap).
- Max PTY control frame: ~14.7 MiB (WS 14 MiB read limit).
- PTY ring buffer: 32 MiB default (replay window; sized to comfortably cover the 50k-line FE scrollback pref, matches web MAX_REPLAY_BUFFER_BYTES).
- File upload max: no client-side limit (server validates).
- Per-terminal data frame size: 64 KiB coalesced output.

### Peer / mesh

- Per-stream setup timeout: 20 seconds.
- Upload frame timeout: 60 seconds per frame (write reply 5s).
- WS buffer per-peer: 1024×32 bytes.
- hi-queue 1024, lo-queue 128 (priority lanes).
- Dial timeout (peer connection): 45s handshake, auth challenge read 10s.
- Push subscription TTL: 30 seconds (delivery window).

---

## 20. Keyboard Shortcuts (see section 11 for complete table)

All keyboard shortcuts are documented in section 11 and are user-facing contracts. README has stale references (noted in-doc).

---

## 21. Theming

**Contract:** 31 CSS custom properties (oklch format) + 21 terminal ANSI colors define theme. CSS vars: core (background, foreground, borders, primary/secondary/accent/destructive colors + their foreground pairs), chart colors (chart-primary, chart-secondary), sidebar colors (8 vars: sidebar, sidebar-foreground, sidebar-primary, etc.). Terminal colors: ANSI 0–15 (black/red/green/yellow/blue/magenta/cyan/white + bright variants), cursor, selection background (rgba). Tool brand colors constant across themes: Claude `#c4a0ff`, Codex `#66e088`, Copilot `#66b3ff`, OpenCode `#bc8cff`. Presets: Default, Dark, Light — 3 built-in presets (Default preset keeps internal key `raycast` for persisted preference compatibility). **Custom color theme:** a 4th picker option, "Custom", lets users define their own palette instead of picking a preset. Editable primitives: background, foreground (text), muted (secondary text), accent/primary, success, warning, destructive, plus the full terminal palette (16 ANSI colors, cursor, selection background). The custom preset is built at apply-time (`buildCustomThemePreset` in `web/src/theme.ts`) from this persisted palette layered onto hardcoded fallback values for any unset field — it is not a static `themePresets` entry. Persisted via preferences as `custom_theme` (nil/absent until first edited). **Typography is independently customizable**: the terminal font family (see 9.12) supports free-text custom fonts alongside the 9 presets, additive to and separate from the color presets/custom theme above — theming (color) and typography (font) are orthogonal preferences.

**Why it matters:** Theme contract is the CSS/terminal color API; changing it breaks custom themes.

**Verification pointer:** `web/src/theme.ts`

---

## 22. Non-goals / explicitly out of scope

- No built-in TLS (plain HTTP/WS; use Tailscale/WireGuard or reverse proxy; `--tls` self-signed mode exists for secure-context features only).
- No transitive peering (no hub; B must pair C directly; no A-B-C routing).
- No hub/peer role distinction (symmetric P2P only).
- Rename never changes real tmux session name (display label only; attachment and hooks unchanged).
- Wiki viewer cannot read remote-host session files directly (artifact-token fallback to new-tab viewer).
- No offline overlay mesh ("If neither machine can reach the other directly, no overlay network will fix that.").
- Schedule definitions NOT synced peer-to-peer (comment in code: "cannot distinguish deleted from defined-elsewhere").
- `termyard agent-setup` default non-blocking (hooks never block); `--block` for debugging.
- OpenCode hooks always resilient regardless of `--block` flag.
- Agent "error" events: Claude Code and Codex have none (native); OpenCode/Copilot native.
- pprof disabled by default; requires loopback + auth when enabled.
- Push subscriptions in-memory (lost on server restart; browser re-subscribes on page load).

---

## 23. Known gaps

All previously flagged gaps are now closed. Final trace confirmed both remaining items:

1. **Frontend host-param wiring** — confirmed matching. `web/src/lib/terminalPool.ts` builds `/ws/session` and `/ws/daemon-session` URLs with `&host=<hostId>` when connecting to a remote peer's session, empty when local. `web/src/hooks/useTerminal.ts` threads `hostId` through via `poolKey`/`identity`. Backend `pkg/server/routes_sessions.go` (`daemonWS`) dispatches on the same `host` query param: empty → local, non-empty and non-local → remote per-stream relay. `/ws/direct-session` never carries `host` (always local PTY), matching the frontend which never sends one for `direct-pty:` sessions. Contract holds end to end.

2. **`peer-connected`/`peer-disconnected` browser-hub path — was confirmed DEAD, now removed.** The browser hub (`/ws/events`) only ever bridged `state.Manager` and `toolevents.Tracker` events (`pkg/ws/hub.go`); it never subscribed to `peer.Manager`, which was the sole producer of `peer-connected`/`peer-disconnected`. Those events only ever reached `peer.Manager`'s own subscriber (`forwardPeerStateChanges`), which forwards them over the peer-to-peer wire protocol to a *remote* peer — never to the local browser. The dead `web/src/App.tsx` branch handling these two message types has been deleted. Peer join/leave still visibly updates the UI through the path that was always doing the real work: remote sessions merging in trigger ordinary `session-added`/`sessions-changed` events, which the hub does broadcast. `forwardPeerStateChanges` and the peer-to-peer wire forwarding it does are untouched and still live.

No unverified areas remain in this document as of this pass.

---
