// @vitest-environment jsdom
//
// Task 5 (identity canonicalization at ingestion): TopBar must navigate on
// a ToolEvent's canonical `key` (set once by normalizeToolEvent), never by
// reconstructing "${evt.host}/${evt.session}" itself -- that string would be
// the raw peer fingerprint + mutable display label, a DIFFERENT encoding
// than SessionView.key, and would silently fail to navigate for a remote
// session or one that has been renamed.
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { TopBar } from './TopBar'
import type { ToolEvent } from '../hooks/useToolEvents'

afterEach(() => {
  cleanup()
})

function makeEvent(over: Partial<ToolEvent> = {}): ToolEvent {
  return {
    key: 'owner-remote-1/session-stable-1',
    tool: 'claude',
    status: 'waiting',
    host: 'peer-fingerprint-remote',
    host_name: 'remote-box',
    session: 'my-display-label',
    session_id: 'session-stable-1',
    window: 0,
    pane: '%1',
    timestamp: new Date().toISOString(),
    ...over,
  }
}

describe('TopBar remote alert navigation (Task 5)', () => {
  it('clicking the primary alert for a remote event opens the session via its canonical key, not a host/session string', () => {
    const onJumpToSession = vi.fn()
    const evt = makeEvent()

    render(
      <TopBar
        currentView="overview"
        onOverview={() => {}}
        onSettings={() => {}}
        events={[evt]}
        connected={true}
        onJumpToSession={onJumpToSession}
        onDismiss={() => {}}
        onDismissAll={() => {}}
        glance={{ parked: 0, working: 0, waiting: 1 }}
      />,
    )

    fireEvent.click(screen.getByText('claude'.toUpperCase()))

    expect(onJumpToSession).toHaveBeenCalledTimes(1)
    const [key, windowIndex, pane] = onJumpToSession.mock.calls[0]
    expect(key).toBe(evt.key)
    // Proves the call did NOT reconstruct "${host}/${session}" (that would
    // be "peer-fingerprint-remote/my-display-label", a different string).
    expect(key).not.toBe(`${evt.host}/${evt.session}`)
    expect(windowIndex).toBe(evt.window)
    expect(pane).toBe(evt.pane)
  })
})
