import { describe, it, expect, vi } from 'vitest'
import { V2CommandClient, V2CommandError, V2CommandNetworkError } from './commands'

const ref = { owner: 'o', session: 's', window: 0, pane: 0 }
const other = { owner: 'o', session: 't', window: 0, pane: 0 }

describe('V2CommandClient', () => {
  it('reuses the same command id across retries after an ambiguous network failure', async () => {
    const ids: unknown[] = []
    const fetchImpl = vi.fn(async (_url: string, init: RequestInit) => {
      const body = JSON.parse(init.body as string)
      ids.push(body.id)
      if (ids.length < 3) throw new TypeError('network error')
      return new Response(JSON.stringify({ ok: true }), { status: 200 })
    }) as unknown as typeof fetch

    const client = new V2CommandClient({ fetchImpl, maxRetries: 3, retryDelayMs: 0 })
    await client.sessionCommand(ref, { action: 'kill' })

    expect(ids.length).toBe(3)
    expect(new Set(ids).size).toBe(1) // same id on every attempt
  })

  it('throws V2CommandNetworkError after exhausting retries', async () => {
    const fetchImpl = vi.fn(async () => {
      throw new TypeError('network error')
    }) as unknown as typeof fetch

    const client = new V2CommandClient({ fetchImpl, maxRetries: 1, retryDelayMs: 0 })
    await expect(client.sessionCommand(ref, { action: 'kill' })).rejects.toBeInstanceOf(V2CommandNetworkError)
  })

  it('does not retry a definitive (well-formed) error response', async () => {
    const fetchImpl = vi.fn(async () =>
      new Response(JSON.stringify({ code: 'not_found', message: 'no such session' }), { status: 404 }),
    ) as unknown as typeof fetch

    const client = new V2CommandClient({ fetchImpl, maxRetries: 3, retryDelayMs: 0 })
    await expect(client.sessionCommand(ref, { action: 'kill' })).rejects.toBeInstanceOf(V2CommandError)
    expect(fetchImpl).toHaveBeenCalledTimes(1)
  })

  it('workspaceCommand posts to /api/v2/workspace-commands with layout + action + nested params', async () => {
    let capturedUrl = ''
    let capturedBody: any = null
    const fetchImpl = vi.fn(async (url: string, init: RequestInit) => {
      capturedUrl = url
      capturedBody = JSON.parse(init.body as string)
      return new Response(JSON.stringify({ ref }), { status: 200 })
    }) as unknown as typeof fetch

    const client = new V2CommandClient({ fetchImpl, genId: () => 'fixed-id' })
    const result = await client.workspaceCommand('L1', { action: 'resize', split_id: 'sp1', ratio: 0.6 })

    expect(capturedUrl).toBe('/api/v2/workspace-commands')
    expect(capturedBody).toEqual({
      id: 'fixed-id',
      layout: 'L1',
      action: 'resize',
      params: { split_id: 'sp1', ratio: 0.6 },
    })
    expect(result).toEqual({ ref })
  })

  it('sessionCommand nests non-action fields under params', async () => {
    let capturedBody: any = null
    const fetchImpl = vi.fn(async (_url: string, init: RequestInit) => {
      capturedBody = JSON.parse(init.body as string)
      return new Response('{}', { status: 200 })
    }) as unknown as typeof fetch

    const client = new V2CommandClient({ fetchImpl, genId: () => 'fixed-id' })
    await client.sessionCommand(ref, { action: 'label', label: 'a' })
    expect(capturedBody).toEqual({ id: 'fixed-id', ref: 'o/s:0.0', action: 'label', params: { label: 'a' } })
  })

  it('createSession omits ref entirely: a create carries no placeholder ref on the wire', async () => {
    let capturedUrl = ''
    let capturedBody: any = null
    const fetchImpl = vi.fn(async (url: string, init: RequestInit) => {
      capturedUrl = url
      capturedBody = JSON.parse(init.body as string)
      return new Response('{}', { status: 200 })
    }) as unknown as typeof fetch

    const client = new V2CommandClient({ fetchImpl, genId: () => 'builtin-id' })
    await client.createSession({ action: 'create', name: 'alpha', cwd: '/tmp' })

    expect(capturedUrl).toBe('/api/v2/session-commands')
    // The `ref` member must be absent entirely -- the server assigns the
    // SessionID. Sending any ref-shaped placeholder gets rejected server-side
    // with "missing session id" once wire-encoded.
    expect(capturedBody).toEqual({ id: 'builtin-id', action: 'create', params: { name: 'alpha', cwd: '/tmp' } })
    expect('ref' in capturedBody).toBe(false)
  })

  it('sessionCommand generates a fresh id per call when none supplied', async () => {
    const ids: unknown[] = []
    const fetchImpl = vi.fn(async (_url: string, init: RequestInit) => {
      ids.push(JSON.parse(init.body as string).id)
      return new Response('{}', { status: 200 })
    }) as unknown as typeof fetch

    const client = new V2CommandClient({ fetchImpl })
    await client.sessionCommand(ref, { action: 'kill' })
    await client.sessionCommand(other, { action: 'kill' })
    expect(new Set(ids).size).toBe(2)
  })
})
