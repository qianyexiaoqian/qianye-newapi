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
import { QY_PAGE_URL_ORDER } from '@/features/qy/lib/page-order'
import { QY_PAGES } from '@/features/qy/lib/pages'

/**
 * `LAB MEMO — NN` 与导航结构解耦之后的两条守则。
 *
 * 编号从前是 `nav.ts` 里页面的声明顺序派生的，把 23 个页面按语义拆进上游分组
 * 那一刻，所有编号会集体错位 —— 用户记下的 `LAB MEMO — 14` 指向了别的页面。
 * 所以：
 *   ① **快照**：下面这份字面量是重排之前的实际编号，逐项相等，防误改；
 *   ② **集合恒等**：编号表与页面表的 url 集合必须完全一致，防漂移
 *      （少一个 = 新页面忘了登记编号，页面会退化成 `00`；
 *        多一个 = 页面已删而编号残留，后面所有页面凭空多一号）。
 */
const FROZEN_ORDER = [
  '/qy/affiliate',
  '/qy/invitees',
  '/qy/transfer',
  '/qy/transfer-logs',
  '/qy/pay-password',
  '/qy/withdraw',
  '/qy/withdrawals',
  '/qy/violations',
  '/qy/availability',
  '/qy/admin/commission',
  '/qy/admin/commission-records',
  '/qy/admin/transfer-records',
  '/qy/admin/transfer-group-rules',
  '/qy/admin/transfer-config',
  '/qy/admin/withdrawals',
  '/qy/admin/violation-rules',
  '/qy/admin/violations',
  '/qy/admin/user-group',
  '/qy/admin/group-pricing',
  '/qy/admin/site-theme',
  '/qy/admin/fund-orders',
  '/qy/admin/audit-logs',
  '/qy/admin/health',
]

describe('qy page order', () => {
  test('keeps the first 23 slots byte-identical to the pre-reshuffle numbering', () => {
    FROZEN_ORDER.forEach((url, index) => {
      assert.equal(
        QY_PAGE_URL_ORDER[index],
        url,
        `第 ${index + 1} 号被改动了：期望 ${url}，实际 ${QY_PAGE_URL_ORDER[index]}`
      )
    })
    assert.ok(
      QY_PAGE_URL_ORDER.length >= FROZEN_ORDER.length,
      '新页面只能追加到编号表末尾，不能顶掉既有编号'
    )
  })

  test('has no duplicate slot', () => {
    assert.equal(
      new Set(QY_PAGE_URL_ORDER).size,
      QY_PAGE_URL_ORDER.length,
      '同一个 url 占了两个编号'
    )
  })

  test('matches the page table url set in both directions', () => {
    const inTable = new Set(QY_PAGES.map((page) => page.url))
    const inOrder = new Set(QY_PAGE_URL_ORDER)

    for (const url of inTable) {
      assert.ok(inOrder.has(url), `${url} 在页面表里但没有 LAB MEMO 编号`)
    }
    for (const url of inOrder) {
      assert.ok(inTable.has(url), `${url} 有编号但已经不在页面表里`)
    }
  })

  test('numbers every registered page and gives 00 to the two index pages', () => {
    assert.equal(qyPageMeta('/qy/affiliate').no, '01')
    assert.equal(qyPageMeta('/qy/availability').no, '09')
    assert.equal(qyPageMeta('/qy/admin/health').no, '23')
    // 索引页是分组入口而不是功能页，不占编号。
    assert.equal(qyPageMeta('/qy').no, '00')
    assert.equal(qyPageMeta('/qy/admin').no, '00')
    // 详情页继承所属功能页的编号（最长前缀），不掉进未登记分支。
    assert.equal(qyPageMeta('/qy/admin/violations/123').no, '17')
  })
})
