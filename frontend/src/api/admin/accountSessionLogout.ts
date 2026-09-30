import { apiClient } from '../client'
import type { TokenGuardReloginAccount } from './accountTokenGuard'

// The email and TOTP delimit the password; preserve its original bytes,
// including spaces, punctuation, and any embedded separator.
export function parseSessionLogoutText(raw: string): TokenGuardReloginAccount {
  const lines = raw.split(/\r\n|\r|\n/).filter(line => line.trim())
  if (lines.length !== 1) throw new Error('invalid_logout_credentials')
  const line = lines[0]
  const first = line.indexOf('----'), last = line.lastIndexOf('----')
  if (first < 0 || last < first + 4) throw new Error('invalid_logout_credentials')
  const email = line.slice(0, first).trim().toLowerCase()
  const password = line.slice(first + 4, last)
  const mfa_secret = line.slice(last + 4).trim()
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email) || email.length > 320 ||
      !password.trim() || password.length > 4096 || !mfa_secret || mfa_secret.length > 4096) {
    throw new Error('invalid_logout_credentials')
  }
  return { email, password, mfa_secret }
}

export interface SessionLogoutJob {
  id: string
  email: string
  status: string
  error_code?: string
  created_at: number
  finished_at?: number
}
const path = '/admin/account-ops/token-guard/session-logout'
export async function startSessionLogout(entry: TokenGuardReloginAccount, requestId: string): Promise<SessionLogoutJob> {
  return (await apiClient.post(path, { ...entry, request_id: requestId, confirmed: true })).data
}
export async function listSessionLogouts(): Promise<SessionLogoutJob[]> {
  return (await apiClient.get(path)).data.jobs ?? []
}
