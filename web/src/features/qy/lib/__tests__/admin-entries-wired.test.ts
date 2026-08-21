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
 * 三条本轮补上的管理端入口，必须留在界面上。
 *
 * ## 为什么这一条不能靠 route-contract 的反向对账
 *
 * 那条守卫比对的是「源码里出现过的路径字面量」与后端路由清单。它抓得住
 * 「后端有、前端连封装都没写」，抓不住「封装还在、页面上那一行被删了」——
 * 实测：把管理端打款凭证的 `<QyWithdrawProofImage>` 整块删掉，反向对账仍然全绿，
 * 因为 `qyFetchWithdrawProofBlob` 里那条路径字面量还在。
 *
 * 这正是本仓复发过五次以上的形状：**实现了但界面上点不到**。所以这三条各自
 * 再钉一层「渲染它的那一行还在不在」。
 *
 * ## 三条各自的代价
 *
 *  1. 抽奖对账异常无法标记已处理 → `checkActivityDeletable` 第五道闸门
 *     (errDeleteFlagOpen) 永远拦着，被报过异常的场次在管理界面上**永久删不掉**；
 *  2. 管理端看不到用户上传的打款凭证 → 审核提现的人只能凭一句话打款
 *     (proof_test.go 的注释：「少了下载，图片存进去就再也拿不出来」)；
 *  3. 支付密码只能重置不能解锁 → 对一个「密码没问题、只是输错了几次」的用户，
 *     唯一可点的动作是把他的密码清掉，下一次动钱直接被 `qy_pay_pwd_not_set` 拦住。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

const FEATURES = fileURLToPath(new URL('../../../', import.meta.url))

function read(rel: string): string {
  return readFileSync(join(FEATURES, rel), 'utf-8')
}

describe('本轮补上的三条管理端入口', () => {
  test('抽奖对账异常：表格里有「标记已处理」的按钮', () => {
    const src = read('qy/pages/admin-lottery/components/lottery-events-tab.tsx')
    assert.ok(
      src.includes('resolveQyLotFlag'),
      '事件页不再调 resolveQyLotFlag —— 被报过异常的活动会永久删不掉'
    )
    assert.ok(
      src.includes('resolve.mutate(row.id)'),
      '按钮上的 onClick 不见了：封装还在，界面上点不到,等于没有'
    )
    assert.ok(src.includes("t('qy_lot_flag_resolve')"), '按钮文案不见了')
  })

  test('提现审核：单据上渲染用户上传的打款凭证', () => {
    const src = read('qy/pages/admin-withdrawals/components/review-dialog.tsx')
    assert.ok(
      src.includes('<QyWithdrawProofImage'),
      '审核弹窗不再渲染打款凭证 —— 审核人只能凭一句话打款'
    )
    assert.ok(
      /<QyWithdrawProofImage[^>]*\badmin\b/.test(src),
      '必须带 admin：不带的话走的是按 user_id 作用域的用户端路径，管理员必然 404'
    )
    assert.ok(
      src.includes('withdrawal.has_proof'),
      '没附凭证的单子不该摆一个永远加载失败的框'
    )
  })

  test('支付密码：锁着的时候可以只解锁、不清密码', () => {
    const src = read('qy/components/qy-reset-pay-password-dialog.tsx')
    assert.ok(
      src.includes('qyAdminUnlockPayPassword'),
      '对话框不再调解锁接口 —— 唯一可点的动作又只剩破坏性的重置'
    )
    assert.ok(src.includes('unlock.mutate('), '解锁按钮的 onClick 不见了')
    assert.ok(
      src.includes('status?.locked === true'),
      '解锁按钮必须只在真的锁着时出现：没锁时点它什么都不会变'
    )
  })
})
