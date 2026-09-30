import { describe, expect, it } from 'vitest'
import { parseSessionLogoutText } from '../accountSessionLogout'
import { formatTwoFARotationCredentials } from '../accountTwoFARotation'

describe('single-line session logout credentials', () => {
  it('recognizes the standard three-field format', () => {
    expect(parseSessionLogoutText('User@example.com----synthetic-password----JBSWY3DPEHPK3PXP')).toEqual({
      email: 'user@example.com', password: 'synthetic-password', mfa_secret: 'JBSWY3DPEHPK3PXP'
    })
  })
  it('round-trips copied rotation credentials with original password bytes', () => {
    const result = { id: 'a'.repeat(32), email: 'user@example.com', password: ' p|a,ss$[]+----word ', mfa_secret: 'JBSWY3DPEHPK3PXP', login_verified: true }
    expect(parseSessionLogoutText(formatTwoFARotationCredentials(result))).toEqual({
      email: result.email, password: result.password, mfa_secret: result.mfa_secret
    })
  })
  it('accepts surrounding blank clipboard lines and trims only email and seed', () => {
    expect(parseSessionLogoutText('\r\n User@example.com ---- pass ---- JBSWY3DPEHPK3PXP \r\n\n')).toEqual({
      email: 'user@example.com', password: ' pass ', mfa_secret: 'JBSWY3DPEHPK3PXP'
    })
  })
  it.each([
    '', '   ', 'missing-delimiters', 'bad-email----pass----SEED',
    'user@example.com----pass', 'user@example.com--------SEED',
    'user@example.com----   ----SEED', 'user@example.com----pass----',
    'user@example.com----pass---- \t ',
    'user@example.com----pass----SEED\nother@example.com----pass----SEED',
    'user@example.com----pass----SEED\ruser@example.com----pass----SEED',
    'user@example.com,pass,SEED', 'user@example.com|pass|SEED'
  ])('rejects incomplete or multiple accounts with a fixed non-sensitive error', raw => {
    expect(() => parseSessionLogoutText(raw)).toThrow('invalid_logout_credentials')
  })
  it('bounds fields before dispatch', () => {
    for (const raw of [
      'x'.repeat(320) + '@example.com----pass----SEED',
      'user@example.com----' + 'p'.repeat(4097) + '----SEED',
      'user@example.com----pass----' + 'A'.repeat(4097)
    ]) expect(() => parseSessionLogoutText(raw)).toThrow('invalid_logout_credentials')
  })
})
