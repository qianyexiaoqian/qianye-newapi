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
/*
 * 「API信息」并入 qy 地址簿（decisions.md 的 D-09）之后，原位置的落点判据。
 * 形状照抄 admin-restricted-accounts 的 section-placement.test.ts —— 那是本仓
 * 第一次搬家时立下的三条不变量，第二次搬家必须逐条适用：
 *
 *   ① 旧 section id 还在：`/system-settings/content/api-info` 这条深链接落到
 *      路牌上，而不是被 `$section` 路由静默重定向回默认段；
 *   ② 左侧菜单里不再有这一项：点进去只告诉你去别处的常驻菜单项是纯噪声；
 *   ③ 路牌零输入控件并真的指向新家 —— 原位置留一份能填能存、却写到别处
 *      （或没接上）的副本，是搬家最贵的失败方式。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import type { TFunction } from 'i18next'

import {
  CONTENT_SECTION_IDS,
  getContentSectionNavItems,
} from '@/features/system-settings/content/section-registry.tsx'

const t = ((key: string) => key) as unknown as TFunction

// __tests__ → admin-api-address → pages → qy → features → src
const srcDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..',
  '..'
)

const PAGE_URL = '/qy/admin/api-address'
const MOVED_SECTION_ID = 'api-info'

describe('「API信息」搬进地址簿后的原位置', () => {
  test('旧深链接仍然落得到路牌，但菜单里不再有这一项', () => {
    assert.ok(
      (CONTENT_SECTION_IDS as readonly string[]).includes(MOVED_SECTION_ID),
      '旧 section id 被删了 —— 旧深链接会被静默重定向回默认段，管理员只会以为自己记错了地址'
    )

    const navUrls = getContentSectionNavItems(t).map((item) => item.url)
    assert.ok(
      !navUrls.some((url) => url.endsWith(`/${MOVED_SECTION_ID}`)),
      '菜单里还留着「API信息」—— 表单已经并入地址簿，这一项只会把人引到一块路牌上'
    )
  })

  test('路牌没有任何输入控件，并指向地址簿', () => {
    const source = readFileSync(
      join(
        srcDir,
        'features',
        'system-settings',
        'content',
        'qy-api-info-moved.tsx'
      ),
      'utf8'
    )
    for (const control of [
      '<Input',
      '<Textarea',
      '<Switch',
      '<Select',
      'useMutation',
    ]) {
      assert.ok(
        !source.includes(control),
        `路牌里出现了 ${control} —— 它必须只是一句话加一个链接，不能是第二份表单`
      )
    }
    assert.ok(source.includes(PAGE_URL), `路牌没有指向 ${PAGE_URL}`)
  })
})
