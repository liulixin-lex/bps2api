// A missing procurement estimate is unknown, never an assumed billing discount.
export const DEFAULT_ACCOUNT_COST_MULTIPLIER: number | null = null

export function isValidAccountCostMultiplier(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= 1000000
}
export function isOptionalAccountCostMultiplier(value: unknown): boolean {
  return value === null || value === '' || isValidAccountCostMultiplier(value)
}
export function readAccountCostMultiplier(extra?: Record<string, unknown> | null): number | null {
  const value = extra?.cost_multiplier
  return isValidAccountCostMultiplier(value) ? value : null
}
