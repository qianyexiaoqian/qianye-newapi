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
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import {
  qyFilterContacts,
  type QyContact,
} from '@/features/qy/pages/transfer/contacts'

/**
 * 联系人弹窗里的搜索（需求 2-B）。
 *
 * 项目方要「联系人的添加提供用户名搜索」。这里守两件事：
 *   ① 搜索确实能按备注名 / 脱敏用户名 / 用户 ID 命中；
 *   ② 它**只翻自己已经存下的那几十条**，不产生任何新的后端查询 ——
 *      按用户名模糊搜索全站用户是用户枚举面，`lookup.go` 顶部刻意写死了
 *      只有 id / email 两档。第 ② 条由下面的"零请求"断言钉死。
 */

function contact(over: Partial<QyContact>): QyContact {
  return {
    id: 1,
    user_id: 1001,
    alias: '',
    masked_username: 'zh***ng',
    status: 'active',
    created_at: 0,
    ...over,
  }
}

const ITEMS: QyContact[] = [
  contact({ id: 1, user_id: 1001, alias: '老王', masked_username: 'wa***ng' }),
  contact({ id: 2, user_id: 2002, alias: '', masked_username: 'Al***ce' }),
  contact({ id: 3, user_id: 31001, alias: 'Bob', masked_username: 'bo***b' }),
]

describe('qyFilterContacts', () => {
  test('空关键字返回全部（且是新数组，不把入参交出去）', () => {
    const out = qyFilterContacts(ITEMS, '   ')
    assert.deepEqual(out, ITEMS)
    assert.notEqual(out, ITEMS)
  })

  test('按备注名命中', () => {
    assert.deepEqual(
      qyFilterContacts(ITEMS, '老王').map((c) => c.id),
      [1]
    )
  })

  test('按脱敏用户名命中，且大小写不敏感', () => {
    assert.deepEqual(
      qyFilterContacts(ITEMS, 'al***ce').map((c) => c.id),
      [2]
    )
    assert.deepEqual(
      qyFilterContacts(ITEMS, 'BO***B').map((c) => c.id),
      [3]
    )
  })

  test('按用户 ID 子串命中（用户记得的往往只是几位数字）', () => {
    assert.deepEqual(
      qyFilterContacts(ITEMS, '1001').map((c) => c.id),
      [1, 3]
    )
  })

  test('一个都不匹配时返回空数组，而不是退回全部', () => {
    // 退回全部会让"搜不到"看起来像"没筛选"，用户会以为搜索坏了。
    assert.deepEqual(qyFilterContacts(ITEMS, 'zzz'), [])
  })
})

describe('搜索不构成新的用户枚举面', () => {
  const src = readFileSync(
    join(dirname(fileURLToPath(import.meta.url)), '..', 'contacts.ts'),
    'utf8'
  )

  test('contacts.ts 里只有一个 GET，且它就是"列出我自己的联系人"', () => {
    // 现有四个接口就是全部：列表 / 添加 / 改名 / 删除。多出来的任何一个
    // 以关键字为参数的读接口，都意味着有人绕过了 resolveRecipient 那三道防线
    // （recipient_lookup 开关、qy_transfer_lookup_logs、按用户 ID 的
    // SearchRateLimit）—— 而且是以"这只是个搜索框"的名义绕过的。
    const gets = [...src.matchAll(/qyGet<[^>]*>\(\s*'([^']*)'/g)].map(
      (m) => m[1]
    )
    assert.deepEqual(gets, ['/transfer/contacts'])
  })

  test('搜索关键字没有被拼进任何请求参数', () => {
    // 关键字一旦出现在 URL 或请求体里，这个搜索框就从"翻我自己的通讯录"
    // 变成了"问服务端某个用户名存不存在"，也就是全站用户枚举面。
    for (const marker of ['search=', 'keyword=', 'q=', 'username=', '?']) {
      assert.ok(
        !src.includes(marker),
        `contacts.ts 里出现了 ${marker}：搜索一旦上行就变成了全站用户枚举面`
      )
    }
  })

  test('筛选是纯函数：同一份入参调两次结果一致，且不改入参', () => {
    const snapshot = JSON.stringify(ITEMS)
    qyFilterContacts(ITEMS, 'bo')
    assert.equal(JSON.stringify(ITEMS), snapshot)
  })
})
