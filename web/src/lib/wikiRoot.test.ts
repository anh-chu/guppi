import { describe, it, expect } from 'vitest'
import { pickEmbedRoot, resolveFilePath } from '../components/WikiPanel'

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
})

describe('resolveFilePath', () => {
  it('passes absolute paths through', () => {
    expect(resolveFilePath('/a/b.ts', '/cwd')).toBe('/a/b.ts')
  })

  it('joins ./ and ../ against the cwd', () => {
    expect(resolveFilePath('./src/a.ts', '/home/sil/guppi')).toBe('/home/sil/guppi/src/a.ts')
  })

  it('does not expand ~, which only a shell can resolve', () => {
    expect(resolveFilePath('~/a.md', '/home/sil/guppi')).toBe('~/a.md')
  })

  // Regression guard for the combination that produced the bug: a relative path
  // resolved against the cwd is by definition inside it, so the cwd stays the root.
  it('a relative path stays rooted at the cwd end to end', () => {
    const cwd = '/home/sil/guppi'
    const resolved = resolveFilePath('./web/src/App.tsx', cwd)
    expect(pickEmbedRoot(resolved, cwd)).toBe(cwd)
  })
})
