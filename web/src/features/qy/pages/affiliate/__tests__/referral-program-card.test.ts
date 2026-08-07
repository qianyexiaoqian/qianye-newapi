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
 * 「推荐计划」从钱包页搬到推广佣金页（需求 1）。
 *
 * # 守什么
 *
 * 项目方原话：「钱包把推荐计划的内容移动到推广佣金的页面下。」
 * 这是一次**搬家**，而搬家有两个端点，只断言其中一个都会给出假绿：
 *
 *   · 只断言"推广页有" → 放过"两边都有"（那不是移动，是复制，两处各显示一份
 *     `aff_quota`，用户会以为自己有两笔待转余额）。
 *   · 只断言"钱包页没有" → 放过"删干净了"（推荐计划整个功能消失，
 *     `/api/user/aff_transfer` 再也没有入口，用户的返佣余额转不出来）。
 *
 * 所以两端一起断言。另外还钉住第三件事：推广页渲染的必须是**上游那一个**
 * `AffiliateRewardsCard`，而不是照着它重画的 qy 版本 —— 后者是本仓反复出现的
 * 「同一概念的第 N 份拷贝」，上游给卡片加字段时这边不会跟着走。
 *
 * 用 AST 走查（`readJsxTree`）而不是 `source.includes()`：注释与 import 里都会
 * 出现这些名字，本文件自己的说明文字就是个例子。
 */
import assert from 'node:assert/strict'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { readJsxTree } from '../../../__tests__/jsx-tree'

// __tests__ → affiliate → pages → qy → features → src
const srcDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..',
  '..'
)
const qyDir = join(srcDir, 'features', 'qy')

const wallet = readJsxTree(join(srcDir, 'features', 'wallet', 'index.tsx'))
const affiliate = readJsxTree(join(qyDir, 'pages', 'affiliate', 'index.tsx'))
const host = readJsxTree(
  join(qyDir, 'pages', 'affiliate', 'components', 'referral-program-card.tsx')
)

describe('推荐计划的搬家（需求 1）', () => {
  test('钱包页不再渲染推荐计划卡，也不再留着它的转账弹窗', () => {
    assert.equal(
      wallet.occurrences('AffiliateRewardsCard').length,
      0,
      '钱包页还在渲染推荐计划卡：这是复制不是移动'
    )
    // 弹窗是那张卡的「转入余额」按钮唯一的去处。卡搬走了弹窗留在钱包页，
    // 就是一个永远打不开的死组件 —— 正是本仓的"断链"形状。
    assert.equal(wallet.occurrences('TransferDialog').length, 0)
  })

  test('推广佣金页恰好渲染一次，且在 QyPageBoundary 之外', () => {
    const spots = affiliate.occurrences('QyReferralProgramCard')
    assert.equal(spots.length, 1, '推广页应当恰好渲染一次推荐计划卡')
    assert.ok(
      !(spots[0] ?? []).includes('QyPageBoundary'),
      `推荐计划卡被放进了 QyPageBoundary（祖先链：${spots[0]?.join(' > ')}）——` +
        ' 它读的是主库 users.aff_*，qy 的 /commission/summary 一挂它会跟着消失'
    )
  })

  test('推广页复用的是上游那一个卡片组件，不是重画的第二份', () => {
    assert.equal(host.occurrences('AffiliateRewardsCard').length, 1)
    assert.equal(host.occurrences('TransferDialog').length, 1)
    assert.ok(
      host.source.includes(
        "from '@/features/wallet/components/affiliate-rewards-card'"
      ),
      '推荐计划卡不再来自上游模块：qy 自己画了一份拷贝'
    )
  })
})
