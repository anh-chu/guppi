import { describe, expect, it } from 'vitest'
import { PLACEHOLDER_CODEPOINT, diacriticValue } from './kittyDiacritics'

describe('kitty diacritics', () => {
  it('maps the first few diacritics to their indices', () => {
    expect(diacriticValue(0x0305)).toBe(0)
    expect(diacriticValue(0x030d)).toBe(1)
    expect(diacriticValue(0x030e)).toBe(2)
    expect(diacriticValue(0x0310)).toBe(3)
  })

  it('maps the last diacritic to index 296', () => {
    expect(diacriticValue(0x1d244)).toBe(296)
  })

  it('returns -1 for a non-diacritic codepoint', () => {
    expect(diacriticValue(0x41)).toBe(-1) // 'A'
    expect(diacriticValue(0x0300)).toBe(-1) // grave, deliberately excluded by kitty
  })

  it('exposes the placeholder base codepoint', () => {
    expect(PLACEHOLDER_CODEPOINT).toBe(0x10eeee)
  })
})
