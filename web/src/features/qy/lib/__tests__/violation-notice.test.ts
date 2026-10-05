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
import { test } from 'node:test'

import { qyViolationNotices } from '../violation-notice'

// 「我的违规记录」页横幅:取 /api/status 的 qy_violation_notices。
// 平铺 test():bun 的 node:test 兼容层在跨文件加载时 describe() 会抛错。

test('shows configured notices in order and skips blank ones', () => {
  assert.deepEqual(
    qyViolationNotices({
      qy_violation_notices: ['  第一条  ', '   ', '第三条\n第二行'],
    }),
    ['第一条', '第三条\n第二行']
  )
})

test('shows at most three banners', () => {
  assert.deepEqual(
    qyViolationNotices({ qy_violation_notices: ['1', '2', '3', '4'] }),
    ['1', '2', '3']
  )
})

test('treats a missing or malformed field as no banners', () => {
  // 旧版本 localStorage 里缓存的 status 没有这个字段,或形状不对。
  assert.deepEqual(qyViolationNotices(null), [])
  assert.deepEqual(qyViolationNotices({}), [])
  assert.deepEqual(qyViolationNotices({ qy_violation_notices: 'oops' }), [])
  assert.deepEqual(
    qyViolationNotices({ qy_violation_notices: [42, null, '有效'] }),
    ['有效']
  )
})
