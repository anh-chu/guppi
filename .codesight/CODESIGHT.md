# guppi — AI Context Map

> **Stack:** chi | none | react | go

> 85 routes | 0 models | 15 components | 58 lib files | 7 env vars | 1 middleware | 7% test coverage
> **Token savings:** this file is ~5,700 tokens. Without it, AI exploration would cost ~74,400 tokens. **Saves ~68,800 tokens per conversation.**
> **Last scanned:** 2026-06-01 06:14 — re-run after significant changes

---

# Routes

## CRUD Resources

- **`/api/peers`** GET | POST | GET/:id | PATCH/:id | DELETE/:id → Peer
- **`/peers`** GET | POST | GET/:id | PATCH/:id | DELETE/:id → Peer

## Other Routes

- `POST` `http://localhost/api/tool-event` params() [auth, ai]
- `GET` `stream` params() [auth, db]
- `GET` `/api/auth/status` params() [auth, db, queue, ai]
- `POST` `/api/auth/setup` params() [auth, db, queue, ai]
- `POST` `/api/auth/login` params() [auth, db, queue, ai]
- `POST` `/api/auth/logout` params() [auth, db, queue, ai]
- `GET` `/api/auth/check` params() [auth, db, queue, ai]
- `POST` `/api/peers/bootstrap` params() [auth, db, queue, ai] ✓
- `GET` `/api/version` params() [auth, db, queue, ai]
- `POST` `/api/tool-event` params() [auth, db, queue, ai]
- `GET` `/api/agent-status` params() [auth, db, queue, ai]
- `GET` `/api/sessions` params() [auth, db, queue, ai]
- `GET` `/api/hosts` params() [auth, db, queue, ai]
- `POST` `/api/session/new` params() [auth, db, queue, ai]
- `POST` `/api/session/rename` params() [auth, db, queue, ai]
- `POST` `/api/session/select-window` params() [auth, db, queue, ai]
- `POST` `/api/session/kill` params() [auth, db, queue, ai]
- `GET` `/api/tool-events` params() [auth, db, queue, ai]
- `GET` `/api/session` params() [auth, db, queue, ai]
- `DELETE` `/api/tool-events` params() [auth, db, queue, ai]
- `DELETE` `/api/tool-event` params() [auth, db, queue, ai]
- `GET` `/api/stats` params() [auth, db, queue, ai]
- `GET` `/api/activity` params() [auth, db, queue, ai]
- `GET` `/api/push/vapid-key` params() [auth, db, queue, ai]
- `POST` `/api/push/subscribe` params() [auth, db, queue, ai]
- `POST` `/api/push/unsubscribe` params() [auth, db, queue, ai]
- `GET` `/api/preferences` params() [auth, db, queue, ai]
- `PUT` `/api/preferences` params() [auth, db, queue, ai]
- `GET` `/api/portforwards` params() [auth, db, queue, ai]
- `POST` `/api/portforwards` params() [auth, db, queue, ai]
- `DELETE` `/api/portforward/{port}` params(port) [auth, db, queue, ai]
- `POST` `/api/peers/{fp}/reconnect` params(fp) [auth, db, queue, ai]
- `GET` `name` params() [auth, db, queue, ai] ✓
- `GET` `cols` params() [auth, db, queue, ai]
- `GET` `rows` params() [auth, db, queue, ai]
- `GET` `Upgrade` params() [auth, db, queue, ai]
- `GET` `Content-Type` params() [auth, db, queue, ai]
- `GET` `Content-Encoding` params() [auth, db, queue, ai]
- `GET` `/auth/status` params() [auth, db, queue, ai]
- `POST` `/auth/setup` params() [auth, db, queue, ai]
- `POST` `/auth/login` params() [auth, db, queue, ai]
- `POST` `/auth/logout` params() [auth, db, queue, ai]
- `GET` `/auth/check` params() [auth, db, queue, ai]
- `POST` `/peers/bootstrap` params() [auth, db, queue, ai]
- `GET` `/version` params() [auth, db, queue, ai]
- `POST` `/tool-event` params() [auth, db, queue, ai]
- `GET` `/agent-status` params() [auth, db, queue, ai]
- `GET` `/sessions` params() [auth, db, queue, ai]
- `GET` `/hosts` params() [auth, db, queue, ai]
- `POST` `/session/new` params() [auth, db, queue, ai]
- `POST` `/session/rename` params() [auth, db, queue, ai]
- `POST` `/session/select-window` params() [auth, db, queue, ai]
- `POST` `/session/kill` params() [auth, db, queue, ai]
- `GET` `/tool-events` params() [auth, db, queue, ai]
- `GET` `session` params() [auth, db, queue, ai]
- `DELETE` `/tool-events` params() [auth, db, queue, ai]
- `DELETE` `/tool-event` params() [auth, db, queue, ai]
- `GET` `/stats` params() [auth, db, queue, ai]
- `GET` `/activity` params() [auth, db, queue, ai]
- `GET` `/push/vapid-key` params() [auth, db, queue, ai]
- `POST` `/push/subscribe` params() [auth, db, queue, ai]
- `POST` `/push/unsubscribe` params() [auth, db, queue, ai]
- `GET` `/preferences` params() [auth, db, queue, ai]
- `PUT` `/preferences` params() [auth, db, queue, ai]
- `GET` `/portforwards` params() [auth, db, queue, ai]
- `POST` `/portforwards` params() [auth, db, queue, ai]
- `DELETE` `/portforward/{port}` params(port) [auth, db, queue, ai]
- `POST` `/peers/{fp}/reconnect` params(fp) [auth, db, queue, ai]
- `GET` `/ws/events` params() [auth, db, queue, ai]
- `GET` `/ws/session` params() [auth, db, queue, ai]
- `GET` `host` params() [auth, db, queue, ai]
- `GET` `/ws/peer` params() [auth, db, queue, ai]
- `GET` `/ws/peer-pty` params() [auth, db, queue, ai]
- `GET` `/proxy/{port}` params(port) [auth, db, queue, ai]
- `GET` `/proxy/{port}/*` params(port) [auth, db, queue, ai]
- `GET` `/*` params() [auth, db, queue, ai]
- `GET` `Origin` params() [auth]

---

# Components

- **App** — `web/src/App.tsx`
- **AgentMark** — props: agentType, className — `web/src/components/AgentMark.tsx`
- **ConnectPeerModal** — props: onClose, onConnected — `web/src/components/ConnectPeerModal.tsx`
- **HelpModal** — props: onClose — `web/src/components/HelpModal.tsx`
- **Login** — props: mode, error, onSubmit — `web/src/components/Login.tsx`
- **NewSessionModal** — props: hosts, sessions, onCreateSession, onClose — `web/src/components/NewSessionModal.tsx`
- **Overview** — props: sessions, hosts, onSessionSelect, getSessionEvents, getSessionActivity, pendingAlerts, onJumpToSession, onDismissAlert — `web/src/components/Overview.tsx`
- **PortForwardModal** — props: onClose — `web/src/components/PortForwardModal.tsx`
- **QuickSwitcher** — props: sessions, waitingEvents, onSelect, onOverview, onCreateSession, onClose — `web/src/components/QuickSwitcher.tsx`
- **Settings** — props: pushState, onPushSubscribe, onPushUnsubscribe, onLogout — `web/src/components/Settings.tsx`
- **AgentStatusList** — props: agents — `web/src/components/Setup.tsx`
- **StatusBar** — props: sessionCount, connected, activeSession, waitingCount, pushState, version, updateAvailable, hosts, agentCount, onHelp — `web/src/components/StatusBar.tsx`
- **Terminal** — props: sessionName, hostId, fullscreen, onToggleFullscreen — `web/src/components/Terminal.tsx`
- **TiledView** — props: tree, activeKey, onActivate, onClose, onKill, onPopOut, onSplit, onRatioChange, fullscreen, onToggleFullscreen — `web/src/components/TiledView.tsx`
- **TopBar** — props: currentView, sidebarCollapsed, onToggleCollapse, onOverview, onSettings, onNewSession, onPortForwards, events, connected, onJumpToSession — `web/src/components/TopBar.tsx`

---

# Libraries

- `pkg/activity/tracker.go`
  - function NewTracker: () *Tracker
  - class SessionActivity
  - class Snapshot
  - class Tracker
- `pkg/agentcheck/agentcheck.go`
  - function CheckAgents: () *StatusResult
  - class AgentStatus
  - class StatusResult
- `pkg/auth/auth.go`
  - function NewPasswordStore: () (*PasswordStore, error)
  - function NewSessionManager: (ttl time.Duration) *SessionManager
  - function Middleware: (sm *SessionManager) func(http.Handler) http.Handler
  - function SetupHandler: (ps *PasswordStore, sm *SessionManager, secureCookies bool) http.HandlerFunc
  - function LoginHandler: (ps *PasswordStore, sm *SessionManager, secureCookies bool) http.HandlerFunc
  - function LogoutHandler: (sm *SessionManager) http.HandlerFunc
  - _...4 more_
- `pkg/commands/agent-setup/agent_setup.go` — function Execute: (ctx context.Context, c *cli.Command) error
- `pkg/commands/notify/notify.go` — function Execute: (ctx context.Context, c *cli.Command) error
- `pkg/commands/server/server.go` — function Execute: (ctx context.Context, c *cli.Command) error
- `pkg/common/commands.go` — function RegisterCommand: (command *cli.Command), function GetCommands: () []*cli.Command
- `pkg/common/global.go` — function Flags: () []cli.Flag, function Before: (ctx context.Context, c *cli.Command) (context.Context, error)
- `pkg/common/version.go` — class AppVersionInfo
- `pkg/git/worktree.go`
  - function IsWorktree: (path string) (bool, error)
  - function FindMainWorktreeRoot: (path string) (string, error)
  - function RemoveWorktree: (path string) error
  - function CreateWorktree: (repoPath, branch, destPath string) error
- `pkg/identity/identity.go`
  - function Generate: (name string) (*Identity, error)
  - function Verify: (publicKeyB64 string, message, signature []byte) bool
  - function LoadOrCreate: (defaultName string) (*Identity, error)
  - function Load: () (*Identity, error)
  - class Identity
- `pkg/identity/peers.go`
  - function NewPeerStore: () (*PeerStore, error)
  - class Peer
  - class PeerStore
- `pkg/peer/bootstrap.go`
  - function NormalizeAddress: (addr string) (string, error)
  - function SendBootstrap: (ctx context.Context, addr string, req BootstrapRequest) (*BootstrapResponse, error)
  - class BootstrapRequest
  - class BootstrapResponse
  - class BootstrapError
- `pkg/peer/handler.go` — function NewHandler: (deps SessionDeps) *Handler, class Handler
- `pkg/peer/manager.go`
  - function NewPeerConnection: (hostID string, bufSize int) *PeerConnection
  - function NewManager: (id *identity.Identity, peerStore *identity.PeerStore, localMgr *state.Manager) *Manager
  - class HostState
  - class PeerConnection
  - class Manager
- `pkg/peer/protocol.go`
  - function NewMessage: (msgType string, payload interface{}) (*Message, error)
  - class Message
  - class AuthPayload
  - class ChallengePayload
  - class StateUpdatePayload
  - class StateEventPayload
  - _...10 more_
- `pkg/peer/pty_manager.go`
  - function NewPTYManager: (tmuxPath string, actTracker *activity.Tracker) *PTYManager
  - class PTYManager
  - class ActivePTY
- `pkg/peer/pty_relay.go`
  - function NewPTYRelay: () *PTYRelay
  - function GenerateStreamID: () string
  - function Bridge: (browserWS, peerWS *websocket.Conn, streamID string)
  - class PTYRelay
  - class PendingStream
  - class ActiveBridge
- `pkg/peer/session.go` — class SessionDeps
- `pkg/peer/supervisor.go`
  - function NewLinkSupervisor: (deps SessionDeps) *LinkSupervisor
  - class LinkSnapshot
  - class LinkSupervisor
- `pkg/portforward/store.go`
  - function NewStore: () *Store
  - class Forward
  - class Store
- `pkg/preferences/preferences.go`
  - function Default: () *Preferences
  - function NewStore: () (*Store, error)
  - class Terminal
  - class Sidebar
  - class Notifications
  - class AgentBanner
  - _...2 more_
- `pkg/server/server.go` — function Run: (ctx context.Context, opts *Options) error, class Options
- `pkg/socket/socket.go`
  - function DefaultPath: () string
  - function EnsureDir: (socketPath string) error
  - function Cleanup: (socketPath string) error
- `pkg/state/manager.go`
  - function NewManager: (client *tmux.Client) *Manager
  - class SessionMetadata
  - class Manager
  - class StateEvent
- `pkg/stats/stats.go`
  - function SystemStats: () map[string]interface
  - function ProcessCountsFromSessions: (sessions []*tmux.Session) []ProcessEntry
  - class ProcessEntry
- `pkg/tmux/client.go`
  - function NewClient: () (*Client, error)
  - function ValidateSessionName: (name string) error
  - class Client
- `pkg/tmux/controlmode.go`
  - function ControlSessionName: () string
  - function WithRefreshDelay: (d time.Duration) ControlModeOption
  - function WithOnConnect: (fn func() ) ControlModeOption
  - function WithOnDisconnect: (fn func() ) ControlModeOption
  - function WithOnOutput: (fn func(paneID string, dataLen int) ) ControlModeOption
  - function NewControlMode: (client *Client, onChange func([]*Session) , opts ...ControlModeOption) *ControlMode
  - _...2 more_
- `pkg/tmux/discovery.go` — function NewDiscovery: (client *Client, interval time.Duration, onChange func([]*Session) ) *Discovery, class Discovery
- `pkg/tmux/paste_image.go`
  - function HandlePTYControlMessage: (ptySess *PTYSession, raw []byte) error
  - function StorePastedImage: (data, mimeType, filename string) (string, error)
  - class PTYControlMessage
- `pkg/tmux/pty.go` — function NewPTYSession: (tmuxPath, sessionName string, cols, rows uint16) (*PTYSession, error), class PTYSession
- `pkg/tmux/sessionmeta.go`
  - function NormalizeAgentType: (command string) string
  - function IsShellCommand: (command string) bool
  - function PrimaryPane: (windows []*Window) *Pane
  - function InferAgentType: (windows []*Window, fallback string) string
  - function ResolveProjectPath: (windows []*Window, fallback string) string
  - function ExtractPromptPreview: (content string) string
- `pkg/tmux/types.go`
  - class Session
  - class Window
  - class PaneDetailed
  - class Pane
- `pkg/toolevents/detect.go` — function DetectAgentInProcessTree: (pid int) (Tool, bool)
- `pkg/toolevents/detector.go` — function NewDetector: (tracker *Tracker, listPane PaneListFunc, interval time.Duration) *Detector, class Detector
- `pkg/toolevents/promptparser.go` — function DetectPrompt: (content string) PromptResult, class PromptResult
- `pkg/toolevents/reconciler.go`
  - function NewReconciler: (tracker *Tracker, lookup PaneLookupFunc, interval time.Duration) *Reconciler
  - class PaneState
  - class PaneInfo
  - class Reconciler
- `pkg/toolevents/silence.go`
  - function NewSilenceMonitor: (tracker *Tracker, detector *Detector, client TmuxClient) *SilenceMonitor
  - class SilenceMonitor
  - interface TmuxClient
- `pkg/toolevents/tracker.go`
  - function NewTracker: () *Tracker
  - class Event
  - class PaneKey
  - class SessionMeta
  - class Tracker
- `pkg/webpush/sender.go`
  - function NewSender: (keys *VAPIDKeys, store *Store, tracker *toolevents.Tracker) *Sender
  - class PushPayload
  - class Sender
- `pkg/webpush/subscriptions.go` — function NewStore: () *Store, class Store
- `pkg/webpush/vapid.go` — function LoadOrCreateKeys: () (*VAPIDKeys, error), class VAPIDKeys
- `pkg/ws/hub.go`
  - function CheckSameOrigin: (r *http.Request) bool
  - function NewHub: (stateMgr *state.Manager, tracker *toolevents.Tracker) *Hub
  - class Hub
  - interface ActivitySource
- `pkg/ws/pty_terminal.go` — function NewPTYTerminalHandler: (tmuxPath string, activityTracker *activity.Tracker) *PTYTerminalHandler, class PTYTerminalHandler
- `web/src/hooks/useActivity.ts` — function useActivity: () => void, interface ActivitySnapshot
- `web/src/hooks/useAuth.ts` — function useAuth: () => AuthState
- `web/src/hooks/useHosts.ts` — function useHosts: () => void, interface Host
- `web/src/hooks/useNotifications.ts` — function useNotifications: (pushSubscribed) => void
- `web/src/hooks/usePortForwards.ts`
  - function usePortForwards: () => void
  - interface PortForward
  - type ForwardMode
- `web/src/hooks/usePreferences.ts`
  - function usePreferencesProvider: () => void
  - function usePreferences: () => void
  - interface Preferences
  - const defaultPreferences: Preferences
  - const PreferencesContext
- `web/src/hooks/usePushNotifications.ts` — function usePushNotifications: () => void
- `web/src/hooks/useSessions.ts`
  - function sessionKey: (session) => string
  - function parseSessionKey: (key) => void
  - function useSessions: () => void
  - interface Pane
  - interface Window
  - interface Session
- `web/src/hooks/useTerminal.ts` — function useTerminal: (sessionName, hostId?) => void
- `web/src/hooks/useToolEvents.ts` — function useToolEvents: () => void, interface ToolEvent
- `web/src/hooks/useWebSocket.ts` — function useWebSocket: (path, onMessage) => void
- `web/src/lib/paneTree.ts`
  - function getLeaves: (tree) => string[]
  - function findLeaf: (tree, key) => boolean
  - function splitLeaf: (tree, targetKey, direction, newKey) => PaneTree
  - function removeLeaf: (tree, key) => PaneTree | null
  - function replaceLeaf: (tree, oldKey, newKey) => PaneTree
  - function updateRatio: (tree, path, ratio) => PaneTree
  - _...7 more_
- `web/src/lib/utils.ts` — function cn: (...inputs) => void
- `web/src/theme.ts`
  - function applyTheme: (themeName, customTheme?, string>) => void
  - function getXtermTheme: (themeName) => void
  - interface ThemePreset
  - const toolColors: Record<string, string>
  - const statusConfig: Record<string, { color: string; label: string; icon?: string; bg?: string }>
  - const themePresets: Record<string, ThemePreset>

---

# Config

## Environment Variables

- `GUPPI_BIN` **required** — pkg/commands/agent-setup/pi-extension/guppi.ts
- `PATH` **required** — pkg/commands/install/install.go
- `SHELL` **required** — pkg/tmux/client.go
- `TMPDIR` **required** — pkg/socket/socket.go
- `TMUX_PANE` **required** — pkg/commands/notify/notify.go
- `XDG_DATA_HOME` **required** — pkg/webpush/vapid.go
- `XDG_RUNTIME_DIR` **required** — pkg/socket/socket.go

## Config Files

- `go.mod`
- `web/vite.config.ts`

---

# Middleware

## auth
- auth — `pkg/auth/auth.go`

---

# Dependency Graph

## Most Imported Files (change these carefully)

- `encoding/json` — imported by **20** files
- `path/filepath` — imported by **19** files
- `net/http` — imported by **11** files
- `web/src/lib/utils.ts` — imported by **10** files
- `os/exec` — imported by **9** files
- `web/src/hooks/usePreferences.ts` — imported by **9** files
- `web/src/theme.ts` — imported by **9** files
- `web/src/hooks/useSessions.ts` — imported by **7** files
- `web/src/hooks/useToolEvents.ts` — imported by **6** files
- `crypto/rand` — imported by **5** files
- `encoding/base64` — imported by **5** files
- `net/url` — imported by **5** files
- `web/src/hooks/useHosts.ts` — imported by **4** files
- `encoding/hex` — imported by **3** files
- `web/src/hooks/useActivity.ts` — imported by **3** files
- `web/src/hooks/usePushNotifications.ts` — imported by **3** files
- `net/http/httptest` — imported by **2** files
- `web/src/components/Terminal.tsx` — imported by **2** files
- `web/src/lib/paneTree.ts` — imported by **2** files
- `web/src/components/Setup.tsx` — imported by **2** files

## Import Map (who imports what)

- `encoding/json` ← `pkg/auth/auth.go`, `pkg/commands/agent-setup/agent_setup.go`, `pkg/commands/notify/notify.go`, `pkg/identity/identity.go`, `pkg/identity/peers.go` +15 more
- `path/filepath` ← `pkg/agentcheck/agentcheck.go`, `pkg/auth/auth.go`, `pkg/commands/agent-setup/agent_setup.go`, `pkg/commands/install/install.go`, `pkg/git/worktree.go` +14 more
- `net/http` ← `pkg/auth/auth.go`, `pkg/commands/notify/notify.go`, `pkg/peer/bootstrap.go`, `pkg/peer/bootstrap_test.go`, `pkg/peer/handler.go` +6 more
- `web/src/lib/utils.ts` ← `web/src/components/AgentMark.tsx`, `web/src/components/NewSessionModal.tsx`, `web/src/components/PortForwardModal.tsx`, `web/src/components/QuickSwitcher.tsx`, `web/src/components/Settings.tsx` +5 more
- `os/exec` ← `pkg/agentcheck/agentcheck.go`, `pkg/commands/agent-setup/agent_setup.go`, `pkg/commands/install/install.go`, `pkg/commands/notify/notify.go`, `pkg/git/worktree.go` +4 more
- `web/src/hooks/usePreferences.ts` ← `web/src/App.tsx`, `web/src/components/NewSessionModal.tsx`, `web/src/components/Overview.tsx`, `web/src/components/Settings.tsx`, `web/src/components/Setup.tsx` +4 more
- `web/src/theme.ts` ← `web/src/App.tsx`, `web/src/components/AgentMark.tsx`, `web/src/components/Overview.tsx`, `web/src/components/QuickSwitcher.tsx`, `web/src/components/Settings.tsx` +4 more
- `web/src/hooks/useSessions.ts` ← `web/src/App.tsx`, `web/src/components/NewSessionModal.tsx`, `web/src/components/Overview.tsx`, `web/src/components/QuickSwitcher.tsx`, `web/src/components/Sidebar.tsx` +2 more
- `web/src/hooks/useToolEvents.ts` ← `web/src/App.tsx`, `web/src/components/Overview.tsx`, `web/src/components/QuickSwitcher.tsx`, `web/src/components/Sidebar.tsx`, `web/src/components/TopBar.tsx` +1 more
- `crypto/rand` ← `pkg/auth/auth.go`, `pkg/identity/identity.go`, `pkg/peer/handler.go`, `pkg/peer/pty_relay.go`, `pkg/tmux/paste_image.go`

---

# Test Coverage

> **7%** of routes and models are covered by tests
> 10 test files found

## Covered Routes

- DELETE:/api/peers/{fp}
- POST:/api/peers/bootstrap
- GET:/api/peers
- POST:/api/peers
- PATCH:/api/peers/{fp}
- GET:name

---

_Generated by [codesight](https://github.com/Houseofmvps/codesight) — see your codebase clearly_