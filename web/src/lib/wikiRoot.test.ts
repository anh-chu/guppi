import { describe, it, expect } from 'vitest'
import { pickEmbedRoot, resolveFilePath, buildWikiSrc } from '../lib/wikiRoot'

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

// Terminal output is full of relative paths; wiki-viewer's navigateToPath treats
// a non-absolute path as ALREADY root-relative, so an unresolved relative path is
// reinterpreted against whatever root we sent. Measured against a live instance:
//   relative + no root         -> {"error":"File not found"}  (searched its own workspace)
//   relative + correct root    -> content
//   absolute + dirname root    -> content
// This is why the detected-files list worked while terminal clicks did not: those
// paths come from the server and are always absolute, so a root was always
// derivable from the dirname.
describe('resolveFilePath: relative terminal paths', () => {
  const cwd = '/home/sil/seedwise/app'

  it('joins a BARE relative path against the cwd (the reported bug)', () => {
    expect(resolveFilePath('app/(public)/sign-in.tsx', cwd))
      .toBe('/home/sil/seedwise/app/app/(public)/sign-in.tsx')
  })

  it('joins a bare single-segment path', () => {
    expect(resolveFilePath('README.md', cwd)).toBe('/home/sil/seedwise/app/README.md')
  })

  it('normalizes ./ and ../ rather than passing the segments through', () => {
    expect(resolveFilePath('./src/a.ts', cwd)).toBe('/home/sil/seedwise/app/src/a.ts')
    expect(resolveFilePath('../other/b.ts', cwd)).toBe('/home/sil/seedwise/other/b.ts')
    expect(resolveFilePath('../../x.ts', cwd)).toBe('/home/sil/x.ts')
  })

  it('normalizes redundant segments inside an absolute path', () => {
    expect(resolveFilePath('/home/sil//guppi/./web/../README.md'))
      .toBe('/home/sil/guppi/README.md')
  })

  it('leaves a relative path alone when there is no cwd to resolve against', () => {
    // The panel turns this into an explicit 'unresolved-path' state rather than
    // loading rootless and letting wiki-viewer search the wrong workspace.
    expect(resolveFilePath('app/sign-in.tsx', undefined)).toBe('app/sign-in.tsx')
    expect(resolveFilePath('app/sign-in.tsx', '')).toBe('app/sign-in.tsx')
  })

  it('does not resolve against a cwd that is itself relative', () => {
    expect(resolveFilePath('a.ts', 'relative/cwd')).toBe('a.ts')
  })

  it('still refuses to invent $HOME for ~/', () => {
    expect(resolveFilePath('~/notes/a.md', cwd)).toBe('~/notes/a.md')
  })

  // The end-to-end invariant: given a cwd, a terminal path becomes absolute AND
  // the chosen root contains it. Both halves are needed for the panel to work.
  it('yields an absolute path inside the chosen root, for every terminal shape', () => {
    for (const raw of [
      'app/(public)/sign-in.tsx',
      './web/src/App.tsx',
      '../other/b.ts',
      '/home/sil/seedwise/app/app/x.tsx',
      '/home/sil/elsewhere/y.ts',
      'README.md',
    ]) {
      const resolved = resolveFilePath(raw, cwd)
      expect(resolved.startsWith('/'), `${raw} -> absolute`).toBe(true)
      const root = pickEmbedRoot(resolved, cwd)
      expect(root, `root for ${raw}`).toBeDefined()
      expect(resolved.startsWith(root! + '/'), `${resolved} inside ${root}`).toBe(true)
    }
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

describe('paths with .. segments', () => {
  const cwd = '/home/sil/guppi'

  it('normalizes relative paths that traverse above the cwd', () => {
    expect(resolveFilePath('../a.ts', cwd)).toBe('/home/sil/a.ts')
    expect(resolveFilePath('../../a.ts', cwd)).toBe('/home/a.ts')
    expect(resolveFilePath('../../../a.ts', cwd)).toBe('/a.ts')
  })

  it('normalizes absolute paths with redundant and traversing segments', () => {
    expect(resolveFilePath('/home/sil/guppi/src/../a.ts')).toBe('/home/sil/guppi/a.ts')
    expect(resolveFilePath('/home/sil/guppi/../../a.ts')).toBe('/home/a.ts')
    expect(resolveFilePath('/etc/../a.ts')).toBe('/a.ts')
  })

  it('keeps the resolved file inside the chosen root when .. changes directories', () => {
    const cases: [string, string][] = [
      ['../seedwise/a.ts', '/home/sil/seedwise'],
      ['./../other/b.ts', '/home/sil/other'],
      ['../../elsewhere/c.ts', '/home/elsewhere'],
    ]
    for (const [raw, expectedRoot] of cases) {
      const resolved = resolveFilePath(raw, cwd)
      const root = pickEmbedRoot(resolved, cwd)
      expect(root, `root for ${raw}`).toBe(expectedRoot)
      expect(resolved.startsWith(root! + '/'), `${resolved} inside ${root}`).toBe(true)
    }
  })

  it('falls back above the cwd when .. traverses outside it', () => {
    const resolved = resolveFilePath('../../secrets/key.txt', cwd)
    expect(resolved).toBe('/home/secrets/key.txt')
    expect(pickEmbedRoot(resolved, cwd)).toBe('/home/secrets')
  })
})

