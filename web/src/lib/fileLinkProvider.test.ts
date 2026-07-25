import { describe, it, expect } from 'vitest'
import { analyzeToken, createFileLinkProvider } from './fileLinkProvider'
import type { ILink } from '@xterm/xterm'

describe('analyzeToken', () => {
  it('accepts anchored paths', () => {
    for (const t of ['/home/sil/main.go', '~/notes.md', './src/a.ts', '../b/c.ts']) {
      expect(analyzeToken(t), t).not.toBeNull()
    }
  })

  it('rejects bare tokens with no extension, no line, and few slashes', () => {
    for (const t of ['foo', 'foo/bar', 'hello', 'a']) {
      expect(analyzeToken(t), t).toBeNull()
    }
  })

  it('accepts bare relative paths that carry an extension', () => {
    expect(analyzeToken('src/main.go')).not.toBeNull()
  })

  it('accepts a bare filename only when a line number is present', () => {
    expect(analyzeToken('main.go')).toBeNull()
    const withLine = analyzeToken('main.go:42')
    expect(withLine).not.toBeNull()
    expect(withLine!.match.line).toBe(42)
  })

  it('skips URLs, which WebLinksAddon owns', () => {
    expect(analyzeToken('https://example.com/a.ts')).toBeNull()
  })

  it('parses line and column', () => {
    const m = analyzeToken('/a/b.ts:12:34')!.match
    expect(m.path).toBe('/a/b.ts')
    expect(m.line).toBe(12)
    expect(m.column).toBe(34)
  })

  it('strips wrappers and trailing punctuation', () => {
    expect(analyzeToken('"/a/b.ts"')!.match.path).toBe('/a/b.ts')
    expect(analyzeToken('(/a/b.ts),')!.match.path).toBe('/a/b.ts')
  })

  it('rejects bare root', () => {
    expect(analyzeToken('/')).toBeNull()
    expect(analyzeToken('~/')).toBeNull()
  })
})

/** Minimal Terminal stand-in: one unwrapped buffer line of the given text. */
function fakeTerminal(line: string, cols = 200) {
  const cells = [...line]
  return {
    cols,
    buffer: {
      active: {
        getLine(row: number) {
          if (row !== 0) return undefined
          return {
            isWrapped: false,
            translateToString: () => line,
            getCell: (x: number) =>
              x < cells.length
                ? { getCode: () => cells[x].codePointAt(0)! }
                : { getCode: () => 0 },
          }
        },
      },
    },
  } as never
}

function linksFor(line: string): ILink[] {
  const provider = createFileLinkProvider(fakeTerminal(line), () => {})
  let out: ILink[] = []
  provider.provideLinks(1, links => { out = links ?? [] })
  return out
}

describe('provideLinks token positions', () => {
  it('locates a single path at the correct columns', () => {
    const links = linksFor('/home/sil/main.go')
    expect(links).toHaveLength(1)
    // xterm ranges are 1-based and end is exclusive-of-cell+1
    expect(links[0].range.start.x).toBe(1)
    expect(links[0].text).toBe('/home/sil/main.go')
  })

  // This is the regression: position tracking used to advance by
  // nextNonSpace + 1, overshooting by one for every token after the first.
  it('locates the SECOND path correctly (off-by-one regression)', () => {
    const line = 'edit /home/sil/main.go now'
    const links = linksFor(line)
    expect(links).toHaveLength(1)
    // 'edit ' is 5 chars, so the path starts at index 5 -> 1-based column 6
    expect(links[0].range.start.x).toBe(6)
    expect(links[0].text).toBe('/home/sil/main.go')
  })

  it('handles multiple paths on one line', () => {
    const line = '/a/one.ts and /b/two.ts'
    const links = linksFor(line)
    expect(links).toHaveLength(2)
    expect(links[0].range.start.x).toBe(1)
    expect(links[0].text).toBe('/a/one.ts')
    // '/a/one.ts and ' is 14 chars -> starts at index 14 -> column 15
    expect(links[1].range.start.x).toBe(15)
    expect(links[1].text).toBe('/b/two.ts')
  })

  it('handles irregular and leading whitespace', () => {
    const line = '   wrote    /x/y.go   ok'
    const links = linksFor(line)
    expect(links).toHaveLength(1)
    expect(links[0].range.start.x).toBe(13) // index 12 -> column 13
    expect(links[0].text).toBe('/x/y.go')
  })

  it('activate reports the parsed path, line and column', () => {
    const seen: Array<{ path: string; line: number | null }> = []
    const provider = createFileLinkProvider(
      fakeTerminal('see /a/b.ts:99 here'),
      m => seen.push({ path: m.path, line: m.line }),
    )
    let links: ILink[] = []
    provider.provideLinks(1, l => { links = l ?? [] })
    expect(links).toHaveLength(1)
    // node test env has no MouseEvent; activate only forwards the parsed match
    links[0].activate(undefined as never, links[0].text)
    expect(seen).toEqual([{ path: '/a/b.ts', line: 99 }])
  })

  it('returns undefined for a blank line', () => {
    expect(linksFor('    ')).toHaveLength(0)
  })

  it('finds nothing in ordinary prose', () => {
    expect(linksFor('the quick brown fox jumped over')).toHaveLength(0)
  })
})
