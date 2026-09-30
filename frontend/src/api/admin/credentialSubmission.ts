// Only an explicit, allowlisted pre-dispatch rejection can release a pending ID.
// Never infer acceptance from the status alone or from an upstream message.
const conflictReasons = ['account_has_unresolved_job', 'account_has_unresolved_logout',
  'credential_worker_busy', 'worker_history_capacity'] as const
export type CredentialSubmissionRejection = typeof conflictReasons[number] | 'credential_submission_invalid'

export function credentialSubmissionRejection(error: unknown): CredentialSubmissionRejection | undefined {
  if (!error || typeof error !== 'object') return
  const value = error as { status?: number; reason?: unknown; metadata?: { submission?: string } }
  if (value.metadata?.submission !== 'not_accepted') return
  if (value.status === 400 && value.reason === 'credential_submission_invalid') return value.reason
  if (value.status === 409 && conflictReasons.includes(value.reason as typeof conflictReasons[number])) {
    return value.reason as CredentialSubmissionRejection
  }
}
