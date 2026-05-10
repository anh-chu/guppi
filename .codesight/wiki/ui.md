# UI

> **Navigation aid.** Component inventory and prop signatures extracted via AST. Read the source files before adding props or modifying component logic.

**14 components** (react)

## Components

- **App** — `web/src/App.tsx`
- **AgentMark** — props: agentType, className — `web/src/components/AgentMark.tsx`
- **HelpModal** — props: onClose — `web/src/components/HelpModal.tsx`
- **Login** — props: mode, error, onSubmit, onTrustCert — `web/src/components/Login.tsx`
- **NewSessionModal** — props: hosts, sessions, onCreateSession, onClose — `web/src/components/NewSessionModal.tsx`
- **Overview** — props: sessions, hosts, onSessionSelect, getSessionEvents, getSessionActivity, pendingAlerts, onJumpToSession, onDismissAlert — `web/src/components/Overview.tsx`
- **QuickSwitcher** — props: sessions, waitingEvents, onSelect, onOverview, onCreateSession, onClose — `web/src/components/QuickSwitcher.tsx`
- **Settings** — props: pushState, onPushSubscribe, onPushUnsubscribe, onLogout — `web/src/components/Settings.tsx`
- **AgentStatusList** — props: agents — `web/src/components/Setup.tsx`
- **StatusBar** — props: sessionCount, connected, activeSession, waitingCount, pushState, version, updateAvailable, hosts, agentCount, onHelp — `web/src/components/StatusBar.tsx`
- **Terminal** — props: sessionName, hostId, fullscreen, onToggleFullscreen — `web/src/components/Terminal.tsx`
- **TiledView** — props: tree, activeKey, onActivate, onClose, onPopOut, onSplit, onRatioChange, fullscreen, onToggleFullscreen, terminalContainerRef — `web/src/components/TiledView.tsx`
- **TopBar** — props: currentView, sidebarCollapsed, onToggleCollapse, onOverview, onSettings, onNewSession, events, connected, onJumpToSession, onDismiss — `web/src/components/TopBar.tsx`
- **TrustCertificate** — props: onBack — `web/src/components/TrustCertificate.tsx`

---
_Back to [overview.md](./overview.md)_