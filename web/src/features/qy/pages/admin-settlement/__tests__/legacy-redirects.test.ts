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
 * 三条旧地址**真的**会把人送到「结算台」上对的那一张标签。
 *
 * # 为什么这条测试值得存在
 *
 * 日消费明细 / 佣金审核 / 提现审核收进选择夹之后，它们的旧路由只剩一个
 * `beforeLoad` 里的 `redirect()`。这段代码有三种安静的坏法，全都不会报错：
 *
 *   1. `to` 写错 —— 送去一个不存在的地址，用户看到 404（或被兜底路由吞掉）；
 *   2. `hash` 算错 —— 到得了宿主页，但选中的是**另一张标签**。运营点开自己
 *      存的「提现审核」书签，看到的是日消费明细，而这一屏本身完全正常；
 *   3. 佣金审核那条**丢掉 `?inviter_id=`** —— 从佣金余额下钻过去的那一跳
 *      退化成"打开一张不带筛选的全量表"。这一条最隐蔽：页面照常渲染、
 *      照常有数据，只是筛选没了。
 *
 * 所以这里直接执行**生产代码里那个 `beforeLoad`**，接住它抛出的 redirect，
 * 逐字检查三件事。不是读源码 grep，也不是重写一遍同样的逻辑。
 *
 * # 闭环
 *
 * 只断言"hash 等于 qyTabHash(旧 url)"是不够的：那等于用同一个函数证明它自己。
 * 所以每一条还要把算出来的 hash 交给 `qyResolveTab`（宿主页选中标签用的就是
 * 它）走一遍，断言解析回来的正是这条旧地址对应的那一页。跳过去与认出来是
 * 两段独立的代码，这条断言把它们钉在一起。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { QY_TAB_GROUPS, qyTabHash } from '@/features/qy/lib/pages'
import { qyResolveTab } from '@/features/qy/pages/lib/tabs'
import { Route as CommissionRecordsRoute } from '@/routes/_authenticated/qy/admin/commission-records/index'
import { Route as DailyConsumeRoute } from '@/routes/_authenticated/qy/admin/daily-consume/index'
import { Route as WithdrawalsRoute } from '@/routes/_authenticated/qy/admin/withdrawals/index'

const HOST = '/qy/admin/settlement'

/** `redirect()` 抛出来的那个对象：真正的载荷在 `options` 上。 */
type ThrownRedirect = {
  options?: {
    to?: string
    hash?: string
    replace?: boolean
    search?: Record<string, unknown>
  }
}

type RouteLike = {
  options: {
    beforeLoad?: (arg: { search: Record<string, unknown> }) => unknown
  }
}

/** 跑一遍真正的 `beforeLoad`，把它抛出的 redirect 载荷取出来。 */
function redirectOf(
  route: unknown,
  search: Record<string, unknown> = {}
): NonNullable<ThrownRedirect['options']> {
  const beforeLoad = (route as RouteLike).options.beforeLoad
  assert.ok(
    beforeLoad != null,
    '这条旧路由没有 beforeLoad —— 它要么变回了一个真页面（那侧栏上会多出一个入口），要么重定向被删了（旧书签直接 404）'
  )
  try {
    beforeLoad({ search })
  } catch (error) {
    const options = (error as ThrownRedirect).options
    assert.ok(options != null, `beforeLoad 抛的不是 redirect：${String(error)}`)
    return options
  }
  assert.fail('beforeLoad 没有抛出 redirect：旧地址会渲染成一张空白页')
}

const CASES = [
  { url: '/qy/admin/daily-consume', route: DailyConsumeRoute },
  { url: '/qy/admin/commission-records', route: CommissionRecordsRoute },
  { url: '/qy/admin/withdrawals', route: WithdrawalsRoute },
] as const

describe('「结算台」的三条旧地址', () => {
  const group = QY_TAB_GROUPS.find((item) => item.host === HOST)
  const tabs = [...(group?.pages ?? [])]

  test('选择夹本身还在（下面每一条都依赖它）', () => {
    assert.deepEqual(tabs, [
      '/qy/admin/daily-consume',
      '/qy/admin/commission-records',
      '/qy/admin/withdrawals',
    ])
  })

  for (const item of CASES) {
    test(`${item.url} → ${HOST} 上它自己那一张标签`, () => {
      const options = redirectOf(item.route)

      assert.equal(options.to, HOST, '旧地址送去了别的地方')
      assert.equal(
        options.hash,
        qyTabHash(item.url),
        '到得了宿主页，但选中的是另一张标签'
      )
      assert.equal(
        options.replace,
        true,
        '旧地址留在了历史栈里：按返回键会被立刻再弹回来'
      )

      // 闭环：宿主页拿这个 hash 认标签时，认出来的必须是这一页。
      assert.equal(
        qyResolveTab(options.hash, tabs),
        item.url,
        '跳过去的 hash 与宿主页认标签的规则对不上'
      )
    })
  }

  test('佣金审核那条把 ?inviter_id= 一起转发过去', () => {
    const options = redirectOf(CommissionRecordsRoute, { inviter_id: '412' })
    assert.deepEqual(
      options.search,
      { inviter_id: '412' },
      '从佣金余额下钻的那一跳丢了筛选：页面照常有数据，只是筛选没了，不会有任何报错'
    )
  })

  test('另外两条不需要 search，也不该凭空造一个', () => {
    // 它们的正文不读任何 query 参数。带一个空对象过去只会让地址栏出现 `?`，
    // 而运营复制出去的链接从此多一截噪声。
    assert.equal(redirectOf(DailyConsumeRoute).search, undefined)
    assert.equal(redirectOf(WithdrawalsRoute).search, undefined)
  })
})
