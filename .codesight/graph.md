# Dependency Graph

## Most Imported Files (change these carefully)

- `path/filepath` — imported by **19** files
- `encoding/json` — imported by **15** files
- `web/src/hooks/usePreferences.ts` — imported by **10** files
- `crypto/rand` — imported by **9** files
- `web/src/theme.ts` — imported by **9** files
- `web/src/lib/utils.ts` — imported by **9** files
- `os/exec` — imported by **8** files
- `net/http` — imported by **8** files
- `crypto/x509` — imported by **8** files
- `crypto/tls` — imported by **7** files
- `encoding/pem` — imported by **7** files
- `web/src/hooks/useSessions.ts` — imported by **7** files
- `encoding/hex` — imported by **6** files
- `web/src/hooks/useToolEvents.ts` — imported by **6** files
- `encoding/base64` — imported by **5** files
- `net/url` — imported by **4** files
- `web/src/hooks/useHosts.ts` — imported by **4** files
- `crypto/sha256` — imported by **3** files
- `crypto/ecdsa` — imported by **3** files
- `crypto/elliptic` — imported by **3** files

## Import Map (who imports what)

- `path/filepath` ← `pkg/agentcheck/agentcheck.go`, `pkg/auth/auth.go`, `pkg/commands/agent-setup/agent_setup.go`, `pkg/commands/install/install.go`, `pkg/git/worktree.go` +14 more
- `encoding/json` ← `pkg/auth/auth.go`, `pkg/commands/agent-setup/agent_setup.go`, `pkg/commands/notify/notify.go`, `pkg/commands/pair/pair.go`, `pkg/identity/identity.go` +10 more
- `web/src/hooks/usePreferences.ts` ← `web/src/App.tsx`, `web/src/components/HelpModal.tsx`, `web/src/components/NewSessionModal.tsx`, `web/src/components/Overview.tsx`, `web/src/components/Settings.tsx` +5 more
- `crypto/rand` ← `pkg/auth/auth.go`, `pkg/identity/identity.go`, `pkg/identity/pairing.go`, `pkg/peer/client_cert_test.go`, `pkg/peer/handler.go` +4 more
- `web/src/theme.ts` ← `web/src/App.tsx`, `web/src/components/AgentMark.tsx`, `web/src/components/Overview.tsx`, `web/src/components/QuickSwitcher.tsx`, `web/src/components/Settings.tsx` +4 more
- `web/src/lib/utils.ts` ← `web/src/components/AgentMark.tsx`, `web/src/components/NewSessionModal.tsx`, `web/src/components/QuickSwitcher.tsx`, `web/src/components/Settings.tsx`, `web/src/components/Setup.tsx` +4 more
- `os/exec` ← `pkg/agentcheck/agentcheck.go`, `pkg/commands/agent-setup/agent_setup.go`, `pkg/commands/install/install.go`, `pkg/commands/notify/notify.go`, `pkg/git/worktree.go` +3 more
- `net/http` ← `pkg/auth/auth.go`, `pkg/commands/notify/notify.go`, `pkg/commands/pair/pair.go`, `pkg/peer/handler.go`, `pkg/peer/pty_relay.go` +3 more
- `crypto/x509` ← `pkg/commands/pair/pair.go`, `pkg/identity/peers.go`, `pkg/peer/client.go`, `pkg/peer/client_cert_test.go`, `pkg/tlscert/reloader.go` +3 more
- `crypto/tls` ← `pkg/commands/pair/pair.go`, `pkg/identity/peers.go`, `pkg/peer/client.go`, `pkg/peer/client_cert_test.go`, `pkg/server/server.go` +2 more
