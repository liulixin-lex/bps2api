import { describe, expect, it } from 'vitest'
import { credentialSubmissionRejection } from '../credentialSubmission'

describe('credential submission acceptance contract', () => {
  it.each(['account_has_unresolved_job', 'account_has_unresolved_logout', 'credential_worker_busy', 'worker_history_capacity'])('recognizes an explicit %s rejection', reason => {
    expect(credentialSubmissionRejection({ status: 409, reason, metadata: { submission: 'not_accepted' } })).toBe(reason)
  })
  it('recognizes handler validation before dispatch', () => {
    expect(credentialSubmissionRejection({ status: 400, reason: 'credential_submission_invalid', metadata: { submission: 'not_accepted' } })).toBe('credential_submission_invalid')
  })
  it.each([
    undefined, null, 'raw-private-message', new Error('network'),
    { status: 409, reason: 'account_has_unresolved_job' },
    { status: 503, reason: 'account_has_unresolved_job', metadata: { submission: 'not_accepted' } },
    { status: 409, reason: 'request_id_reused', metadata: { submission: 'not_accepted' } },
    { status: 409, reason: 'submission_uncertain_review_jobs', metadata: { submission: 'not_accepted' } },
    { status: 409, reason: 'private-cookie', metadata: { submission: 'not_accepted' } },
    { status: 409, reason: 'account_has_unresolved_job', metadata: { submission: 'unknown' } },
    { status: 400, reason: 'worker_history_capacity', metadata: { submission: 'not_accepted' } },
  ])('never treats an unknown result as a definite rejection: %j', error => {
    expect(credentialSubmissionRejection(error)).toBeUndefined()
  })
})
