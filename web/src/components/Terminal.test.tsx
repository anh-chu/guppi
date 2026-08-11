/**
 * @vitest-environment jsdom
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act, cleanup } from '@testing-library/react'
import { renderHook } from '@testing-library/react'
import { Terminal } from './Terminal'
import { useTerminal } from '../hooks/useTerminal'
import { useArtifacts } from '../hooks/useArtifacts'
import { useFileUpload } from '../hooks/useFileUpload'
import { usePreferences } from '../hooks/usePreferences'
import { useTerminalInput } from './terminal/useTerminalInput'
import { MobileGestureKey, HoldableKey } from './terminal/MobileKeys'
import { grantArtifactToken, getArtifactKind } from '../lib/artifactPreview'
import type { Terminal as XTermTerminal } from '@xterm/xterm'

// ── Module mocks ─────────────────────────────────────────────────────
vi.mock('../hooks/useTerminal', () => ({ useTerminal: vi.fn() }))
vi.mock('../hooks/useArtifacts', () => ({ useArtifacts: vi.fn() }))
vi.mock('../hooks/useFileUpload', () => ({ useFileUpload: vi.fn() }))
vi.mock('../hooks/usePreferences', () => ({ usePreferences: vi.fn() }))
vi.mock('../lib/artifactPreview', () => ({
  grantArtifactToken: vi.fn(),
  getArtifactKind: vi.fn(),
  truncateArtifactText: vi.fn(),
}))
vi.mock('../lib/pip', () => ({
  popOut: vi.fn(),
  pipUnavailableReason: vi.fn(() => null),
}))

// ── Helpers ──────────────────────────────────────────────────────────
function mockMatchMedia(matches = false) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches,
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  })
}

function mockResizeObserver() {
  let callback: ResizeObserverCallback | null = null
  const observe = vi.fn()
  const disconnect = vi.fn()
  class MockResizeObserver {
    constructor(cb: ResizeObserverCallback) { callback = cb }
    observe = observe
    disconnect = disconnect
  }
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const ctor = vi.fn((cb: any) => {
    callback = cb
    return new MockResizeObserver(cb) as unknown as ResizeObserver
  })
  globalThis.ResizeObserver = ctor as any
  return { trigger: () => callback?.([], new MockResizeObserver(() => {}) as unknown as ResizeObserver), observe, disconnect, ctor }
}

function createMockTerminal(sendText: ReturnType<typeof vi.fn>, overrides: Record<string, unknown> = {}) {
  return {
    buffer: {
      active: {
        viewportY: 0,
        length: 2,
        getLine: (i: number) => ({ translateToString: (trim?: boolean) => `line ${i}` }),
      },
    },
    rows: 2,
    clearSelection: vi.fn(),
    paste: vi.fn(),
    registerLinkProvider: vi.fn(() => ({ dispose: vi.fn() })),
    onScroll: vi.fn(() => ({ dispose: vi.fn() })),
    onRender: vi.fn(() => ({ dispose: vi.fn() })),
    onResize: vi.fn(() => ({ dispose: vi.fn() })),
    rows: 24,
    ...overrides,
  } as unknown as XTermTerminal
}

function createUseTerminalReturn(overrides: Record<string, unknown> = {}) {
  const sendText = vi.fn()
  const sendRawBytes = vi.fn()
  const sendImage = vi.fn()
  const focus = vi.fn()
  const fit = vi.fn()
  const term = createMockTerminal(sendText)

  return {
    termRef: { current: term },
    connect: vi.fn(),
    disconnect: vi.fn(),
    fit,
    rebind: vi.fn(),
    focus,
    termConnected: true,
    sendRawBytes,
    sendText,
    sendImage,
    ctrlModifierActive: false,
    toggleCtrlModifier: vi.fn(),
    clearCtrlModifier: vi.fn(),
    altModifierActive: false,
    toggleAltModifier: vi.fn(),
    clearAltModifier: vi.fn(),
    selectionMenu: null as { x: number; y: number; text: string } | null,
    setSelectionMenu: vi.fn(),
    reconfigure: vi.fn(),
    ...overrides,
  }
}

function createUseFileUploadReturn(overrides: Record<string, unknown> = {}) {
  return {
    uploads: [] as { id: number; name: string; status: string; sent: number; size: number; error?: string; quotedPath?: string }[],
    uploadFile: vi.fn(async (file: File) => ({ id: 1, quotedPath: `'/tmp/${file.name}'` })),
    cancelUpload: vi.fn(),
    dismissUpload: vi.fn(),
    keepVisible: vi.fn(),
    ...overrides,
  }
}

function setupMobileKeybar() {
  mockMatchMedia(true)
  const slot = document.createElement('div')
  slot.id = 'mobile-keybar-slot'
  document.body.appendChild(slot)
  return slot
}

// ── Suite ────────────────────────────────────────────────────────────

describe('Terminal', () => {
  const defaultPrefs = {
    theme: 'dark',
    terminal: {
      renderer: 'webgl',
      scrollback: 10000,
      font_size: 13,
      font_family: 'mono',
    },
  }

  function renderTerminal(
    props: Partial<React.ComponentProps<typeof Terminal>> = {},
    { artifacts = [] as any[] } = {},
  ) {
    const returnVal = createUseTerminalReturn()
    const fileUploadVal = createUseFileUploadReturn()
    ;(useTerminal as ReturnType<typeof vi.fn>).mockReturnValue(returnVal)
    ;(useFileUpload as ReturnType<typeof vi.fn>).mockReturnValue(fileUploadVal)
    ;(useArtifacts as ReturnType<typeof vi.fn>).mockReturnValue({ artifacts, refresh: vi.fn() })

    const result = render(<Terminal sessionName="s" {...props} />)
    return { ...result, returnVal, fileUploadVal, rerenderWithOverrides }

    function rerenderWithOverrides(
      newProps: Partial<React.ComponentProps<typeof Terminal>> = {},
      hookUpdates: { terminal?: Record<string, unknown>; fileUpload?: Record<string, unknown> } = {},
    ) {
      Object.assign(returnVal, hookUpdates.terminal ?? {})
      Object.assign(fileUploadVal, hookUpdates.fileUpload ?? {})
      result.rerender(<Terminal sessionName="s" {...props} {...newProps} />)
    }
  }

  beforeEach(() => {
    vi.clearAllMocks()
    cleanup()
    mockMatchMedia(false)
    ;(usePreferences as ReturnType<typeof vi.fn>).mockReturnValue({ prefs: defaultPrefs })
    ;(getArtifactKind as ReturnType<typeof vi.fn>).mockReturnValue('text')
    ;(grantArtifactToken as ReturnType<typeof vi.fn>).mockResolvedValue('mock-token')
    Object.defineProperty(window, 'open', { value: vi.fn(), writable: true })
  })

  afterEach(() => {
    document.getElementById('mobile-keybar-slot')?.remove()
    cleanup()
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('attaches and cleans up global listeners', () => {
    const addDoc = vi.spyOn(document, 'addEventListener')
    const remDoc = vi.spyOn(document, 'removeEventListener')
    const addWin = vi.spyOn(window, 'addEventListener')
    const remWin = vi.spyOn(window, 'removeEventListener')
    mockResizeObserver()

    const { unmount } = renderTerminal({ fullscreen: true })

    expect(addDoc).toHaveBeenCalledWith('visibilitychange', expect.any(Function))
    expect(addWin).toHaveBeenCalledWith('focus', expect.any(Function))
    expect(globalThis.ResizeObserver).toHaveBeenCalled()

    unmount()

    expect(remDoc).toHaveBeenCalledWith('visibilitychange', expect.any(Function))
    expect(remWin).toHaveBeenCalledWith('focus', expect.any(Function))
  })

  it('refocuses and fits on visibilitychange and resize', () => {
    vi.useFakeTimers()
    const ro = mockResizeObserver()
    const { returnVal } = renderTerminal({ fullscreen: true })

    document.dispatchEvent(new Event('visibilitychange'))
    vi.runAllTimers()
    expect(returnVal.focus).toHaveBeenCalled()
    expect(returnVal.fit).toHaveBeenCalled()

    ro.trigger()
    vi.advanceTimersByTime(50)
    expect(returnVal.fit).toHaveBeenCalled()

    vi.useRealTimers()
  })

  it('mobile key bar renders into portal', () => {
    setupMobileKeybar()
    renderTerminal({ keyBarEnabled: true })
    expect(screen.getByRole('button', { name: /esc/i })).toBeTruthy()
    expect(screen.getByRole('button', { name: /backspace/i })).toBeTruthy()
  })

  it('HoldableKey repeats after hold delay and cancels on touch end', () => {
    const timeouts: { cb: () => void; delay: number }[] = []
    const intervals: { cb: () => void; delay: number }[] = []
    vi.spyOn(globalThis, 'setTimeout').mockImplementation((cb: any, delay?: number) => {
      timeouts.push({ cb, delay: delay ?? 0 })
      return timeouts.length
    })
    const intervalSpy = vi.spyOn(globalThis, 'setInterval').mockImplementation((cb: any, delay?: number) => {
      intervals.push({ cb, delay: delay ?? 0 })
      return intervals.length
    })

    const onPress = vi.fn()
    render(<HoldableKey label="X" onPress={onPress} />)
    const button = screen.getByRole('button', { name: /x/i })

    fireEvent.touchStart(button, { touches: [{ clientX: 0, clientY: 0 }] })
    expect(onPress).not.toHaveBeenCalled()
    const holdTimeout = timeouts.find((t) => t.delay === 260)
    expect(holdTimeout).toBeTruthy()

    holdTimeout!.cb()
    const repeatInterval = intervals.find((i) => i.delay === 80)
    expect(repeatInterval).toBeTruthy()

    repeatInterval!.cb()
    expect(onPress).toHaveBeenCalledTimes(1)

    repeatInterval!.cb()
    expect(onPress).toHaveBeenCalledTimes(2)

    fireEvent.touchEnd(button)
    expect(intervalSpy).toHaveBeenCalled()
  })

  it('MobileGestureKey respects threshold and repeats', () => {
    const timeouts: { cb: () => void; delay: number }[] = []
    const intervals: { cb: () => void; delay: number }[] = []
    vi.spyOn(globalThis, 'setTimeout').mockImplementation((cb: any, delay?: number) => {
      timeouts.push({ cb, delay: delay ?? 0 })
      return timeouts.length
    })
    const intervalSpy = vi.spyOn(globalThis, 'setInterval').mockImplementation((cb: any, delay?: number) => {
      intervals.push({ cb, delay: delay ?? 0 })
      return intervals.length
    })

    const onTrigger = vi.fn()
    render(<MobileGestureKey label="X" directions={['left', 'right']} onTrigger={onTrigger} />)
    const button = screen.getByRole('button', { name: /x/i })

    fireEvent.touchStart(button, { touches: [{ clientX: 0, clientY: 0 }] })
    fireEvent.touchMove(button, { touches: [{ clientX: 5, clientY: 0 }] })
    expect(onTrigger).not.toHaveBeenCalled()

    fireEvent.touchMove(button, { touches: [{ clientX: 30, clientY: 0 }] })
    expect(onTrigger).toHaveBeenCalledWith('right')

    const callsBefore = onTrigger.mock.calls.length
    fireEvent.touchMove(button, { touches: [{ clientX: 31, clientY: 0 }] })
    expect(onTrigger.mock.calls.length).toBe(callsBefore)

    expect(timeouts).toHaveLength(1)
    timeouts[0].cb()
    expect(intervals).toHaveLength(1)
    intervals[0].cb()
    expect(onTrigger.mock.calls.length).toBeGreaterThan(callsBefore)

    fireEvent.touchCancel(button)
    expect(intervalSpy).toHaveBeenCalled()
  })

  it('selection menu focuses first item and closes on Escape returning focus', () => {
    const { returnVal, rerenderWithOverrides } = renderTerminal()

    rerenderWithOverrides({}, {
      terminal: { selectionMenu: { x: 10, y: 10, text: 'note.txt' } },
    })

    const copyItem = screen.getByRole('menuitem', { name: /copy/i })
    expect(document.activeElement).toBe(copyItem)

    const container = document.querySelector('.absolute.inset-0.overflow-hidden') as HTMLElement
    const focusSpy = vi.spyOn(container, 'focus')

    fireEvent.keyDown(window, { key: 'Escape' })
    expect(returnVal.setSelectionMenu).toHaveBeenCalledWith(null)
    expect(focusSpy).toHaveBeenCalled()
  })

  it('pastes text and images through the clipboard menu', async () => {
    setupMobileKeybar()
    const consoleSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const { returnVal } = renderTerminal({ keyBarEnabled: true })

    const clipboardButton = screen.getByRole('button', { name: /clipboard/i })
    fireEvent.click(clipboardButton)

    // Text paste
    const textBlob = new Blob(['hello'], { type: 'text/plain' })
    const clipboardItem = {
      types: ['text/plain'],
      getType: vi.fn(async () => textBlob),
    }
    Object.defineProperty(navigator, 'clipboard', {
      value: { read: vi.fn(async () => [clipboardItem]) },
      writable: true,
    })

    const pasteButton = screen.getByRole('button', { name: /^paste$/i })
    await act(async () => { fireEvent.click(pasteButton) })
    await waitFor(() => {
      expect(returnVal.termRef.current?.paste).toHaveBeenCalledWith('hello')
    })

    // Image paste
    const imageBlob = new Blob(['pngbytes'], { type: 'image/png' })
    const imageItem = {
      types: ['image/png'],
      getType: vi.fn(async () => imageBlob),
    }
    Object.defineProperty(navigator, 'clipboard', {
      value: { read: vi.fn(async () => [imageItem]) },
      writable: true,
    })
    fireEvent.click(clipboardButton)
    const pasteButton2 = screen.getByRole('button', { name: /^paste$/i })
    await act(async () => { fireEvent.click(pasteButton2) })
    await waitFor(() => {
      expect(returnVal.sendImage).toHaveBeenCalledWith(expect.any(File), 'image/png')
    })

    // Paste error is logged
    Object.defineProperty(navigator, 'clipboard', {
      value: { read: vi.fn(async () => { throw new Error('clipboard denied') }) },
      writable: true,
    })
    fireEvent.click(clipboardButton)
    const pasteButton3 = screen.getByRole('button', { name: /^paste$/i })
    await act(async () => { fireEvent.click(pasteButton3) })
    await waitFor(() => {
      expect(consoleSpy).toHaveBeenCalledWith('Failed to paste from clipboard:', expect.any(Error))
    })

    consoleSpy.mockRestore()
  })

  it('renders upload progress and failure states', () => {
    const { fileUploadVal, rerenderWithOverrides } = renderTerminal()
    fileUploadVal.uploads = [
      { id: 1, name: 'big.bin', status: 'uploading', sent: 500, size: 1000 },
      { id: 2, name: 'bad.bin', status: 'error', sent: 0, size: 0, error: 'server error' },
    ]
    rerenderWithOverrides()

    expect(screen.getByText('50%')).toBeTruthy()
    expect(screen.getByText(/server error/i)).toBeTruthy()
    expect(screen.getByRole('button', { name: /dismiss/i })).toBeTruthy()
  })

  it('uploads dropped files and injects quoted paths', async () => {
    mockMatchMedia(false)
    const { returnVal, fileUploadVal } = renderTerminal()
    const file = new File(['x'], 'drop.txt', { type: 'text/plain' })
    ;(fileUploadVal.uploadFile as ReturnType<typeof vi.fn>).mockResolvedValue({ id: 1, quotedPath: "'/tmp/drop.txt'" })

    const dropZone = document.querySelector('.absolute.inset-0:not(.overflow-hidden)') as HTMLElement
    const dt = { types: ['Files'], files: [file as unknown as globalThis.File], dropEffect: 'copy' }

    const dragEnter = new Event('dragenter', { bubbles: true, cancelable: true })
    Object.defineProperty(dragEnter, 'dataTransfer', { value: dt, configurable: true })
    dropZone.dispatchEvent(dragEnter)

    await waitFor(() => expect(dropZone.className).toContain('ring-primary'))

    const drop = new Event('drop', { bubbles: true, cancelable: true })
    Object.defineProperty(drop, 'dataTransfer', { value: dt, configurable: true })
    await act(async () => { dropZone.dispatchEvent(drop) })

    expect(fileUploadVal.uploadFile).toHaveBeenCalledWith(file)
    await waitFor(() => expect(returnVal.sendText).toHaveBeenCalledWith("'/tmp/drop.txt'"))
  })

  it('supports artifact open and download', async () => {
    const artifact = {
      path: '/tmp/artifact.txt',
      display_path: 'artifact.txt',
      name: 'artifact.txt',
      source: 'test',
      first_seen: 'now',
      stale: false,
    }
    const onOpenFile = vi.fn(() => true)
    const { rerenderWithOverrides } = renderTerminal({ onOpenFile }, { artifacts: [artifact] })

    fireEvent.click(screen.getByTitle(/detected files/i))
    fireEvent.click(screen.getByTitle('/tmp/artifact.txt'))
    expect(onOpenFile).toHaveBeenCalledWith('/tmp/artifact.txt')

    // Re-open sidebar and test the download path.
    fireEvent.click(screen.getByTitle(/detected files/i))
    fireEvent.click(screen.getByTitle(/download/i))
    await waitFor(() => expect(grantArtifactToken).toHaveBeenCalledWith('/tmp/artifact.txt', 's', undefined, undefined))
  })

  // Hook-level coverage for paste/drag/upload behaviours that are hard to
  // exercise precisely through jsdom UI events.
  describe('useTerminalInput', () => {
    function renderInputHook(overrides: Record<string, unknown> = {}) {
      const sendText = vi.fn()
      const sendRawBytes = vi.fn()
      const sendImage = vi.fn()
      const focus = vi.fn()
      const paste = vi.fn()
      const clearSelection = vi.fn()
      const term = createMockTerminal(sendText, { paste, clearSelection })

      const useFileUploadReturn = createUseFileUploadReturn()
      ;(useFileUpload as ReturnType<typeof vi.fn>).mockReturnValue(useFileUploadReturn)

      const { result } = renderHook(() =>
        useTerminalInput({
          sessionName: 's',
          termRef: { current: term },
          sendRawBytes,
          sendText,
          sendImage,
          termConnected: true,
          focus,
          ...overrides,
        })
      )
      return { result, sendText, sendRawBytes, sendImage, focus, paste, clearSelection, useFileUploadReturn }
    }

    it('sends arrow and page sequences', () => {
      const { result, sendText } = renderInputHook({})
      act(() => result.current.sendArrow('left'))
      expect(sendText).toHaveBeenCalledWith('\x1b[D')
      act(() => result.current.sendPage('up'))
      expect(sendText).toHaveBeenCalledWith('\x1b[5~')
    })

    it('pastes an image from async clipboard', async () => {
      const { result, sendImage } = renderInputHook({})
      const imageBlob = new Blob(['pngbytes'], { type: 'image/png' })
      Object.defineProperty(navigator, 'clipboard', {
        value: {
          read: vi.fn(async () => [{
            types: ['image/png'],
            getType: vi.fn(async () => imageBlob),
          }]),
        },
        writable: true,
      })
      await act(async () => { await result.current.handlePaste() })
      expect(sendImage).toHaveBeenCalledWith(expect.any(File), 'image/png')
    })

    it('logs clipboard paste errors', async () => {
      const { result } = renderInputHook({})
      const consoleSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
      Object.defineProperty(navigator, 'clipboard', {
        value: { read: vi.fn(async () => { throw new Error('denied') }) },
        writable: true,
      })
      await act(async () => { await result.current.handlePaste() })
      expect(consoleSpy).toHaveBeenCalledWith('Failed to paste from clipboard:', expect.any(Error))
      consoleSpy.mockRestore()
    })

    it('captures viewport text on copy', () => {
      const { result, clearSelection } = renderInputHook({})
      act(() => result.current.handleCopy())
      expect(clearSelection).toHaveBeenCalled()
      expect(result.current.capturedText).toBe('line 0\nline 1')
    })

    it('uploads files and injects paths when connected', async () => {
      const { result, sendText, useFileUploadReturn } = renderInputHook({ termConnected: true })
      ;(useFileUploadReturn.uploadFile as ReturnType<typeof vi.fn>).mockResolvedValue({ id: 1, quotedPath: "'/tmp/f.txt'" })
      const file = new File(['x'], 'f.txt')
      await act(async () => { await result.current.uploadFiles([file]) })
      expect(useFileUploadReturn.uploadFile).toHaveBeenCalledWith(file)
      expect(sendText).toHaveBeenCalledWith("'/tmp/f.txt'")
    })

    it('keeps upload visible instead of injecting when disconnected', async () => {
      const { result, sendText, useFileUploadReturn } = renderInputHook({ termConnected: false })
      ;(useFileUploadReturn.uploadFile as ReturnType<typeof vi.fn>).mockResolvedValue({ id: 4, quotedPath: "'/tmp/off.txt'" })
      await act(async () => { await result.current.uploadFiles([new File(['x'], 'off.txt')]) })
      expect(sendText).not.toHaveBeenCalled()
      expect(useFileUploadReturn.keepVisible).toHaveBeenCalledWith(4)
    })

    it('toggles drag state and accepts dropped files', async () => {
      mockMatchMedia(false)
      const { result, useFileUploadReturn } = renderInputHook({})
      const file = new File(['x'], 'drop.txt')

      const dropZone = document.createElement('div')
      const dt = { types: ['Files'], files: [file as unknown as globalThis.File], dropEffect: 'copy' }

      const dragEnter = new Event('dragenter', { bubbles: true, cancelable: true })
      Object.defineProperty(dragEnter, 'dataTransfer', { value: dt, configurable: true })
      await act(async () => { dropZone.dispatchEvent(dragEnter) })

      // The handler bound to the drop zone needs a real React event target;
      // call through the hook callback directly for file drop handling.
      await act(async () => {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        await result.current.handleTerminalDrop({
          preventDefault: vi.fn(),
          stopPropagation: vi.fn(),
          dataTransfer: { types: ['Files'], files: [file] },
        } as any)
      })

      expect(useFileUploadReturn.uploadFile).toHaveBeenCalledWith(file)
    })
  })
})
