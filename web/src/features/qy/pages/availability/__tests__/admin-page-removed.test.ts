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
import { describe, test } from 'node:test'

import { qyPageMeta } from '@/features/qy/lib/page-meta'
import { QY_PAGE_URL_ORDER } from '@/features/qy/nav'

const ADMIN_URL = '/qy/admin/availability'
const USER_URL = '/qy/availability'

/**
 * 管理端可用率总览已按项目方要求移除；**用户端必须完好无损** —— 可用率是给全体
 * 用户看的，把管理页删成"整个功能没了"是这次改动最容易犯的错。
 *
 * 登记点有三处（`nav.ts` 的 ADMIN_PAGES、`page-meta.ts` 的页面代号、
 * `use-qy-nav-en-label.ts` 的侧栏英文副标），本项目反复出现"同一概念的第 N 份拷贝
 * 各自漂移"，所以两个方向都断言：管理页三处都不许留，用户页三处都不许丢。
 * 侧栏英文副标那份靠 `outcome-i18n-keys.test.ts` 的键缺失断言间接钉住
 * （它是 hook，独立于 React 渲染取不到）。
 */
describe('admin availability page removal', () => {
  test('drops the admin url from the page order', () => {
    assert.ok(
      !QY_PAGE_URL_ORDER.includes(ADMIN_URL),
      `${ADMIN_URL} 已移除，不得再出现在导航声明顺序里`
    )
  })

  test('drops the admin url from the japanese subtitle map', () => {
    // `qyPageMeta` 取最长前缀，删掉本页后 `/qy/admin/availability` 会回落到
    // 管理区 `/qy/admin` 的副标 —— 断言的是"不再有自己那条登记"，
    // 而不是"完全无匹配"，否则这条会被回落行为假绿/假红。
    const meta = qyPageMeta(ADMIN_URL)
    assert.notEqual(meta.codeKey, 'qy_sg_code_a_availability')
    assert.equal(meta.no, '00', '已移除的页面不得再占一个 GATE 序号')
  })

  test('keeps the user-facing availability page fully registered', () => {
    assert.ok(
      QY_PAGE_URL_ORDER.includes(USER_URL),
      '用户端可用率页是本功能唯一入口，不得随管理页一起删掉'
    )
    const meta = qyPageMeta(USER_URL)
    assert.notEqual(meta.no, '00')
    assert.equal(meta.codeKey, 'qy_sg_code_availability')
  })
})
