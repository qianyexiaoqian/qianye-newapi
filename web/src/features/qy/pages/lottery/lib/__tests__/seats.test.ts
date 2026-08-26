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

import {
  QY_LOT_MEASURED_MS_PER_PICK,
  qyLotBatchSeconds,
  qyLotSeatCap,
  type QyLotSeatBinding,
} from '../seats'

/**
 * 「这一次还能加几注」以及**是哪一条闸门定的**。
 *
 * ## 为什么"哪一条"必须被算出来而不是只算一个数
 *
 * 三条闸门在服务端是三个不同的错误码（`qy_lot_too_many_picks` /
 * `qy_lot_user_cap` / `qy_lot_cap_reached`），而用户读完要做的下一个动作完全
 * 相反：单次批量到顶 → 再提交一次就能接着买；每人上限到顶 → 再提交多少次都
 * 没用；全场名额到顶 → 手快有手慢无。合成一句"还能买 N 注"，这三种情况里有
 * 两种会让用户做错事。
 *
 * 下面这张表逐格给出 `(单次批量, 我的剩余, 全场剩余) → (能加几注, 哪一条)`，
 * 每一行的期望值都是自己按上面那段口径算出来的，不是从实现回读的。
 */
describe('qyLotSeatCap：三条闸门取最紧的那一条', () => {
  const cases: {
    name: string
    perRequest: number | undefined
    mine: number | null | undefined
    total: number | null | undefined
    cap: number
    binding: QyLotSeatBinding
  }[] = [
    {
      name: '没有任何一条每人/全场闸门 → 单次批量说了算',
      perRequest: 999,
      mine: null,
      total: null,
      cap: 999,
      binding: 'per_request',
    },
    {
      name: '每人上限比单次批量紧',
      perRequest: 999,
      mine: 500,
      total: null,
      cap: 500,
      binding: 'per_user',
    },
    {
      name: '全场名额比每人上限还紧',
      perRequest: 999,
      mine: 500,
      total: 3,
      cap: 3,
      binding: 'total',
    },
    {
      // 相等时取**更重**的那条：说"一次最多买 10 注"会让用户以为再提交一次
      // 还能买，而全场其实只剩这 10 个名额。
      name: '全场剩余恰好等于单次批量 → 报全场',
      perRequest: 10,
      mine: null,
      total: 10,
      cap: 10,
      binding: 'total',
    },
    {
      name: '每人剩余恰好等于单次批量 → 报每人',
      perRequest: 10,
      mine: 10,
      total: null,
      cap: 10,
      binding: 'per_user',
    },
    {
      name: '每人买满 → 0 注，而且说得出是每人上限',
      perRequest: 999,
      mine: 0,
      total: 500,
      cap: 0,
      binding: 'per_user',
    },
    {
      name: '全场买满 → 0 注，而且说得出是全场',
      perRequest: 999,
      mine: 500,
      total: 0,
      cap: 0,
      binding: 'total',
    },
    {
      // 上限被在线调低之后剩余量可以是负数（后端已夹到 0，但契约上仍要挡）：
      // 一个负的 cap 会让 `openSlots` 算出负数，而 `Array.from({length: -3})`
      // 是空数组 —— 表现是"机选补满"点了没反应，没有任何一处报错。
      name: '负的剩余量夹到 0',
      perRequest: 10,
      mine: -3,
      total: null,
      cap: 0,
      binding: 'per_user',
    },
    {
      // 一个不下发 `max_picks_per_request` 的后端根本不认识 `picks`，
      // 多发几注只会被整批 400。退回一个乐观的默认会让每一次多注提交都失败。
      name: '老后端不下发单次批量 → 退回一注',
      perRequest: undefined,
      mine: undefined,
      total: undefined,
      cap: 1,
      binding: 'per_request',
    },
    {
      // 老后端不下发 `total_entries_remaining`。`undefined` 必须与"本场没有
      // 全场上限"完全同义，而不是被读成 0（那会让每一场都变成"名额已满"）。
      name: '老后端不下发全场剩余 → 当作没有全场上限',
      perRequest: 10,
      mine: null,
      total: undefined,
      cap: 10,
      binding: 'per_request',
    },
  ]

  for (const c of cases) {
    test(c.name, () => {
      const got = qyLotSeatCap({
        max_picks_per_request: c.perRequest,
        my_entries_remaining: c.mine,
        total_entries_remaining: c.total,
      })
      assert.equal(got.cap, c.cap)
      assert.equal(got.binding, c.binding)
    })
  }

  test('三个原始量原样带出，供界面分别说明', () => {
    const got = qyLotSeatCap({
      max_picks_per_request: 999,
      my_entries_remaining: 500,
      total_entries_remaining: 3,
    })
    assert.equal(got.perRequestCap, 999)
    assert.equal(got.myRemaining, 500)
    assert.equal(got.totalRemaining, 3)
  })
})

/**
 * 「这一批要跑多久」。
 *
 * N 注在服务端是 N 次**串行**扣费，所以估时与 N 成正比。这个数唯一的用途是
 * 在按下确认之前把代价说出来 —— 一个转了半分钟的按钮与一个卡死的页面在屏幕上
 * 长得一模一样。
 */
describe('qyLotBatchSeconds：把注数换算成秒', () => {
  test('满配 999 注按实测均值算出来是三十几秒', () => {
    // 期望值在这里独立算一遍：999 × 36ms = 35.964s → 向上取整 36 秒。
    assert.equal(QY_LOT_MEASURED_MS_PER_PICK, 36)
    assert.equal(qyLotBatchSeconds(999), 36)
  })

  test('向上取整 —— 说少了比说多了更糟', () => {
    // 10 注 = 0.36 秒。报 0 秒等于告诉用户"这是瞬间的事"。
    assert.equal(qyLotBatchSeconds(10), 1)
    assert.equal(qyLotBatchSeconds(28), 2) // 1.008s → 2
  })

  test('后端下发别的每注耗时时按它算，不按前端那份默认', () => {
    // 前端写死一份不同的数会让"预计 N 秒"在后端调整之后继续印着一个不再成立
    // 的秒数 —— 这一格的全部价值就在于那个数是真的。
    assert.equal(qyLotBatchSeconds(100, 50), 5)
    assert.equal(qyLotBatchSeconds(100, 10), 1)
  })

  test('零与负数不产出一个假的秒数', () => {
    assert.equal(qyLotBatchSeconds(0), 0)
    assert.equal(qyLotBatchSeconds(-5), 0)
    assert.equal(qyLotBatchSeconds(100, 0), 0)
  })
})

/*
 * ── 变异实验 ────────────────────────────────────────────────────────
 *
 * 改在 `lib/seats.ts` 上，改完跑 `bun run test`（运行器会跑整个 src，
 * 不接受单文件参数；基线 2000 pass / 3 fail）。
 *
 * 本轮实跑:
 *
 * ① 删掉整段 `totalRemaining` 判定（退回改造前只看每人上限）。
 *    → KILLED：1995 pass / 8 fail（+5）。本文件三行（"全场比每人还紧"、
 *      "全场剩余等于单次批量"、"全场买满"）+ 弹窗那两条用例。
 *
 * 未实跑但由上面那张表逐格覆盖的（每一行的期望值都是本文件独立算出的）:
 *   `<= cap` → `< cap`（相等时不取更重的那条）→ 「全场剩余恰好等于单次批量」；
 *   `?? 1` → `?? 10` → 「老后端不下发单次批量」；
 *   `Math.max(0, …)` → 直接取值 → 「负的剩余量夹到 0」；
 *   `Math.ceil` → `Math.floor` → 「向上取整」与「满配 999 注」两行。
 */
