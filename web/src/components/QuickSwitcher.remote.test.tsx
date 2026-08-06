// @vitest-environment jsdom
//
// Task 5 (identity canonicalization at ingestion): QuickSwitcher's waiting-event
// items must select on a ToolEvent's canonical `key`, not a reconstructed
// "${evt.host}/${evt.session}" string (raw fingerprint + mutable display label).
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@testing-library/react'
import { QuickSwitcher } from './QuickSwitcher'
import type { ToolEvent } from '../hooks/useToolEvents'

afterEach(() => {
  cleanup()
})

// jsdom doesn't implement scrollIntoView; QuickSwitcher calls it on selection change.
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = vi.fn()
}

describe('QuickSwitcher remote event navigation (Task 5)', () => {
  it('selecting a waiting event from a remote peer selects its canonical key', () => {
    const onSelect = vi.fn()
    const evt: ToolEvent = {
      key: 'owner-remote-1/session-stable-1',
      tool: 'claude',
      status: 'waiting',
      host: 'peer-fingerprint-remote',
      host_name: 'remote-box',
      session: 'my-display-label',
      session_id: 'session-stable-1',
      window: 2,
      timestamp: new Date().toISOString(),
    }

    render(
      <QuickSwitcher
        sessions={[]}
        waitingEvents={[evt]}
        onSelect={onSelect}
        onOverview={() => {}}
        onCreateSession={() => {}}
        onClose={() => {}}
      />,
    )

    fireEvent.click(screen.getByText(evt.session))

    expect(onSelect).toHaveBeenCalledTimes(1)
    const [key, windowIndex] = onSelect.mock.calls[0]
    expect(key).toBe(evt.key)
    expect(key).not.toBe(`${evt.host}/${evt.session}`)
    expect(windowIndex).toBe(evt.window)
  })
})
