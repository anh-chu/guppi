/**
 * The ONE place termyard touches @wiki-viewer/viewer.
 *
 * Everything about how a file looks lives in that package, on purpose: markdown,
 * syntax highlighting and CSV rendering are its job, and duplicating any of it
 * here is how two renderers drift apart. A change to the package's contract must
 * break this file and only this file.
 *
 * The contract:
 *   fileKindFor(filename) -> FileKind
 *   <FileViewer kind content filename assetUrl />
 *
 * Text kinds get `content`, asset kinds get `assetUrl`. See lib/fileSource.ts.
 *
 * BUILD REQUIREMENT, and it is not shippable as-is. package.json depends on this
 * package as `file:../../wiki-viewer/packages/viewer`, which only resolves when
 * wiki-viewer is checked out as a SIBLING of guppi. On any other machine, and in
 * CI, `npm install` cannot find it. The dependency must become a published
 * version, or a git URL pinned to a SHA, before termyard is released.
 *
 * A fresh clone also needs the package BUILT, because this import resolves to its
 * dist/ through the package's exports map, and npm does not reliably run `prepare`
 * for a file: dependency. If the build fails on an unresolved import, build the
 * package first.
 *
 * Do NOT record this note as a "//"-prefixed key in package.json's dependencies:
 * npm parses every key there as a package name, so it fails the whole install
 * with EINVALIDPACKAGENAME rather than being ignored as a comment.
 */
import { fileKindFor, FileViewer } from '@wiki-viewer/viewer'
import '@wiki-viewer/viewer/styles.css'

import type { FileKind } from '../lib/fileSource'

/**
 * Wrapping instead of re-exporting is what makes the contract enforced rather
 * than assumed: the return annotation fails to compile if the package ever
 * returns a kind lib/fileSource.ts does not model, which is the direction that
 * matters, since an unmodelled kind would fall through every branch and render
 * a blank pane.
 */
export function fileKindOf(filename: string): FileKind {
  return fileKindFor(filename)
}

export interface MountedFileViewerProps {
  kind: FileKind
  filename: string
  content?: string
  assetUrl?: string
}

/**
 * The viewer's root uses flex-1, so it needs a flex parent with a real height.
 * Given a parent without one it collapses to zero height and renders a blank
 * area with a clean console and no errors at all. That wrapper is not
 * decoration; it is the difference between working and silently invisible.
 */
export function MountedFileViewer({ kind, filename, content, assetUrl }: MountedFileViewerProps) {
  return (
    <div className="flex flex-col flex-1 min-h-0 min-w-0 overflow-hidden">
      {/* content is a required prop on FileViewer, and asset kinds have none.
          '' rather than undefined keeps that explicit at the boundary instead of
          relying on the package tolerating a missing string. */}
      <FileViewer kind={kind} content={content ?? ''} filename={filename} assetUrl={assetUrl} />
    </div>
  )
}
