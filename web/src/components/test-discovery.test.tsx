// @vitest-environment jsdom
import { render, screen } from '@testing-library/react'
import { expect, test } from 'vitest'

function Sentinel() {
  return <div data-testid="sentinel">discovered</div>
}

test('discovers and renders a tsx test', () => {
  render(<Sentinel />)
  expect(screen.getByTestId('sentinel').textContent).toBe('discovered')
})
