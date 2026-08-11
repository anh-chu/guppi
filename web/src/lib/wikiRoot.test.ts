import { describe, it, expect } from 'vitest'
import { pickEmbedRoot, buildWikiSrc } from '../lib/wikiRoot'

// wiki-viewer relativizes ?file= against ?root= and answers {"error":"Invalid
// path"} when the file falls outside, which the panel can only show as its empty
// "select a file to view or edit" screen. So the one hard invariant is that the
// chosen root contains the file. Verified against a live instance: root
// /home/sil/guppi with a file in /home/sil/seedwise returns Invalid path, and
// the file's own directory returns content.
describe('pickEmbedRoot', () => {
  it('uses the session cwd when it contains the file', () => {
    expect(pickEmbedRoot('/home/sil/guppi/web/src/App.tsx', '/home/sil/guppi'))
      .toBe('/home/sil/guppi')
  })

  // The reported bug: a terminal in one repo printing a path from another.
  it('falls back to the file dir when the cwd does NOT contain the file', () => {
    expect(pickEmbedRoot('/home/sil/seedwise/app/app/(public)/sign-in.tsx', '/home/sil/guppi'))
      .toBe('/home/sil/seedwise/app/app/(public)')
  })

  it('never returns a root that fails to contain the file', () => {
    const cases: [string, string | undefined][] = [
      ['/home/sil/seedwise/a.ts', '/home/sil/guppi'],
      ['/var/log/syslog', '/home/sil'],
      ['/home/sil/guppi/x.go', '/home/sil/guppi'],
      ['/home/sil/a/b/c/d.txt', undefined],
    ]
    for (const [file, cwd] of cases) {
      const root = pickEmbedRoot(file, cwd)
      expect(root, `root for ${file}`).toBeDefined()
      expect(file.startsWith(root! + '/'), `${file} inside ${root}`).toBe(true)
    }
  })

  // Prefix siblings are why containment needs a path-boundary check rather than
  // a bare startsWith. The peer hit the same class server-side.
  it('does not treat a prefix sibling as inside the cwd', () => {
    expect(pickEmbedRoot('/home/sil/guppi-secrets/key.txt', '/home/sil/guppi'))
      .toBe('/home/sil/guppi-secrets')
  })

  it('treats the file dir itself as inside', () => {
    expect(pickEmbedRoot('/home/sil/guppi/a.ts', '/home/sil/guppi/')).toBe('/home/sil/guppi/')
  })

  it('accepts a cwd equal to the file path', () => {
    expect(pickEmbedRoot('/home/sil/guppi', '/home/sil/guppi')).toBe('/home/sil/guppi')
  })

  it('keeps the cwd for non-absolute paths it cannot reason about', () => {
    expect(pickEmbedRoot('~/notes/a.md', '/home/sil/guppi')).toBe('/home/sil/guppi')
    expect(pickEmbedRoot('README.md', '/home/sil/guppi')).toBe('/home/sil/guppi')
    expect(pickEmbedRoot(null, '/home/sil/guppi')).toBe('/home/sil/guppi')
  })

  it('prefers the cwd over rooting at / for a file directly in /', () => {
    expect(pickEmbedRoot('/passwd', '/home/sil/guppi')).toBe('/home/sil/guppi')
  })

  it('returns the file dir when there is no cwd at all', () => {
    expect(pickEmbedRoot('/home/sil/seedwise/a.ts', undefined)).toBe('/home/sil/seedwise')
  })

  // A remote grant materialises the file into a private temp dir whose root is
  // returned alongside the local path. The chooser must respect that root when
  // it differs from the session cwd.
  it('uses an explicit remote grant root when the resolved path is outside the session cwd', () => {
    expect(pickEmbedRoot('/tmp/grants/peer-abc/project/src/main.go', '/home/local/cwd'))
      .toBe('/tmp/grants/peer-abc/project/src')
  })

  // Local grants return {token, path} with no root. Resolver falls back to the
  // usual cwd/file-dir logic: cwd when it contains the file, file dir otherwise.
  it('falls back to the cwd for a local grant whose path is inside the session cwd', () => {
    expect(pickEmbedRoot('/home/local/cwd/src/main.go', '/home/local/cwd')).toBe('/home/local/cwd')
  })

  it('falls back to the file dir for a local grant whose path is outside the session cwd', () => {
    expect(pickEmbedRoot('/home/local/project/src/main.go', '/home/local/cwd'))
      .toBe('/home/local/project/src')
  })
})

describe('buildWikiSrc', () => {
  it('always sets embed=1 and chrome=1', () => {
    const src = buildWikiSrc({ root: '/home/sil/guppi' })
    expect(src).toContain('embed=1')
    expect(src).toContain('chrome=1')
  })

  it('always includes root', () => {
    expect(buildWikiSrc({ root: '/home/sil/guppi' })).toContain('root=%2Fhome%2Fsil%2Fguppi')
  })

  it('omits file when null', () => {
    const src = buildWikiSrc({ root: '/tmp', file: null })
    expect(src).not.toContain('file=')
  })

  it('omits file when undefined', () => {
    const src = buildWikiSrc({ root: '/tmp' })
    expect(src).not.toContain('file=')
  })

  it('includes file when present', () => {
    const src = buildWikiSrc({ root: '/tmp', file: '/tmp/readme.md' })
    expect(src).toContain('file=%2Ftmp%2Freadme.md')
  })

  it('URL-encodes path components', () => {
    const src = buildWikiSrc({ root: '/home/sil/my files', file: '/home/sil/my files/doc.txt' })
    expect(src).toContain('root=%2Fhome%2Fsil%2Fmy+files')
    expect(src).toContain('file=%2Fhome%2Fsil%2Fmy+files%2Fdoc.txt')
  })
})

