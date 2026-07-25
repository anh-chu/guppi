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
  constructor(private terminal: Terminal, private onOpen: OpenFileHandler) {}

  provideLinks(row: number, callback: (links: ILink[] | undefined) => void): void {
    const { text: lineText } = collectWrappedLine(this.terminal, row)
    if (!lineText.trim()) { callback(undefined); return }

    const links: ILink[] = []
    const tokens = lineText.split(/\s+/)
    let pos = 0

    for (const token of tokens) {
      const analysis = analyzeToken(token)
      if (analysis) {
        const globalStart = pos + analysis.start
        const span = findSpanInBuffer(this.terminal, row, globalStart, analysis.length)
        if (span) {
          const match = analysis.match
          links.push({
            range: {
              start: { x: span.startX + 1, y: span.startY + 1 },
              end:   { x: span.endX + 1,   y: span.endY + 1 },
            },
            text: token.slice(analysis.start, analysis.start + analysis.length),
            activate: () => this.onOpen(match),
            hover: (event, text) => {
              if (event?.target instanceof HTMLElement) {
                event.target.style.cursor = 'pointer'
                event.target.style.textDecoration = 'underline'
              }
            },
            leave: (event) => {
              if (event?.target instanceof HTMLElement) {
                event.target.style.cursor = ''
                event.target.style.textDecoration = ''
              }
            },
          })
        }
      }
      // advance past token + whitespace separator
      pos += token.length
      const nextNonSpace = lineText.slice(pos).search(/\S/)
      if (nextNonSpace === -1) break
      pos += nextNonSpace + 1
    }
    callback(links.length ? links : undefined)
  }
}

export function createFileLinkProvider(terminal: Terminal, onOpen: OpenFileHandler): ILinkProvider {
  return new FileLinkProviderImpl(terminal, onOpen)
}
