## Session identity refactor (edge case, multi-host)

Problem: compound identity (host+session name) reimplemented ad hoc in 5+ places:
- server/routes_common.go sessionKey()
- server/group_naming.go splitSessionKey()
- groupsync frontend JS mirror of sessionKey()
- toolevents/tracker.go (uses "\x00" separator, different format)
- sessionlaunch/service.go own sessionKey()

Found bug: artifactSessionKey() in toolevents/tracker.go intentionally ignores host -> same-name sessions on different hosts can cross-contaminate tool artifacts.

Host field on model.Session is documented as "peer fingerprint" (transport detail) but used as owner identity throughout ordering/grouping/pairing state -> peer reconnect/repair could make sessions look duplicated or vanish.

User-visible symptoms (edge case: only hits multi-host + duplicate session names):
- wrong artifacts/output attached to a session
- notifications/prompt preview mixed up across hosts
- sidebar group/split-pane layout breaks or empties after rename
- sidebar manual order silently resets
- peer reconnect looks like session identity change (dup/vanish in sidebar)
- rename/kill/schedule action could target wrong host's same-named session

Proposed fix (scoped, not a rewrite):
- Extend existing pkg/model.SessionRef (currently session:window.pane) with owner/host field instead of inventing a new type.
- owner_id = reuse existing peer fingerprint / PeerMgr.LocalID(), not a new ID scheme.
- Consolidate the 5 ad-hoc host+name string builders into one typed constructor/parser, normalize only at existing boundaries.
- Session Name (tmux/daemon key) is already stable/immutable in practice (state/naming.go keeps DisplayName decoupled from Name) - no need to invent session_id from scratch.
- Add regression tests: same-name session on two different hosts must not collide (esp. artifactSessionKey); rename must not change ordering/grouping/pairing references.

Priority: low (edge case, single-host users unaffected today). Revisit when multi-host/pairing usage grows.
