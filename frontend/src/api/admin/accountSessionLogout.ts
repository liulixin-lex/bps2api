import { apiClient } from '../client'
import type { TokenGuardReloginAccount } from './accountTokenGuard'

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
