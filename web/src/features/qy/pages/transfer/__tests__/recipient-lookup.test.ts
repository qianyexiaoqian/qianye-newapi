/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'

import {
  qyIsAmbiguousPreview,
  qyPinnedBy,
  qyRecipientInputHints,
} from '@/features/qy/pages/transfer/lib/recipient-lookup'
import type { QyTransferPreview } from '@/features/qy/pages/transfer/types'

const preview = (over: Partial<QyTransferPreview>): QyTransferPreview => ({
  exists: false,
  user_id: 0,
  masked_username: '',
  masked_email: '',
  receivable: false,
  blocked_reason: '',
  amount: 0,
  fee_quota: 0,
  total: 0,
  ...over,
})

test('username mode opens a text keyboard and its own hint copy', () => {
  assert.deepEqual(qyRecipientInputHints('id_or_username'), {
    inputMode: 'text',
    placeholderKey: 'qy_tr_recipient_ph_id_username',
    helpKey: 'qy_tr_recipient_help_id_username',
  })
  assert.equal(qyRecipientInputHints('id_or_email').inputMode, 'text')
  // 未知取值回落最保守的「只认 ID」，而不是暗示可以搜名字。
  assert.equal(qyRecipientInputHints('id').inputMode, 'numeric')
  assert.equal(qyRecipientInputHints('whatever').helpKey, 'qy_tr_recipient_help_id')
})

test('every hint key exists in both locales', () => {
  const dir = dirname(fileURLToPath(import.meta.url))
  const load = (lang: string) =>
    JSON.parse(
      readFileSync(join(dir, `../../../../../i18n/qy/${lang}.json`), 'utf8')
    ) as Record<string, string>
  const keys = ['id', 'id_or_email', 'id_or_username'].flatMap((mode) => {
    const h = qyRecipientInputHints(mode)
    return [h.placeholderKey, h.helpKey]
  })
  keys.push(
    'qy_tr_ambiguous_title',
    'qy_tr_ambiguous_desc',
    'qy_tr_matched_by_id',
    'qy_tr_matched_by_username',
    'qy_tr_pick_candidate',
    'qy_err_contact_ambiguous'
  )
  for (const lang of ['zh', 'en']) {
    const dict = load(lang)
    for (const key of keys) assert.ok(dict[key], `${lang} missing ${key}`)
  }
})

test('ambiguous only when the backend actually sent candidates', () => {
  const candidates = [
    { user_id: 2, masked_username: 'al***e', matched_by: 'id' as const },
    { user_id: 3, masked_username: '2', matched_by: 'username' as const },
  ]
  assert.equal(
    qyIsAmbiguousPreview(preview({ blocked_reason: 'ambiguous', candidates })),
    true
  )
  assert.equal(qyIsAmbiguousPreview(preview({ blocked_reason: 'ambiguous' })), false)
  assert.equal(qyIsAmbiguousPreview(preview({ blocked_reason: 'not_found' })), false)
  assert.equal(qyIsAmbiguousPreview(null), false)
})

test('a picked lookup mode only applies to the exact input it was picked for', () => {
  const pinned = { identifier: '3', by: 'id' as const }
  assert.equal(qyPinnedBy(pinned, '3'), 'id')
  assert.equal(qyPinnedBy(pinned, ' 3 '), 'id')
  // 用户改了输入：选择作废，回到自动判断。
  assert.equal(qyPinnedBy(pinned, '33'), undefined)
  assert.equal(qyPinnedBy(null, '3'), undefined)
})
