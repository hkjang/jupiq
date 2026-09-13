import type { OidcConfig } from '../types'

// sessionStorage, not localStorage: a silent attempt belongs to this tab's
// browsing session. A fresh tab tries again; a reload after a refusal does not.
const ATTEMPTED_KEY = 'jupiq.sso.silentAttempted'
const SIGNED_OUT_KEY = 'jupiq.sso.signedOut'

// Values the server puts in that parameter: none (silent attempt found no
// provider session), error (the provider declined for another reason) and
// limited (jupiq refused to start because the address exceeded its rate limit).
export const SSO_MARKER_PARAM = 'sso'

// Routes where a silent attempt must never start. The login page is the
// destination of a refusal, and the API/MCP/probe prefixes are not screens at
// all - only browser navigations to the SPA may redirect to the provider.
const excludedPathPrefixes = ['/login', '/api/', '/mcp', '/healthz', '/readyz', '/momento/']

function readFlag(key: string): boolean {
  try {
    return window.sessionStorage.getItem(key) === 'true'
  } catch {
    // Private modes and blocked site data throw. Reading that as "already
    // attempted" fails closed; reading it as "not yet" would start a loop.
    return true
  }
}

function writeFlag(key: string, value: boolean) {
  try {
    if (value) window.sessionStorage.setItem(key, 'true')
    else window.sessionStorage.removeItem(key)
  } catch {
    /* readFlag already fails closed, so there is nothing to recover here */
  }
}

/** Records that the user signed out on purpose, which suppresses auto-login. */
export function markSignedOut() {
  writeFlag(SIGNED_OUT_KEY, true)
  writeFlag(ATTEMPTED_KEY, true)
}

/** Lifts the suppression once a session exists again. */
export function clearSilentSsoState() {
  writeFlag(SIGNED_OUT_KEY, false)
  writeFlag(ATTEMPTED_KEY, false)
}

export function safeReturnTo(value: string): string {
  return value.startsWith('/') && !value.startsWith('//') && !value.startsWith('/\\') ? value : '/'
}

export function isSilentSsoExcludedPath(pathname: string): boolean {
  return excludedPathPrefixes.some((prefix) => pathname === prefix.replace(/\/$/, '') || pathname.startsWith(prefix))
}

/**
 * Decides whether to try signing in without showing the login screen.
 *
 * prompt=none either answers with a code immediately or comes back with
 * login_required. Trying again on that answer would bounce the browser between
 * the provider and this app forever, so the attempt runs at most once per tab
 * session, never right after a deliberate sign-out, and never when the address
 * already carries the refusal marker the callback appends.
 */
export function shouldAttemptSilentSso(oidc: OidcConfig, pathname: string, search: string): boolean {
  if (!oidc.enabled || !oidc.auto_login) return false
  if (isSilentSsoExcludedPath(pathname)) return false
  const marker = new URLSearchParams(search).get(SSO_MARKER_PARAM)
  if (marker === 'none' || marker === 'error' || marker === 'limited') return false
  if (readFlag(SIGNED_OUT_KEY)) return false
  if (readFlag(ATTEMPTED_KEY)) return false
  return true
}

/** Sends the whole tab to the provider for a silent attempt (no hidden iframe). */
export function beginSilentSso(returnTo: string, navigate: (url: string) => void = (url) => window.location.assign(url)) {
  writeFlag(ATTEMPTED_KEY, true)
  navigate(`/api/v1/auth/oidc/login?prompt=none&return_to=${encodeURIComponent(safeReturnTo(returnTo))}`)
}
