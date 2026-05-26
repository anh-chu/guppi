# guppi — Wiki

_Generated 2026-05-26 — re-run `npx codesight --wiki` if the codebase has changed._

Structural map compiled from source code via AST. No LLM — deterministic, 200ms.

> **How to use safely:** These articles tell you WHERE things live and WHAT exists. They do not show full implementation logic. Always read the actual source files before implementing new features or making changes. Never infer how a function works from the wiki alone.

## Articles

- [Overview](./overview.md)
- [Auth](./auth.md)
- [Payments](./payments.md)
- [Section](./section.md)
- [Content-encoding](./content-encoding.md)
- [Content-type](./content-type.md)
- [Upgrade](./upgrade.md)
- [Activity](./activity.md)
- [Agent-status](./agent-status.md)
- [Cols](./cols.md)
- [Host](./host.md)
- [Hosts](./hosts.md)
- [Hub](./hub.md)
- [Name](./name.md)
- [Notify](./notify.md)
- [Pair](./pair.md)
- [Portforward](./portforward.md)
- [Portforwards](./portforwards.md)
- [Preferences](./preferences.md)
- [Proxy](./proxy.md)
- [Pty_relay](./pty_relay.md)
- [Push](./push.md)
- [Rows](./rows.md)
- [Session](./session.md)
- [Sessions](./sessions.md)
- [Stats](./stats.md)
- [Tls](./tls.md)
- [Tool-event](./tool-event.md)
- [Tool-events](./tool-events.md)
- [Version](./version.md)
- [Ws](./ws.md)
- [Ui](./ui.md)
- [Libraries](./libraries.md)

## Quick Stats

- Routes: **83**
- Models: **0**
- Components: **15**
- Env vars: **7** required, **0** with defaults

## How to Use

- **New session:** read `index.md` (this file) for orientation — WHERE things are
- **Architecture question:** read `overview.md` (~500 tokens)
- **Domain question:** read the relevant article, then **read those source files**
- **Library question:** read `libraries.md`, then read the listed source files
- **Before implementing anything:** read the source files listed in the article
- **Full source context:** read `.codesight/CODESIGHT.md`

## What the Wiki Does Not Cover

These exist in your codebase but are **not** reflected in wiki articles:
- Routes registered dynamically at runtime (loops, plugin factories, `app.use(dynamicRouter)`)
- Internal routes from npm packages (e.g. Better Auth's built-in `/api/auth/*` endpoints)
- WebSocket and SSE handlers
- Raw SQL tables not declared through an ORM
- Computed or virtual fields absent from schema declarations
- TypeScript types that are not actual database columns
- Routes marked `[inferred]` were detected via regex and may have lower precision
- gRPC, tRPC, and GraphQL resolvers may be partially captured

When in doubt, search the source. The wiki is a starting point, not a complete inventory.

---
_Last compiled: 2026-05-26 · 34 articles · [codesight](https://github.com/Houseofmvps/codesight)_