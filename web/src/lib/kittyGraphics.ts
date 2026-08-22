// Kitty graphics protocol support for guppi's xterm.js terminal.
//
// Implements the core of the kitty terminal graphics protocol so any program
// that speaks it (tode/terminal-browser, icat, timg, chafa -f kitty, viu, ...)
// can render raster images inside the browser terminal.
//
// Scope (the common denominator that works over a remote PTY):
//   - Direct transmission only (t=d). File/temp/shared-memory media (t=f/t/s)
//     reference server-side paths the browser cannot read, so they are refused.
//   - Formats: RGB (f=24), RGBA (f=32, default), PNG (f=100).
//   - Chunked transmission (m=1 / m=0).
//   - Actions: transmit (a=t), transmit+display (a=T), put (a=p), delete (a=d),
//     query (a=q, also used as the capability probe).
//   - Cursor placement, anchored to the scrollback so images scroll with text.
//
// Not implemented (rare / not needed by the target programs): unicode
// placeholders (U=1), animation (a=f/a=a/a=c), zlib compression (o=z),
// relative placements (P/Q/H/V), negative z-index (text-over-image).
//
// Escape form: ESC _ G <control-data> ; <base64-payload> ESC \

import type { IDisposable, Terminal } from '@xterm/xterm'
import { PLACEHOLDER_CODEPOINT, diacriticValue } from './kittyDiacritics'

const ESC = 0x1b
const ST_BACKSLASH = 0x5c // ESC \
const BEL = 0x07
const UNDERSCORE = 0x5f
const G = 0x47

type Segment =
  | { kind: 'text'; bytes: Uint8Array }
  | { kind: 'cmd'; control: Map<string, string>; payloadB64: string }

interface StoredImage {
  bitmap: ImageBitmap | null
  width: number // natural pixel width
  height: number // natural pixel height
}

interface Placement {
  key: number // image identity (i, or -I when only a number is given)
  absRow: number // absolute buffer row of the image top
  col: number // starting column
  cols: number // width in cells
  rows: number // height in cells
}

// A command awaiting more chunks (m=1). The control keys from the first chunk
// define the command; subsequent chunks only carry additional payload.
interface PendingChunks {
  control: Map<string, string>
  payload: string
}

export interface KittyGraphicsOptions {
  sendResponse: (bytes: Uint8Array) => void
}

const encoder = new TextEncoder()

function toU8(data: Uint8Array | string): Uint8Array {
  return typeof data === 'string' ? encoder.encode(data) : data
}

function parseControl(raw: string): Map<string, string> {
  const map = new Map<string, string>()
  for (const pair of raw.split(',')) {
    const eq = pair.indexOf('=')
    if (eq <= 0) continue
    map.set(pair.slice(0, eq), pair.slice(eq + 1))
  }
  return map
}

function num(map: Map<string, string>, key: string, dflt = 0): number {
  const v = map.get(key)
  if (v === undefined) return dflt
  const n = Number.parseInt(v, 10)
  return Number.isFinite(n) ? n : dflt
}

function imageKey(control: Map<string, string>): number {
  const i = num(control, 'i', 0)
  if (i > 0) return i
  const I = num(control, 'I', 0)
  if (I > 0) return -I
  return 0
}

// Inflate zlib-compressed (RFC 1950, kitty o=z) pixel data using the platform
// DecompressionStream. Kitty uses the zlib wrapper, which maps to 'deflate'.
async function inflateZlib(bytes: Uint8Array): Promise<Uint8Array> {
  const ds = new DecompressionStream('deflate')
  const stream = new Blob([bytes.buffer as ArrayBuffer]).stream().pipeThrough(ds)
  const buf = await new Response(stream).arrayBuffer()
  return new Uint8Array(buf)
}

function base64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let j = 0; j < bin.length; j++) out[j] = bin.charCodeAt(j)
  return out
}

/**
 * Split a raw byte stream into text runs and kitty graphics commands.
 *
 * `carry` is any trailing bytes from a previous call that looked like the start
 * of an APC sequence but were incomplete. Returns the parsed segments plus a
 * new `carry` to prepend next time.
 *
 * Chunk assembly (m=1) is handled by the caller via `pending`, because a single
 * logical command can span multiple `_G` escapes.
 */
export function scanStream(
  input: Uint8Array,
  carry: Uint8Array | null,
): { segments: Segment[]; carry: Uint8Array | null } {
  const data = carry && carry.length ? concat(carry, input) : input
  const segments: Segment[] = []
  let textStart = 0
  let i = 0

  while (i < data.length) {
    if (data[i] !== ESC) {
      i++
      continue
    }
    // Potential start of ESC _ G. Need at least 3 bytes to classify.
    if (i + 1 >= data.length) {
      // Lone trailing ESC: could begin an APC. Carry it.
      return finish(data, textStart, i, segments)
    }
    if (data[i + 1] !== UNDERSCORE) {
      i++
      continue
    }
    if (i + 2 >= data.length) {
      return finish(data, textStart, i, segments)
    }
    if (data[i + 2] !== G) {
      // ESC _ but not G: not a graphics command, skip past.
      i += 2
      continue
    }

    // Found `ESC _ G`. Find the string terminator: ESC \ or BEL.
    const termIdx = findTerminator(data, i + 3)
    if (termIdx === -1) {
      // Incomplete command; flush preceding text and carry the rest.
      if (i > textStart) segments.push({ kind: 'text', bytes: data.subarray(textStart, i) })
      return { segments, carry: data.subarray(i) }
    }

    // Flush preceding text.
    if (i > textStart) segments.push({ kind: 'text', bytes: data.subarray(textStart, i) })

    // Body is between `ESC _ G` and the terminator (ESC index or BEL index).
    const body = latin1(data, i + 3, termIdx)
    const semi = body.indexOf(';')
    const controlRaw = semi === -1 ? body : body.slice(0, semi)
    const payloadB64 = semi === -1 ? '' : body.slice(semi + 1)
    segments.push({ kind: 'cmd', control: parseControl(controlRaw), payloadB64 })

    // Advance past terminator (ESC \ is 2 bytes, BEL is 1).
    i = data[termIdx] === BEL ? termIdx + 1 : termIdx + 2
    textStart = i
  }

  if (data.length > textStart) segments.push({ kind: 'text', bytes: data.subarray(textStart) })
  return { segments, carry: null }
}

function finish(
  data: Uint8Array,
  textStart: number,
  escIdx: number,
  segments: Segment[],
): { segments: Segment[]; carry: Uint8Array | null } {
  if (escIdx > textStart) segments.push({ kind: 'text', bytes: data.subarray(textStart, escIdx) })
  return { segments, carry: data.subarray(escIdx) }
}

// Returns index of the terminator: for `ESC \` returns index of ESC; for BEL
// returns index of BEL. -1 if not found.
function findTerminator(data: Uint8Array, from: number): number {
  for (let j = from; j < data.length; j++) {
    if (data[j] === BEL) return j
    if (data[j] === ESC && j + 1 < data.length && data[j + 1] === ST_BACKSLASH) return j
    // An ESC not followed by \ inside the payload should not occur in valid
    // kitty commands (payload is base64/control ascii); ignore it as content.
  }
  return -1
}

function latin1(data: Uint8Array, start: number, end: number): string {
  let s = ''
  for (let j = start; j < end; j++) s += String.fromCharCode(data[j])
  return s
}

function concat(a: Uint8Array, b: Uint8Array): Uint8Array {
  const out = new Uint8Array(a.length + b.length)
  out.set(a, 0)
  out.set(b, a.length)
  return out
}

export class KittyGraphics {
  private term: Terminal
  private sendResponse: (bytes: Uint8Array) => void

  private canvas: HTMLCanvasElement | null = null
  private ctx: CanvasRenderingContext2D | null = null
  private screenEl: HTMLElement | null = null
  private resizeObserver: ResizeObserver | null = null
  private disposables: IDisposable[] = []
  private rafPending = false

  private images = new Map<number, StoredImage>()
  private placements: Placement[] = []
  // Virtual placements for Unicode placeholders: image id -> grid size (cols x rows).
  private virtualPlacements = new Map<number, { cols: number; rows: number }>()
  private carry: Uint8Array | null = null
  private pending: PendingChunks | null = null

  constructor(term: Terminal, opts: KittyGraphicsOptions) {
    this.term = term
    this.sendResponse = opts.sendResponse
  }

  /** Attach the overlay canvas to the terminal DOM and hook redraw triggers. */
  attach(): void {
    const root = this.term.element
    if (!root) return
    const screen = root.querySelector('.xterm-screen') as HTMLElement | null
    if (!screen) return

    // Re-attaching (rehost/reopen): move the existing canvas into the new root.
    if (!this.canvas) {
      const canvas = document.createElement('canvas')
      canvas.className = 'xterm-kitty-overlay'
      canvas.style.position = 'absolute'
      canvas.style.left = '0'
      canvas.style.top = '0'
      canvas.style.pointerEvents = 'none'
      canvas.style.zIndex = '4'
      this.canvas = canvas
      this.ctx = canvas.getContext('2d')
    }
    this.screenEl = screen
    if (this.canvas.parentElement !== root) root.appendChild(this.canvas)

    // Redraw on scroll/render/resize.
    this.clearHooks()
    this.disposables.push(this.term.onScroll(() => this.scheduleRedraw()))
    this.disposables.push(this.term.onRender(() => this.scheduleRedraw()))
    this.disposables.push(this.term.onResize(() => this.scheduleRedraw()))
    this.resizeObserver = new ResizeObserver(() => this.scheduleRedraw())
    this.resizeObserver.observe(screen)
    this.scheduleRedraw()
  }

  /** Clear all images/placements (on terminal reset / replay start). */
  reset(): void {
    this.images.clear()
    this.placements = []
    this.virtualPlacements.clear()
    this.carry = null
    this.pending = null
    this.scheduleRedraw()
  }

  dispose(): void {
    this.clearHooks()
    this.canvas?.remove()
    this.canvas = null
    this.ctx = null
    this.images.clear()
    this.placements = []
  }

  private clearHooks(): void {
    for (const d of this.disposables) {
      try { d.dispose() } catch { /* ignored */ }
    }
    this.disposables = []
    if (this.resizeObserver) {
      try { this.resizeObserver.disconnect() } catch { /* ignored */ }
      this.resizeObserver = null
    }
  }

  /**
   * Write server output to the terminal, intercepting kitty graphics commands.
   * Fast-path: when the data contains no APC start and no chunk is pending, it
   * is written directly. Otherwise the stream is split so graphics commands are
   * handled at the correct cursor position (after preceding text is parsed).
   */
  write(data: Uint8Array | string, done: () => void): void {
    const bytes = toU8(data)
    if (!this.carry && !this.pending && !containsApcStart(bytes)) {
      this.term.write(bytes, done)
      return
    }

    const { segments, carry } = scanStream(bytes, this.carry)
    this.carry = carry

    let idx = 0
    const step = (): void => {
      while (idx < segments.length) {
        const seg = segments[idx++]
        if (seg.kind === 'text') {
          this.term.write(seg.bytes, step)
          return
        }
        // Graphics command: preceding text is now parsed, cursor is accurate.
        const advance = this.handleCommand(seg.control, seg.payloadB64)
        if (advance) {
          this.term.write(advance, step)
          return
        }
      }
      done()
    }
    step()
  }

  // Handle one complete-or-partial graphics command. Returns cursor-advance
  // bytes to write (or null). Image decoding happens asynchronously.
  private handleCommand(control: Map<string, string>, payloadB64: string): Uint8Array | null {
    // Chunk assembly: a command with m=1 continues in later escapes.
    if (this.pending) {
      this.pending.payload += payloadB64
      if (control.get('m') === '1') return null
      const full = this.pending
      this.pending = null
      return this.dispatch(full.control, full.payload)
    }
    if (control.get('m') === '1') {
      this.pending = { control, payload: payloadB64 }
      return null
    }
    return this.dispatch(control, payloadB64)
  }

  private dispatch(control: Map<string, string>, payloadB64: string): Uint8Array | null {
    const action = control.get('a') ?? 't'
    switch (action) {
      case 'q':
        // Query / capability probe. Only advertise direct transmission: replying
        // OK to a file/temp/shared-memory query would make the client send frames
        // the browser cannot read. An error makes well-behaved clients (e.g.
        // tode) fall back to inline direct transmission.
        if (this.mediumSupported(control)) this.respond(control, 'OK')
        else this.respond(control, 'ENOTSUPP:only direct transmission is supported')
        return null
      case 'd':
        this.handleDelete(control)
        return null
      case 't':
        this.transmit(control, payloadB64)
        return null
      case 'T':
      case 'p':
        return this.transmitAndDisplay(control, payloadB64, action)
      default:
        // Animation / compose: not supported. Acknowledge quietly.
        return null
    }
  }

  private mediumSupported(control: Map<string, string>): boolean {
    const t = control.get('t')
    // Default medium is direct (d). Anything else references server-side data.
    return t === undefined || t === 'd'
  }

  private transmit(control: Map<string, string>, payloadB64: string): void {
    if (!this.mediumSupported(control)) {
      this.respond(control, 'ENOTSUPP:only direct transmission is supported')
      return
    }
    this.decodeAndStore(control, payloadB64)
    this.respond(control, 'OK')
  }

  private transmitAndDisplay(
    control: Map<string, string>,
    payloadB64: string,
    action: string,
  ): Uint8Array | null {
    const key = imageKey(control)

    // Virtual placement for a Unicode placeholder: transmit the image data (if
    // any) and record the grid size, but do NOT draw at the cursor or advance
    // it. The image is rendered later wherever U+10EEEE placeholder cells with
    // matching id/row/col diacritics appear in the buffer.
    if (num(control, 'U', 0) === 1) {
      if (action === 'T') {
        if (!this.mediumSupported(control)) {
          this.respond(control, 'ENOTSUPP:only direct transmission is supported')
          return null
        }
        this.decodeAndStore(control, payloadB64)
      }
      const cols = num(control, 'c', 0)
      const rows = num(control, 'r', 0)
      if (cols > 0 && rows > 0) this.virtualPlacements.set(key, { cols, rows })
      this.respond(control, 'OK')
      this.scheduleRedraw()
      return null
    }

    if (action === 'T') {
      if (!this.mediumSupported(control)) {
        this.respond(control, 'ENOTSUPP:only direct transmission is supported')
        return null
      }
      this.decodeAndStore(control, payloadB64)
    }
    // Compute placement size. cols/rows must be known synchronously for the
    // cursor advance; fall back to raw pixel dims (s/v) when present.
    const { cellW, cellH } = this.cellSize()
    let cols = num(control, 'c', 0)
    let rows = num(control, 'r', 0)
    const sPx = num(control, 's', 0)
    const vPx = num(control, 'v', 0)
    const known = this.images.get(key)
    const natW = sPx || known?.width || 0
    const natH = vPx || known?.height || 0
    if (cols <= 0 || rows <= 0) {
      if (cols <= 0 && rows <= 0) {
        cols = natW && cellW ? Math.max(1, Math.ceil(natW / cellW)) : 0
        rows = natH && cellH ? Math.max(1, Math.ceil(natH / cellH)) : 0
      } else if (cols <= 0) {
        // Preserve aspect from rows.
        cols = natW && natH ? Math.max(1, Math.round((rows * cellH * natW) / natH / cellW)) : 0
      } else {
        rows = natW && natH ? Math.max(1, Math.round((cols * cellW * natH) / natW / cellH)) : 0
      }
    }

    const buf = this.term.buffer.active
    const col = buf.cursorX
    const absRow = buf.baseY + buf.cursorY
    this.placements.push({ key, absRow, col, cols, rows })
    this.scheduleRedraw()

    this.respond(control, 'OK')

    // Cursor movement policy: C=1 means do not move.
    if (num(control, 'C', 0) === 1) return null
    if (cols <= 0 && rows <= 0) return null
    let seq = ''
    if (rows > 0) seq += `\x1b[${rows}B`
    if (cols > 0) seq += `\x1b[${cols}C`
    return seq ? encoder.encode(seq) : null
  }

  private decodeAndStore(control: Map<string, string>, payloadB64: string): void {
    const key = imageKey(control)
    const compressed = control.get('o') === 'z'
    if (control.has('o') && !compressed) {
      // Only zlib (o=z) compression is supported.
      this.respond(control, 'ENOTSUPP:unsupported compression')
      return
    }
    const format = num(control, 'f', 32)
    let raw: Uint8Array
    try {
      raw = base64ToBytes(payloadB64)
    } catch {
      return
    }

    const store = (bitmap: ImageBitmap, w: number, h: number): void => {
      const existing = this.images.get(key)
      if (existing?.bitmap && existing.bitmap !== bitmap) {
        try { existing.bitmap.close() } catch { /* ignored */ }
      }
      this.images.set(key, { bitmap, width: w, height: h })
      this.scheduleRedraw()
    }

    // Reserve the slot so placement geometry can read dims once known.
    if (!this.images.has(key)) this.images.set(key, { bitmap: null, width: 0, height: 0 })

    const proceed = (bytes: Uint8Array): void => {
      if (format === 100) {
        // PNG (or other browser-decodable format).
        const blob = new Blob([bytes.buffer as ArrayBuffer], { type: 'image/png' })
        createImageBitmap(blob)
          .then((bmp) => store(bmp, bmp.width, bmp.height))
          .catch(() => { /* ignore decode failure */ })
        return
      }

      // Raw pixel data: f=24 (RGB) or f=32 (RGBA).
      const w = num(control, 's', 0)
      const h = num(control, 'v', 0)
      if (w <= 0 || h <= 0) return
      const rgba = new Uint8ClampedArray(w * h * 4)
      if (format === 24) {
        for (let p = 0, q = 0; q < rgba.length; p += 3, q += 4) {
          rgba[q] = bytes[p] ?? 0
          rgba[q + 1] = bytes[p + 1] ?? 0
          rgba[q + 2] = bytes[p + 2] ?? 0
          rgba[q + 3] = 255
        }
      } else {
        rgba.set(bytes.subarray(0, Math.min(bytes.length, rgba.length)))
      }
      this.images.set(key, { bitmap: null, width: w, height: h })
      const imageData = new ImageData(rgba, w, h)
      createImageBitmap(imageData)
        .then((bmp) => store(bmp, w, h))
        .catch(() => { /* ignore */ })
    }

    if (compressed) {
      inflateZlib(raw)
        .then(proceed)
        .catch(() => { /* ignore inflate failure */ })
    } else {
      proceed(raw)
    }
  }

  private handleDelete(control: Map<string, string>): void {
    const d = control.get('d') ?? 'a'
    const free = d === d.toUpperCase()
    const lower = d.toLowerCase()
    switch (lower) {
      case 'a': // all placements
        this.placements = []
        if (free) this.freeAllImages()
        break
      case 'i': { // by image id
        const key = imageKey(control)
        this.placements = this.placements.filter((p) => p.key !== key)
        if (free) this.freeImage(key)
        break
      }
      case 'n': { // by image number
        const key = -num(control, 'I', 0)
        this.placements = this.placements.filter((p) => p.key !== key)
        if (free) this.freeImage(key)
        break
      }
      case 'c': { // intersecting cursor
        const buf = this.term.buffer.active
        const row = buf.baseY + buf.cursorY
        const cx = buf.cursorX
        this.placements = this.placements.filter(
          (p) => !(row >= p.absRow && row < p.absRow + p.rows && cx >= p.col && cx < p.col + p.cols),
        )
        break
      }
      default:
        this.placements = []
    }
    this.scheduleRedraw()
  }

  private freeImage(key: number): void {
    const img = this.images.get(key)
    if (img?.bitmap) { try { img.bitmap.close() } catch { /* ignored */ } }
    this.images.delete(key)
  }

  private freeAllImages(): void {
    for (const img of this.images.values()) {
      if (img.bitmap) { try { img.bitmap.close() } catch { /* ignored */ } }
    }
    this.images.clear()
  }

  // Build and send a protocol response, respecting the quiet level q.
  private respond(control: Map<string, string>, message: string): void {
    const q = num(control, 'q', 0)
    const ok = message === 'OK'
    if (ok && q >= 1) return
    if (!ok && q >= 2) return
    const i = num(control, 'i', 0)
    const I = num(control, 'I', 0)
    let head = ''
    if (i > 0) head += `i=${i}`
    if (I > 0) head += `${head ? ',' : ''}I=${I}`
    const seq = `\x1b_G${head};${message}\x1b\\`
    try { this.sendResponse(encoder.encode(seq)) } catch { /* ignored */ }
  }

  private cellSize(): { cellW: number; cellH: number } {
    const el = this.screenEl
    const cols = this.term.cols || 1
    const rows = this.term.rows || 1
    if (!el || !el.clientWidth || !el.clientHeight) return { cellW: 0, cellH: 0 }
    return { cellW: el.clientWidth / cols, cellH: el.clientHeight / rows }
  }

  private scheduleRedraw(): void {
    if (this.rafPending) return
    this.rafPending = true
    requestAnimationFrame(() => {
      this.rafPending = false
      this.redraw()
    })
  }

  private redraw(): void {
    const canvas = this.canvas
    const ctx = this.ctx
    const el = this.screenEl
    if (!canvas || !ctx || !el) return

    const cssW = el.clientWidth
    const cssH = el.clientHeight
    const dpr = window.devicePixelRatio || 1
    if (canvas.width !== Math.round(cssW * dpr) || canvas.height !== Math.round(cssH * dpr)) {
      canvas.width = Math.round(cssW * dpr)
      canvas.height = Math.round(cssH * dpr)
    }
    canvas.style.width = `${cssW}px`
    canvas.style.height = `${cssH}px`

    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, cssW, cssH)
    if (this.placements.length === 0) return

    const cols = this.term.cols || 1
    const rows = this.term.rows || 1
    const cellW = cssW / cols
    const cellH = cssH / rows
    const viewportTop = this.term.buffer.active.viewportY

    for (const p of this.placements) {
      const img = this.images.get(p.key)
      if (!img?.bitmap) continue
      // Fall back to the decoded natural size when no explicit cols/rows were
      // known at dispatch time (e.g. PNG without r=/c=), so the image still
      // renders at its true size instead of collapsing to 0x0.
      const pCols = p.cols > 0 ? p.cols : Math.max(1, Math.ceil(img.width / cellW))
      const pRows = p.rows > 0 ? p.rows : Math.max(1, Math.ceil(img.height / cellH))
      const screenRow = p.absRow - viewportTop
      const y = screenRow * cellH
      const h = pRows * cellH
      if (y + h <= 0 || y >= cssH) continue // off-screen vertically
      const x = p.col * cellW
      const w = pCols * cellW
      try {
        ctx.drawImage(img.bitmap, x, y, w, h)
      } catch { /* ignored */ }
    }

    if (this.virtualPlacements.size > 0) {
      this.drawPlaceholders(ctx, cellW, cellH, viewportTop)
    }
  }

  // Render Unicode placeholder cells (U+10EEEE) by drawing the matching image
  // sub-rectangle into each cell, using the id (from the cell foreground color)
  // and the row/column diacritics.
  private drawPlaceholders(
    ctx: CanvasRenderingContext2D,
    cellW: number,
    cellH: number,
    viewportTop: number,
  ): void {
    const rows = this.term.rows
    const cols = this.term.cols
    const buf = this.term.buffer.active
    for (let r = 0; r < rows; r++) {
      const line = buf.getLine(viewportTop + r)
      if (!line) continue
      let prev: { id: number; row: number; col: number } | null = null
      for (let c = 0; c < cols; c++) {
        const cell = line.getCell(c)
        if (!cell) { prev = null; continue }
        const chars = cell.getChars()
        if (!chars || chars.codePointAt(0) !== PLACEHOLDER_CODEPOINT) { prev = null; continue }

        // Image id is encoded in the cell foreground color.
        let id: number
        if (cell.isFgRGB()) id = cell.getFgColor() & 0xffffff
        else if (cell.isFgPalette()) id = cell.getFgColor()
        else { prev = null; continue }

        const cps = [...chars]
        const d1 = cps[1] ? diacriticValue(cps[1].codePointAt(0)!) : -1
        const d2 = cps[2] ? diacriticValue(cps[2].codePointAt(0)!) : -1
        const d3 = cps[3] ? diacriticValue(cps[3].codePointAt(0)!) : -1
        if (d3 >= 0) id = (id & 0xffffff) | (d3 << 24)

        const row: number = d1 >= 0 ? d1 : (prev && prev.id === id ? prev.row : 0)
        const col: number = d2 >= 0 ? d2 : (prev && prev.id === id ? prev.col + 1 : 0)
        prev = { id, row, col }

        const vp = this.virtualPlacements.get(id)
        const img = this.images.get(id)
        if (!vp || !img?.bitmap) continue
        const sw = img.width / vp.cols
        const sh = img.height / vp.rows
        const sx = col * sw
        const sy = row * sh
        if (sx >= img.width || sy >= img.height) continue
        try {
          ctx.drawImage(img.bitmap, sx, sy, sw, sh, c * cellW, r * cellH, cellW, cellH)
        } catch { /* ignored */ }
      }
    }
  }
}

function containsApcStart(data: Uint8Array): boolean {
  for (let i = 0; i + 2 < data.length; i++) {
    if (data[i] === ESC && data[i + 1] === UNDERSCORE && data[i + 2] === G) return true
  }
  // Also treat a trailing ESC / ESC_ as a possible start so we don't miss a
  // command split across writes.
  const n = data.length
  if (n >= 1 && data[n - 1] === ESC) return true
  if (n >= 2 && data[n - 2] === ESC && data[n - 1] === UNDERSCORE) return true
  return false
}
