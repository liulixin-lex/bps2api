import { describe, expect, it } from 'vitest'
import { isValidAccountCostMultiplier, readAccountCostMultiplier } from '../accountCost'

describe('account cost multiplier', () => {
  it('defaults to unknown independently of account billing and preserves explicit zero', () => {
    expect(readAccountCostMultiplier()).toBe(null)
    expect(readAccountCostMultiplier({ rate_multiplier: 7 })).toBe(null)
    expect(readAccountCostMultiplier({ cost_multiplier: 0 })).toBe(0)
    expect(readAccountCostMultiplier({ cost_multiplier: 0.25 })).toBe(0.25)
  })
  it.each([-1, NaN, Infinity, 1000001, '0.1', '', null, undefined, true])('rejects invalid value %s', value => {
    expect(isValidAccountCostMultiplier(value)).toBe(false)
    expect(readAccountCostMultiplier({ cost_multiplier: value })).toBe(null)
  })
})
