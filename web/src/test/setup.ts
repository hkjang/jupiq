import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach, beforeEach, vi } from 'vitest'

// jsdom has no matchMedia, and antd's responsive observer calls it while
// rendering. It is installed as a plain function before every test because
// `vi.restoreAllMocks()` strips the implementation from a `vi.fn()`, which used
// to leave every test after the first one in a file rendering nothing.
function installMatchMedia() {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string) => ({
      matches: query.includes('min-width: 992px'),
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }),
  })
}

class ResizeObserverMock {
  observe() {}
  unobserve() {}
  disconnect() {}
}

installMatchMedia()
vi.stubGlobal('ResizeObserver', ResizeObserverMock)
vi.stubGlobal('scrollTo', () => {})

beforeEach(() => {
  installMatchMedia()
  vi.stubGlobal('ResizeObserver', ResizeObserverMock)
  vi.stubGlobal('scrollTo', () => {})
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})
