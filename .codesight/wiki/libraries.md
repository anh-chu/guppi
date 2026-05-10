# Libraries

> **Navigation aid.** Library inventory extracted via AST. Read the source files listed here before modifying exported functions.

**55 library files** across 2 modules

## Pkg (43 files)

- `pkg/peer/protocol.go` — NewMessage, Message, AuthPayload, ChallengePayload, StateUpdatePayload, StateEventPayload, …
- `pkg/auth/auth.go` — NewPasswordStore, NewSessionManager, Middleware, SetupHandler, LoginHandler, LogoutHandler, …
- `pkg/preferences/preferences.go` — Default, NewStore, Terminal, Sidebar, Notifications, AgentBanner, …
- `pkg/tmux/controlmode.go` — ControlSessionName, WithRefreshDelay, WithOnConnect, WithOnDisconnect, WithOnOutput, NewControlMode, …
- `pkg/tlscert/tlscert.go` — ParseSANs, LoadOrGenerateCA, LoadCACertPEM, LoadOrGenerate, LoadTLSConfig, LoadTLSConfigWithReloader, …
- `pkg/peer/pty_relay.go` — NewPTYRelay, GenerateStreamID, Bridge, PTYRelay, PendingStream, ActiveBridge
- `pkg/tmux/sessionmeta.go` — NormalizeAgentType, IsShellCommand, PrimaryPane, InferAgentType, ResolveProjectPath, ExtractPromptPreview
- `pkg/identity/identity.go` — Generate, Verify, LoadOrCreate, Load, Identity
- `pkg/toolevents/tracker.go` — NewTracker, Event, PaneKey, SessionMeta, Tracker
- `pkg/activity/tracker.go` — NewTracker, SessionActivity, Snapshot, Tracker
- `pkg/peer/manager.go` — NewManager, HostState, PeerConnection, Manager
- `pkg/state/manager.go` — NewManager, SessionMetadata, Manager, StateEvent
- `pkg/tmux/types.go` — Session, Window, PaneDetailed, Pane
- `pkg/toolevents/reconciler.go` — NewReconciler, PaneState, PaneInfo, Reconciler
- `pkg/ws/hub.go` — CheckSameOrigin, NewHub, Hub, ActivitySource
- `pkg/agentcheck/agentcheck.go` — CheckAgents, AgentStatus, StatusResult
- `pkg/identity/pairing.go` — NewPairingManager, PairingCode, PairingManager
- `pkg/identity/peers.go` — NewPeerStore, Peer, PeerStore
- `pkg/peer/pty_manager.go` — NewPTYManager, PTYManager, ActivePTY
- `pkg/socket/socket.go` — DefaultPath, EnsureDir, Cleanup
- `pkg/stats/stats.go` — SystemStats, ProcessCountsFromSessions, ProcessEntry
- `pkg/tmux/paste_image.go` — HandlePTYControlMessage, StorePastedImage, PTYControlMessage
- `pkg/toolevents/silence.go` — NewSilenceMonitor, SilenceMonitor, TmuxClient
- `pkg/webpush/sender.go` — NewSender, PushPayload, Sender
- `pkg/common/commands.go` — RegisterCommand, GetCommands
- _…and 18 more files_

## Web (12 files)

- `web/src/hooks/useSessions.ts` — sessionKey, parseSessionKey, useSessions, Pane, Window, Session
- `web/src/theme.ts` — applyTheme, getXtermTheme, ThemePreset, toolColors, statusConfig, themePresets
- `web/src/hooks/usePreferences.ts` — usePreferencesProvider, usePreferences, Preferences, defaultPreferences, PreferencesContext
- `web/src/hooks/useActivity.ts` — useActivity, ActivitySnapshot
- `web/src/hooks/useHosts.ts` — useHosts, Host
- `web/src/hooks/useToolEvents.ts` — useToolEvents, ToolEvent
- `web/src/hooks/useAuth.ts` — useAuth
- `web/src/hooks/useNotifications.ts` — useNotifications
- `web/src/hooks/usePushNotifications.ts` — usePushNotifications
- `web/src/hooks/useTerminal.ts` — useTerminal
- `web/src/hooks/useWebSocket.ts` — useWebSocket
- `web/src/lib/utils.ts` — cn

---
_Back to [overview.md](./overview.md)_