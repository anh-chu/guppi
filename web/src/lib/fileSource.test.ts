import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  basename,
  classifyGrantFailure,
  fetchFileText,
  FILE_SIZE_LIMIT_BYTES,
  fileUrlForToken,
  loadFile,
  mintFileToken,
  needsText,
} from './fileSource'

/** Minimal Response stand-in: only what fileSource actually reads. */
function res(init: {
  ok?: boolean
  status?: number
  json?: unknown
  text?: string
  bytes?: Uint8Array
  contentLength?: string
}): Response {
  const status = init.status ?? (init.ok === false ? 500 : 200)
  return {
    ok: init.ok ?? status < 400,
    status,
    headers: { get: (k: string) => (k.toLowerCase() === 'content-length' ? init.contentLength ?? null : null) },
    json: async () => {
      if (init.json === undefined) throw new Error('no json')
      return init.json
    },
    text: async () => init.text ?? '',
    arrayBuffer: async () => {
      const b = init.bytes ?? new TextEncoder().encode(init.text ?? '')
      return b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength) as ArrayBuffer
    },
  } as unknown as Response
}

function stubFetch(impl: (url: string, init?: RequestInit) => Promise<Response>) {
  const spy = vi.fn(impl)
  vi.stubGlobal('fetch', spy)
  return spy
}

afterEach(() => { vi.unstubAllGlobals() })

describe('needsText', () => {
  it('sends text kinds through content', () => {
    for (const kind of ['markdown', 'csv', 'html', 'text', 'source'] as const) {
      expect(needsText(kind)).toBe(true)
    }
  })

  // These are exactly the kinds that could never work over postMessage: they
  // need a URL the browser can range-request, which is why the viewer had to
  // move onto termyard's own origin.
  it('sends asset kinds through a URL', () => {
    for (const kind of ['pdf', 'image', 'media', 'binary'] as const) {
      expect(needsText(kind)).toBe(false)
    }
  })
})

describe('basename', () => {
  it('takes the leaf', () => {
    expect(basename('/home/sil/guppi/main.go')).toBe('main.go')
    expect(basename('README.md')).toBe('README.md')
  })
  it('falls back to the whole path when there is no leaf', () => {
    expect(basename('/')).toBe('/')
  })
})

describe('classifyGrantFailure', () => {
  // The peer refuses before sending anything, so this string is the ONLY signal
  // that the 10 MB relay ceiling was hit.
  it('names the 10 MB relay cap for a remote file', () => {
    const f = classifyGrantFailure(404, 'remote file: file too large', true)
    expect(f.title).toBe('File too large')
    expect(f.detail).toContain('10 MB')
    expect(f.detail).toContain('control link')
  })

  it('does not blame the control link for a local file', () => {
    const f = classifyGrantFailure(404, 'file too large', false)
    expect(f.title).toBe('File too large')
    expect(f.detail).not.toContain('control link')
  })

  it('distinguishes an unreachable host from a missing file', () => {
    expect(classifyGrantFailure(502, 'peer not connected', true).title).toBe('Host not reachable')
    expect(classifyGrantFailure(404, 'not found', false).title).toBe('File not found')
  })

  // The server answers a local miss with the bare string "not found". Echoing it
  // as the detail produced "File not found / not found", which tells the user
  // nothing twice, and the remote wording blamed a host that was this machine.
  it('does not blame a host for a local miss, or repeat the title', () => {
    const local = classifyGrantFailure(404, 'not found', false)
    expect(local.detail).toBe('The path does not exist.')
    expect(local.detail).not.toContain('host')
    const remote = classifyGrantFailure(404, 'not found', true)
    expect(remote.detail).toContain('The host answered')
  })

  it('keeps a local 404 body that actually says something', () => {
    expect(classifyGrantFailure(404, 'path escapes the workspace', false).detail)
      .toBe('path escapes the workspace')
  })

  it('tells the user to reload when the session died', () => {
    expect(classifyGrantFailure(401, '', false).title).toBe('Not authorized')
    expect(classifyGrantFailure(403, '', false).title).toBe('Not authorized')
  })

  it('keeps an unrecognised status legible', () => {
    const f = classifyGrantFailure(418, 'teapot', false)
    expect(f.detail).toContain('418')
    expect(f.detail).toContain('teapot')
  })
})

describe('mintFileToken', () => {
  it('passes the host through so the grant relays over the mesh', async () => {
    const spy = stubFetch(async () => res({ json: { token: 'abc' } }))
    const out = await mintFileToken('/tmp/a.md', 'peer-1')
    expect(out).toEqual({ token: 'abc' })
    const url = spy.mock.calls[0][0] as string
    expect(url).toContain('path=%2Ftmp%2Fa.md')
    expect(url).toContain('host=peer-1')
    expect((spy.mock.calls[0][1] as RequestInit).method).toBe('POST')
  })

  it('omits host for a local file', async () => {
    const spy = stubFetch(async () => res({ json: { token: 'abc' } }))
    await mintFileToken('/tmp/a.md')
    expect(spy.mock.calls[0][0] as string).not.toContain('host=')
  })

  it('reports a rejection instead of throwing', async () => {
    stubFetch(async () => res({ status: 404, text: 'not found' }))
    const out = await mintFileToken('/nope')
    expect('error' in out && out.error.title).toBe('File not found')
  })

  it('reports a grant with no token', async () => {
    stubFetch(async () => res({ json: {} }))
    const out = await mintFileToken('/tmp/a.md')
    expect('error' in out && out.error.detail).toContain('no token')
  })

  it('reports a network failure', async () => {
    stubFetch(async () => { throw new Error('offline') })
    const out = await mintFileToken('/tmp/a.md')
    expect('error' in out && out.error.title).toBe('Could not open file')
  })
})

describe('fetchFileText', () => {
  it('fetches through the token, same origin so the cookie rides along', async () => {
    const spy = stubFetch(async () => res({ text: 'hello' }))
    const out = await fetchFileText('tok')
    expect(out).toEqual({ content: 'hello' })
    expect(spy.mock.calls[0][0]).toBe('/file?token=tok')
  })

  it('decodes multi-byte content correctly', async () => {
    stubFetch(async () => res({ text: 'héllo → ok' }))
    expect(await fetchFileText('tok')).toEqual({ content: 'héllo → ok' })
  })

  // Content-Length is free and is the difference between a message and a tab
  // wedged on a multi-gigabyte log.
  it('refuses an oversized body from the declared length alone', async () => {
    const spy = stubFetch(async () => res({ contentLength: String(FILE_SIZE_LIMIT_BYTES + 1) }))
    const out = await fetchFileText('tok')
    expect('error' in out && out.error.title).toBe('File too large')
    expect(spy).toHaveBeenCalledOnce()
  })

  it('refuses an oversized body with no declared length', async () => {
    stubFetch(async () => res({ bytes: new Uint8Array(FILE_SIZE_LIMIT_BYTES + 1) }))
    const out = await fetchFileText('tok')
    expect('error' in out && out.error.title).toBe('File too large')
  })

  it('names the TTL when the grant has expired', async () => {
    stubFetch(async () => res({ status: 403 }))
    const out = await fetchFileText('tok')
    expect('error' in out && out.error.title).toBe('Read grant expired')
    expect('error' in out && out.error.detail).toContain('5 minutes')
  })
})

describe('loadFile', () => {
  it('gives a text kind content and never builds an asset URL', async () => {
    stubFetch(async (url) =>
      url.startsWith('/file/grant') ? res({ json: { token: 'tok' } }) : res({ text: '# hi' }),
    )
    const out = await loadFile({ path: '/w/notes.md', kind: 'markdown' })
    expect(out.ok).toBe(true)
    if (!out.ok) return
    expect(out.value.content).toBe('# hi')
    expect(out.value.assetUrl).toBeUndefined()
    expect(out.value.filename).toBe('notes.md')
  })

  it('gives an asset kind a URL and never fetches the bytes itself', async () => {
    const spy = stubFetch(async () => res({ json: { token: 'tok' } }))
    const out = await loadFile({ path: '/w/a.png', kind: 'image' })
    expect(out.ok).toBe(true)
    if (!out.ok) return
    expect(out.value.assetUrl).toBe(fileUrlForToken('tok'))
    expect(out.value.content).toBeUndefined()
    // One call only: the browser fetches the image, which is what makes the
    // session cookie and range requests work.
    expect(spy).toHaveBeenCalledOnce()
  })

  it('surfaces a grant failure without touching /file', async () => {
    const spy = stubFetch(async () => res({ status: 404, text: 'remote file: file too large' }))
    const out = await loadFile({ path: '/w/big.mp4', kind: 'media', hostId: 'peer-1' })
    expect(out.ok).toBe(false)
    if (out.ok) return
    expect(out.error.title).toBe('File too large')
    expect(spy).toHaveBeenCalledOnce()
  })

  // A remembered result is how "the second file you open is ignored" happens.
  it('mints a fresh token on every call', async () => {
    let n = 0
    const spy = stubFetch(async (url) =>
      url.startsWith('/file/grant') ? res({ json: { token: `tok${++n}` } }) : res({ text: 'x' }),
    )
    const a = await loadFile({ path: '/w/a.md', kind: 'markdown' })
    const b = await loadFile({ path: '/w/b.md', kind: 'markdown' })
    expect(a.ok && b.ok).toBe(true)
    expect(spy.mock.calls.filter(c => (c[0] as string).startsWith('/file/grant'))).toHaveLength(2)
    const again = await loadFile({ path: '/w/a.png', kind: 'image' })
    expect(again.ok && again.value.assetUrl).toBe(fileUrlForToken('tok3'))
  })
})
