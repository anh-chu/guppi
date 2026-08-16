import type { ILink, ILinkProvider, Terminal } from '@xterm/xterm'

export interface FileLinkMatch {
  path: string
  line: number | null
  column: number | null
}

type OpenFileHandler = (match: FileLinkMatch) => void

const MAX_WRAPPED_ROWS = 24
const OPEN_WRAPPERS = new Set(['"', "'", '`', '(', '[', '{', '<'])
const TRAILING_PUNCTUATION = new Set(['"', "'", '`', ')', ']', '}', '>', ',', ';', '!', '?', '.'])
const LINE_COLUMN_SUFFIX = /((?::\d+){1,2})$/
const LAST_SEGMENT_HAS_EXTENSION = /\.[A-Za-z0-9]+$/

interface TokenAnalysis {
  start: number
  length: number
  match: FileLinkMatch
}

// Conservative: bare relative tokens require extension+line, or 2+ slashes, or trailing slash.
// Anchored paths (/, ~/, ./, ../) always qualify. URLs are skipped (WebLinksAddon handles them).
export function analyzeToken(token: string): TokenAnalysis | null {
  let start = 0
  let end = token.length

  while (start < end && OPEN_WRAPPERS.has(token[start])) start++
  while (end > start && TRAILING_PUNCTUATION.has(token[end - 1])) end--
  if (end - start < 2) return null

  const candidate = token.slice(start, end)
  if (candidate.includes('://')) return null

  let core = candidate
  let line: number | null = null
  let column: number | null = null
  const suffixMatch = LINE_COLUMN_SUFFIX.exec(candidate)
  if (suffixMatch) {
    core = candidate.slice(0, suffixMatch.index)
    const parts = suffixMatch[1].split(':')
    line = Number.parseInt(parts[1], 10) || null
    column = parts[2] ? Number.parseInt(parts[2], 10) || null : null
  }
  while (core.endsWith(':')) core = core.slice(0, -1)
  if (core.length < 2) return null

  const isAnchored =
    core.startsWith('/') || core.startsWith('~/') ||
    core.startsWith('./') || core.startsWith('../')

  if (core.includes('/')) {
    if (!isAnchored) {
      const lastSegment = core.slice(core.lastIndexOf('/') + 1)
      const hasExtension = LAST_SEGMENT_HAS_EXTENSION.test(lastSegment)
      const endsWithSlash = core.endsWith('/')
      const slashCount = (core.match(/\//g) ?? []).length
      if (!hasExtension && !endsWithSlash && slashCount < 2) return null
    }
  } else {
    if (line === null || !LAST_SEGMENT_HAS_EXTENSION.test(core)) return null
  }
  if (core === '/' || core === '~/') return null

  return {
    start,
    length: core.length + (suffixMatch ? candidate.length - suffixMatch.index : 0),
    match: { path: core, line, column },
  }
}

interface CellSpan { startX: number; startY: number; endX: number; endY: number }

function collectWrappedLine(terminal: Terminal, row: number): { text: string; startRow: number } {
  let startRow = row
  while (startRow > 0 && terminal.buffer.active.getLine(startRow - 1)?.isWrapped) {
    startRow--
  }
  const parts: string[] = []
  for (let r = startRow; ; r++) {
    const line = terminal.buffer.active.getLine(r)
    if (!line) break
    parts.push(line.translateToString(true))
    if (r >= startRow + MAX_WRAPPED_ROWS) break
    const next = terminal.buffer.active.getLine(r + 1)
    if (!next?.isWrapped) break
  }
  return { text: parts.join(''), startRow }
}

function findSpanInBuffer(
  terminal: Terminal,
  row: number,
  tokenStart: number,
  tokenLength: number,
): CellSpan | null {
  const { text: lineText, startRow } = collectWrappedLine(terminal, row)
  const cols = terminal.cols

  let charIdx = 0
  let cellIdx = 0
  let startX = -1
  let startY = -1

  for (let r = startRow; ; r++) {
    const line = terminal.buffer.active.getLine(r)
    if (!line) break
    for (let x = 0; x < cols; x++) {
      if (charIdx === tokenStart) { startX = x; startY = r }
      if (charIdx === tokenStart + tokenLength - 1) {
        return startX !== -1 ? { startX, startY, endX: x + 1, endY: r } : null
      }
      const cell = line.getCell(x)
      if (cell && cell.getCode() !== 0) charIdx++
      cellIdx++
    }
    const next = terminal.buffer.active.getLine(r + 1)
    if (!next?.isWrapped) break
  }
  return null
}

class FileLinkProviderImpl implements ILinkProvider {
  private requestId = 0

  constructor(
    private terminal: Terminal,
    private onOpen: OpenFileHandler,
    private checkExists?: (path: string) => Promise<boolean>,
  ) {}

  provideLinks(bufferLineNumber: number, callback: (links: ILink[] | undefined) => void): void {
    const requestId = ++this.requestId
    // xterm passes a 1-based buffer line number, while Buffer.getLine() is
    // 0-based. WebLinksAddon does the same conversion (computeLink calls
    // _getWindowedLineStrings(y - 1)). Without it we scan the line BELOW the
    // one the pointer is on, so links land on the wrong row and paths appear
    // unclickable.
    const row = bufferLineNumber - 1
    if (row < 0) { callback(undefined); return }
    const { text: lineText } = collectWrappedLine(this.terminal, row)
    if (!lineText.trim()) { callback(undefined); return }

    const candidates: Array<{ link: ILink; path: string }> = []
    const tokens = lineText.split(/\s+/)
    let searchFrom = 0

    for (const token of tokens) {
      if (!token) { searchFrom++; continue }
      const tokenPos = lineText.indexOf(token, searchFrom)
      if (tokenPos === -1) break
      searchFrom = tokenPos + token.length

      const analysis = analyzeToken(token)
      if (!analysis) continue

      const globalStart = tokenPos + analysis.start
      const span = findSpanInBuffer(this.terminal, row, globalStart, analysis.length)
      if (span) {
        const match = analysis.match
        candidates.push({
          path: match.path,
          link: {
            range: {
              start: { x: span.startX + 1, y: span.startY + 1 },
              // endX is already one past the last char (0-based exclusive),
              // which equals the 1-based inclusive column of the last char.
              end:   { x: span.endX,      y: span.endY + 1 },
            },
            text: token.slice(analysis.start, analysis.start + analysis.length),
            activate: (event) => {
              if (event.ctrlKey || event.metaKey) this.onOpen(match)
            },
            hover: (event) => {
              if (event?.target instanceof HTMLElement) {
                event.target.style.cursor = 'pointer'
                event.target.style.textDecoration = 'underline'
                event.target.title = 'Cmd/Ctrl-click to open file'
              }
            },
            leave: (event) => {
              if (event?.target instanceof HTMLElement) {
                event.target.style.cursor = ''
                event.target.style.textDecoration = ''
                event.target.title = ''
              }
            },
          },
        })
      }
    }

    if (!this.checkExists) {
      const links = candidates.map((c) => c.link)
      callback(links.length ? links : undefined)
      return
    }

    // Existence check is async (server round trip, cached client-side), so
    // the callback fires once every candidate on this line has resolved.
    // Fails open per-candidate: a rejected check keeps the link rather than
    // hiding a path we simply failed to confirm.
    const checkExists = this.checkExists
    Promise.all(candidates.map((c) => checkExists(c.path).catch(() => true))).then((results) => {
      // xterm does not identify async provider replies. Ignore replies for an
      // older hover request so a slow existence check cannot underline another row.
      if (requestId !== this.requestId) return
      const links = candidates.filter((_, i) => results[i]).map((c) => c.link)
      callback(links.length ? links : undefined)
    })
  }
}

export function createFileLinkProvider(
  terminal: Terminal,
  onOpen: OpenFileHandler,
  checkExists?: (path: string) => Promise<boolean>,
): ILinkProvider {
  return new FileLinkProviderImpl(terminal, onOpen, checkExists)
}
