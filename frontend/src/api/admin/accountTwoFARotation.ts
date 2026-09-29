import { apiClient } from '../client'
import { parseTwoFALoginText, type TokenGuardConfig, type TokenGuardReloginAccount } from './accountTokenGuard'

export interface TwoFARotationJob {
  id: string
  email: string
  status: string
  error_code?: string
  login_verified: boolean
  rotated_pending_verify: boolean
  retryable: boolean
  created_at: number
}

export interface TwoFARotationResult {
  id: string
  email: string
  password: string
  mfa_secret: string
  login_verified: boolean
}

const path = '/admin/account-ops/token-guard/two-fa-rotation'

export function parseTwoFARotationText(raw: string): TokenGuardReloginAccount[] {
  return parseTwoFALoginText(raw.split(/\r?\n/).map(line =>
    line.includes('----') ? line : line.includes('|') ? line.split('|').join('----') : line
  ).join('\n'))
}

export async function startTwoFARotation(entry: TokenGuardReloginAccount, requestId: string): Promise<TwoFARotationJob> {
  return (await apiClient.post(path, { ...entry, request_id: requestId, confirmed: true })).data
}

export async function listTwoFARotations(): Promise<TwoFARotationJob[]> {
  return (await apiClient.get(path)).data.jobs ?? []
}

export async function verifyTwoFARotation(id: string): Promise<TwoFARotationJob> {
  return (await apiClient.post(path + '/' + encodeURIComponent(id) + '/verify')).data
}

export async function getTwoFARotationResult(id: string): Promise<TwoFARotationResult> {
  return (await apiClient.get(path + '/' + encodeURIComponent(id) + '/result')).data
}

export function formatTwoFARotationCredentials(result: TwoFARotationResult): string {
  if (!result.login_verified || !result.email || !result.password || !result.mfa_secret?.trim()) {
    throw new Error('incomplete_verified_credentials')
  }
  return [result.email, result.password, result.mfa_secret].join('----')
}

// Update only an existing, uniquely matched re-login entry. Do not add accounts,
// change passwords, alter OAuth tokens, or replace unrelated guard settings.
export function applyTwoFARotationResult(config: TokenGuardConfig, result: TwoFARotationResult): TokenGuardConfig {
  if (!result.login_verified || !result.mfa_secret.trim()) throw new Error('unverified_result')
  const email = result.email.trim().toLowerCase()
  const accounts = config.relogin_accounts ?? []
  if (accounts.filter(entry => entry.email.trim().toLowerCase() === email).length !== 1) {
    throw new Error('unique_saved_account_required')
  }
  return {
    ...config,
    relogin_accounts: accounts.map(entry => entry.email.trim().toLowerCase() === email
      ? { ...entry, mfa_secret: result.mfa_secret }
      : { ...entry })
  }
}
