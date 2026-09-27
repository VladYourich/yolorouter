// Pure form logic for the request-log-retention field on the General
// settings page: retention validation plus the GET→form→PUT projections.
// Kept out of the component so the rules are unit-testable without mounting
// anything, mirroring the backend service-layer bounds exactly — the client
// rejects a bad retention with an explicit message instead of letting the
// request bounce (or worse, a numeric input silently clamping the value into
// range). Same module shape as utils/keyAutoRecovery, the sibling setting
// this page also hosts.

// Whole days, [0, 3650] — mirrors internal/service/systemsettings's
// UpdateRequestLogRetention validation (min 0 = keep forever, max ten
// years). Keep in sync or the client accepts something the server rejects,
// or vice versa.
export const REQUEST_LOG_MIN_RETENTION_DAYS = 0
export const REQUEST_LOG_MAX_RETENTION_DAYS = 3650

/** Why a retention input was rejected — selects the localized message. */
export type RetentionIssue = 'empty' | 'notWholeNumber' | 'belowMin' | 'aboveMax'

export type RetentionCheck =
  | { ok: true; days: number }
  | { ok: false; issue: RetentionIssue }

/**
 * Validates one retention-days input. Accepts the exact shapes
 * NInputNumber's v-model produces (`number | null` — null when cleared or
 * unparsable), so the component passes its form value straight through. NaN
 * is treated as empty: it cannot reach the wire as JSON anyway and reads to
 * the user as "nothing valid entered", not "wrong kind of number". Unlike
 * the key-recovery interval, 0 is a legal value — it means "keep forever",
 * the fail-open default.
 */
export function checkRetentionDays(input: number | null | undefined): RetentionCheck {
  if (input === null || input === undefined || Number.isNaN(input)) {
    return { ok: false, issue: 'empty' }
  }
  if (!Number.isInteger(input)) return { ok: false, issue: 'notWholeNumber' }
  if (input < REQUEST_LOG_MIN_RETENTION_DAYS) return { ok: false, issue: 'belowMin' }
  if (input > REQUEST_LOG_MAX_RETENTION_DAYS) return { ok: false, issue: 'aboveMax' }
  return { ok: true, days: input }
}

/**
 * The one place a retention issue becomes an i18n key. Components and tests
 * share it, so renaming a message key can't leave a validation branch
 * pointing at a string that no longer exists.
 */
export function retentionIssueMessageKey(issue: RetentionIssue): string {
  switch (issue) {
    case 'empty':
      return 'generalSettings.retention.daysRequired'
    case 'notWholeNumber':
      return 'generalSettings.retention.daysWholeNumber'
    case 'belowMin':
      return 'generalSettings.retention.daysMin'
    case 'aboveMax':
      return 'generalSettings.retention.daysMax'
  }
}

/** The retention form's editable state (what the GET prefills, what the PUT sends). */
export interface RetentionForm {
  days: number | null
}

/** Projects the GET payload's snake_case row into the editable form state. */
export function retentionFormFromSetting(setting: {
  retention_days: number
}): RetentionForm {
  return { days: setting.retention_days }
}

export type RetentionSavePlan =
  | { ok: true; payload: { retention_days: number; version: number } }
  | { ok: false; issue: RetentionIssue }

/**
 * Everything onSave needs decided in one call: whether the current form can
 * be PUT, the exact request payload if so, or the blocking retention issue
 * if not. The component never re-derives the bounds itself.
 */
export function retentionSavePlan(form: RetentionForm, version: number): RetentionSavePlan {
  const check = checkRetentionDays(form.days)
  if (!check.ok) return { ok: false, issue: check.issue }
  return { ok: true, payload: { retention_days: check.days, version } }
}
