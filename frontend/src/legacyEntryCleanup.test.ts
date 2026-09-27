// Source hygiene gate for the key-auto-recovery modal removal (TC-11): the
// old sidebar entry + modal UI was replaced by the form on the General
// settings page, and these assertions pin that the removal stays complete —
// no leftover component file, no dangling references in non-test sources,
// and no orphaned nav label in either locale tree — while the message keys
// the migrated form still uses must NOT be swept away with it (the
// keyAutoRecovery domain belongs to the page now, per the migration note in
// GeneralSettingsPage.vue).
//
// The sources are read through vite's import.meta.glob with a ?raw query —
// every non-test .ts/.vue file under src/ as its own text, no fs walking.
import { describe, expect, it } from 'vitest'

import en from './locales/en'
import zhCN from './locales/zh-CN'

// Keyed by path ('./layouts/DefaultLayout.vue', ...); values are raw source.
const sources = import.meta.glob<string>('./**/*.{ts,vue}', {
  query: '?raw',
  import: 'default',
  eager: true,
})

const REMOVED_COMPONENT = 'KeyAutoRecoveryModal'

// Test files legitimately mention the removed component's name (this file
// does); the shipped sources must not.
function shippedSources(): [path: string, text: string][] {
  return Object.entries(sources).filter(([path]) => !path.endsWith('.test.ts'))
}

describe('key-auto-recovery modal removal stays complete (TC-11)', () => {
  it('no removed-modal component file remains anywhere under src', () => {
    const leftovers = Object.keys(sources).filter((path) => path.endsWith(`/${REMOVED_COMPONENT}.vue`))
    expect(leftovers, 'files named after the removed component').toEqual([])
  })

  it('no non-test source references the removed modal component', () => {
    const offenders = shippedSources()
      .filter(([, text]) => text.includes(REMOVED_COMPONENT))
      .map(([path]) => path)
    expect(offenders, 'sources still mentioning the removed component').toEqual([])
  })

  it('the modal entry\'s exclusive nav label is gone from both locale trees', () => {
    // nav.keyAutoRecovery existed only to label the deleted sidebar entry;
    // the keyAutoRecovery message domain (below) is a separate file that
    // stays.
    expect(en.nav).not.toHaveProperty('keyAutoRecovery')
    expect(zhCN.nav).not.toHaveProperty('keyAutoRecovery')
  })

  it('the migrated form\'s message keys stay in both locale trees', () => {
    // The General settings page renders the recovery card from these keys
    // (title/desc + the interval messages picked via
    // utils/keyAutoRecovery's intervalIssueMessageKey) — removing the modal
    // must not take its copy with it.
    for (const tree of [en, zhCN]) {
      expect(tree.keyAutoRecovery).toHaveProperty('title')
      expect(tree.keyAutoRecovery).toHaveProperty('desc')
      expect(tree.keyAutoRecovery).toHaveProperty('intervalRequired')
      expect(tree.keyAutoRecovery).toHaveProperty('conflict')
    }
  })
})
