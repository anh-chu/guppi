/**
 * Feature flags. Default OFF; enable via build-time env var
 * (VITE_V2_STATE=1) or a runtime localStorage override for manual testing
 * without a rebuild (e.g. `localStorage.setItem('termyard.v2State', '1')`).
 */

const LOCAL_STORAGE_KEY = 'termyard.v2State'

export function isV2StateEnabled(): boolean {
  try {
    const override = window.localStorage.getItem(LOCAL_STORAGE_KEY)
    if (override === '1') return true
    if (override === '0') return false
  } catch {
    // localStorage unavailable (e.g. some test environments) -- fall through
    // to the build-time flag.
  }
  return (import.meta as any).env?.VITE_V2_STATE === '1'
}
