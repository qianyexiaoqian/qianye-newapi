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
 * 统计窗口「不限期限」哨兵在前端的三条口径。
 *
 * 它们各自守一件事：
 *
 *  1. **哨兵值必须与后端同值**。后端把 -1 原样下发，前端认错值的表现是界面上
 *     出现「-1 小时内累计」——而更糟的一种错法是把它折成 24，那是一句读起来
 *     完全正常的假话。
 *  2. **窗口变长的判据不能是裸的 `>`**。哨兵是 -1，比任何正数都小，于是
 *     「24 小时 → 不限期限」这个最激进的改动会被判成放宽：二次确认与影响面
 *     预览一起被跳过，一批存量账号在管理员毫不知情的情况下越线。
 *  3. **表单两格折成库里一列**。表单态里小时数与开关并存（取消勾选时要能把
 *     填过的小时数原样还回来），但提交时只能有一个数字 —— 不然会出现
 *     「不限期限 + 72 小时」这种库里根本表达不了的组合。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
  qyCategoryFormToPayload,
  qyCategoryTightens,
  qyCategoryToForm,
  qyEmptyCategoryForm,
  qyValidateCategoryForm,
} from '../../pages/admin-violation-categories/lib/category-form'
import type { QyViolationCategory } from '../../pages/admin-violation-categories/types'
import {
  QY_WINDOW_UNLIMITED,
  qyThresholdStateKey,
  qyWindowEffectiveHours,
  qyWindowIsUnlimited,
  qyWindowWidens,
} from '../violation-thresholds'

describe('不限期限哨兵', () => {
  test('取值是 -1，与后端 violation.WindowUnlimited 同值', () => {
    // 这个数字写在两处（Go 的 WindowUnlimited、这里的 QY_WINDOW_UNLIMITED），
    // 没有编译期把它们绑在一起，所以它值得一条断言：改错一侧的表现不是报错，
    // 而是界面上出现一个 -1 小时的窗口。
    assert.equal(QY_WINDOW_UNLIMITED, -1)
  })

  test('只有恰好等于哨兵才算不限期限', () => {
    const cases: [number | undefined, boolean][] = [
      [-1, true],
      [24, false],
      [0, false],
      // -2 不是哨兵。后端对它的处理是回落 24 小时（保守方向），前端跟着读成
      // 有限窗口 —— 两侧一致才不会出现"界面说不限、后端按 24 算"。
      [-2, false],
      [undefined, false],
    ]
    for (const [hours, want] of cases) {
      assert.equal(qyWindowIsUnlimited(hours), want, `hours=${String(hours)}`)
    }
  })

  test('生效小时数：0 与无法识别的负数回落 24，哨兵原样', () => {
    const cases: [number | undefined, number][] = [
      [1, 1],
      [24, 24],
      [72, 72],
      [0, 24],
      [-2, 24],
      [undefined, 24],
      [-1, -1],
    ]
    for (const [hours, want] of cases) {
      assert.equal(
        qyWindowEffectiveHours(hours),
        want,
        `hours=${String(hours)}`
      )
    }
  })
})

describe('窗口变长的判据', () => {
  test('无限排在所有有限值之上，两个方向都要对', () => {
    const cases: [number, number, boolean, string][] = [
      [24, 72, true, '常规变长'],
      [72, 24, false, '常规变短'],
      [24, 24, false, '没变'],
      [
        24,
        QY_WINDOW_UNLIMITED,
        true,
        '24 → 不限期限是最激进的一种放大，裸的 > 会把它判成放宽',
      ],
      [24 * 90, QY_WINDOW_UNLIMITED, true, '上界 → 不限期限仍然是放大'],
      [QY_WINDOW_UNLIMITED, 24, false, '不限期限 → 24 是止损，不该拦'],
      [QY_WINDOW_UNLIMITED, QY_WINDOW_UNLIMITED, false, '没变'],
      [0, 24, false, '0 与 24 都读成 24'],
      [0, QY_WINDOW_UNLIMITED, true, '0 → 不限期限是放大'],
    ]
    for (const [before, next, want, why] of cases) {
      assert.equal(qyWindowWidens(before, next), want, why)
    }
  })

  test('裸比较会判错这一格——这条断言就是那个反例', () => {
    // 变异验证：如果哪天有人把 qyWindowWidens 换回 `next > before`，
    // 下面这一行会变成 false，而 24 → 不限期限的二次确认会被静默跳过。
    assert.equal(QY_WINDOW_UNLIMITED > 24, false)
    assert.equal(qyWindowWidens(24, QY_WINDOW_UNLIMITED), true)
  })
})

describe('违规类型表单：两格折成一列', () => {
  const cat = (over: Partial<QyViolationCategory>): QyViolationCategory =>
    ({
      id: 1,
      key: 'spam',
      name: 'n',
      remark: '',
      public_title: '',
      public_desc: '',
      ai_guidance: '',
      ai_excluded: false,
      published: false,
      enabled: true,
      window_hours: 24,
      threshold: 10,
      sort_order: 100,
      is_fallback: false,
      created_at: 0,
      updated_at: 0,
      ...over,
    }) as QyViolationCategory

  test('勾了不限期限就提交哨兵，小时数一律忽略', () => {
    const values = {
      ...qyEmptyCategoryForm(),
      key: 'spam',
      name: 'n',
      window_hours: '72',
      window_unlimited: true,
    }
    assert.equal(
      qyCategoryFormToPayload(values, false).window_hours,
      QY_WINDOW_UNLIMITED,
      '库里只有一列，"不限期限 + 72 小时"这种组合不存在'
    )
    assert.equal(
      qyCategoryFormToPayload(
        { ...values, window_unlimited: false },
        false
      ).window_hours,
      72,
      '取消勾选之后必须把填过的小时数发出去，而不是回落到一个硬编码的 24'
    )
  })

  test('回填：不限期限的行不能把 -1 塞进输入框', () => {
    const form = qyCategoryToForm(cat({ window_hours: QY_WINDOW_UNLIMITED }))
    assert.equal(form.window_unlimited, true)
    assert.equal(
      form.window_hours,
      '24',
      '输入框里出现 -1 会让管理员以为这是一个可以编辑的小时数'
    )

    const finite = qyCategoryToForm(cat({ window_hours: 72 }))
    assert.equal(finite.window_unlimited, false)
    assert.equal(finite.window_hours, '72')
  })

  test('勾了不限期限时，空的小时数不该挡住保存', () => {
    const t = ((key: string) => key) as never
    const base = {
      ...qyEmptyCategoryForm(),
      key: 'spam',
      name: 'n',
      window_hours: '',
    }
    assert.equal(
      qyValidateCategoryForm({ ...base, window_unlimited: false }, t),
      'qy_vcat_err_window_range'
    )
    assert.equal(
      qyValidateCategoryForm({ ...base, window_unlimited: true }, t),
      null,
      '小时数框已经不参与提交了，还拦着等于给出一句改不掉的报错'
    )
  })

  test('"会不会扩大处置面"认折叠后的窗口', () => {
    const before = cat({ window_hours: 24 })
    const form = (over: Record<string, unknown>) => ({
      ...qyEmptyCategoryForm(),
      enabled: true,
      threshold: '10',
      window_hours: '24',
      ...over,
    })
    assert.equal(
      qyCategoryTightens(before, form({ window_unlimited: true })),
      true,
      '勾上不限期限必须触发二次确认：它让一年前的命中重新算数'
    )
    assert.equal(
      qyCategoryTightens(
        cat({ window_hours: QY_WINDOW_UNLIMITED }),
        form({ window_unlimited: false })
      ),
      false,
      '把不限期限收回 24 小时是止损，不该多要一次确认'
    )
  })
})

describe('管理端类型列表的阈值那一格', () => {
  test('不限期限时换成不带小时数的那一句', () => {
    assert.equal(
      qyThresholdStateKey('active', 24),
      'qy_vcat_threshold_value'
    )
    assert.equal(
      qyThresholdStateKey('active', QY_WINDOW_UNLIMITED),
      'qy_vcat_threshold_value_unlimited'
    )
    assert.equal(
      qyThresholdStateKey('disabled', QY_WINDOW_UNLIMITED),
      'qy_vcat_threshold_disabled_unlimited'
    )
    // 「还没配」那一句本来就不提窗口，不需要分叉 —— 分叉出来的第二个键
    // 会与第一个一字不差，而两个一样的键是下一次改文案时漏改一个的入口。
    assert.equal(
      qyThresholdStateKey('unset', QY_WINDOW_UNLIMITED),
      'qy_vcat_threshold_unset'
    )
  })

  test('省略窗口参数时按有限窗口读（旧调用点不会静默变成无限）', () => {
    assert.equal(qyThresholdStateKey('active'), 'qy_vcat_threshold_value')
  })
})
