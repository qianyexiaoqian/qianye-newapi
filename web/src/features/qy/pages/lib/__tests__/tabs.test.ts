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

import { qyTabHash } from '@/features/qy/lib/pages'
import { qyResolveTab } from '@/features/qy/pages/lib/tabs'

/**
 * 「选择夹选中哪一张」的解析规则（需求 2 / 3）。
 *
 * 这里守的是**回落**：hash 认不出来时必须落到一张真实存在且可见的标签，
 * 而不是渲染空白。会走到这条分支的现实场景有三个，一个比一个常见：
 *   · 用户手改地址栏 / 老书签指向已经改名的页面；
 *   · 站点把 `features.withdraw` 关了，而分享出去的链接正指向提现那一张；
 *   · 从入口卡跳进来时 hash 拼错（那是断链，但用户看到的应该是第一张标签）。
 */

const WALLET_TABS = ['/qy/transfer', '/qy/transfer-logs', '/qy/pay-password']

describe('qyResolveTab', () => {
  test('认得出每一张标签自己的 hash', () => {
    for (const url of WALLET_TABS) {
      assert.equal(qyResolveTab(qyTabHash(url), WALLET_TABS), url)
    }
  })

  test('带不带 # 前缀都认（TanStack 给的是不带的，手拼的常带）', () => {
    assert.equal(
      qyResolveTab('#qy-transfer-logs', WALLET_TABS),
      '/qy/transfer-logs'
    )
  })

  test('hash 缺失 / 认不出时落到第一张，而不是渲染空白', () => {
    assert.equal(qyResolveTab(undefined, WALLET_TABS), '/qy/transfer')
    assert.equal(qyResolveTab('', WALLET_TABS), '/qy/transfer')
    assert.equal(qyResolveTab('wallet-add-funds', WALLET_TABS), '/qy/transfer')
  })

  test('指向已被功能开关关掉的标签时落到第一张可见标签', () => {
    // features.withdraw 关掉之后，宿主页交进来的可见列表里没有提现两张。
    const visible = ['/qy/affiliate', '/qy/invitees']
    assert.equal(qyResolveTab('qy-withdraw', visible), '/qy/affiliate')
  })

  test('一张可见标签都没有时返回 null（宿主自己决定整块不渲染）', () => {
    assert.equal(qyResolveTab('qy-transfer', []), null)
  })
})
