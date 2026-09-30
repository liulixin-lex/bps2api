import { describe, expect, it } from 'vitest'
import { applyTwoFARotationResult, formatTwoFARotationCredentials, parseTwoFARotationText } from '../accountTwoFARotation'
import type { TokenGuardConfig } from '../accountTokenGuard'

const result = { id: 'job', email: 'User@example.com', password: 'original-password', mfa_secret: 'NEW_SECRET', login_verified: true }
const config = {
  enabled: true,
  relogin_headers: { Authorization: 'unchanged' },
  group_ids: [7],
  relogin_accounts: [
    { email: 'user@example.com', password: 'unchanged-password', mfa_secret: 'OLD_SECRET' },
    { email: 'other@example.com', password: 'other-password', mfa_secret: 'OTHER_SECRET' }
  ]
} as unknown as TokenGuardConfig

describe('2FA rotation input and verified result application', () => {
  it('accepts supported separators while preserving passwords with commas and pipes', () => {
    expect(parseTwoFARotationText('User@example.com----p|a,ss----SECRET')[0]).toEqual({ email: 'user@example.com', password: 'p|a,ss', mfa_secret: 'SECRET' })
    expect(parseTwoFARotationText('user@example.com|pass|SECRET')[0].password).toBe('pass')
    expect(parseTwoFARotationText('user@example.com,pass,SECRET')[0].password).toBe('pass')
  })
  it.each(['', 'bad', 'u@example.com|p|', 'u@example.com|p|s|extra', 'u@example.com|p|s\nU@example.com|p|s'])('rejects ambiguous or incomplete batch: %s', input => {
    expect(() => parseTwoFARotationText(input)).toThrow('invalid_batch')
  })
  it('changes only the seed of a unique saved email without mutating input', () => {
    const original = structuredClone(config)
    const updated = applyTwoFARotationResult(config, result)
    expect(config).toEqual(original)
    expect(updated).toEqual({ ...original, relogin_accounts: [
      { ...original.relogin_accounts[0], mfa_secret: 'NEW_SECRET' }, original.relogin_accounts[1]
    ] })
  })
  it.each([{ ...result, login_verified: false }, { ...result, mfa_secret: ' ' }])('rejects unverified results', value => {
    expect(() => applyTwoFARotationResult(config, value)).toThrow('unverified_result')
  })
  it('rejects missing and duplicate matches', () => {
    expect(() => applyTwoFARotationResult(config, { ...result, email: 'missing@example.com' })).toThrow('unique_saved_account_required')
    expect(() => applyTwoFARotationResult({ ...config, relogin_accounts: [...config.relogin_accounts, config.relogin_accounts[0]] }, result)).toThrow('unique_saved_account_required')
  })
})


describe('verified account credential export', () => {
  it('copies the exact email----password----new-secret format and preserves password characters', () => {
    expect(formatTwoFARotationCredentials({ ...result, password: ' p|a,ss$[]+----word ' })).toBe('User@example.com---- p|a,ss$[]+----word ----NEW_SECRET')
  })
  it.each([
    { ...result, login_verified: false }, { ...result, email: '' },
    { ...result, password: '' }, { ...result, mfa_secret: '' },
    { ...result, password: undefined } as unknown as typeof result
  ])('does not copy incomplete or unverified credentials', value => {
    expect(() => formatTwoFARotationCredentials(value)).toThrow('incomplete_verified_credentials')
  })
})
