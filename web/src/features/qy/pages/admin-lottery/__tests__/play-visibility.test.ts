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

/**
 * 「找不到双色球」这条反馈的守卫（需求 6）。
 *
 * 双色球的前端一直都是完整的 —— 选号器、奖档、期次系列、验证脚本一个不缺。
 * 项目方反馈「没有发现"双色球"活动 UI 界面和配置活动界面」，原因**纯粹是层级**：
 * 要先在「类型」里选「抽奖」，再在二级的「摇号方式」里选「双色球」。两层之下的
 * 东西等于不存在，而这件事写不进任何类型或运行期断言 —— 把 `<SelectItem
 * value='ball'>` 塞回二级下拉，typecheck 与全部单测都会继续全绿。
 *
 * 所以这里按源码守两条：一级选择里三个玩法并列、二级下拉里没有双色球。
 * 同理，大厅在一条活动都没有时必须给管理员一个"去创建"的落点 ——
 * 那正是这次"功能全都写完了、看起来却像没做"的另一半原因。
 */

// __tests__ → admin-lottery → pages
const pagesDir = join(dirname(fileURLToPath(import.meta.url)), '..', '..')

const wizard = readFileSync(
  join(pagesDir, 'admin-lottery', 'components', 'lottery-create-wizard.tsx'),
  'utf8'
)
const hall = readFileSync(
  join(pagesDir, 'lottery', 'components', 'lottery-hall-list.tsx'),
  'utf8'
)
const card = readFileSync(
  join(pagesDir, 'lottery', 'components', 'lottery-activity-card.tsx'),
  'utf8'
)

describe('创建向导第一步：三个玩法并列', () => {
  test('双色球不再是二级下拉里的一个选项', () => {
    assert.ok(
      !wizard.includes("value='ball'"),
      '双色球回到「摇号方式」下拉里 = 又埋回两层之下，正是被投诉的那个形状'
    )
  })

  test('三张一级卡片各出现一次', () => {
    for (const label of [
      "t('qy_lot_kind_draw')",
      "t('qy_lot_kind_guess')",
      "t('qy_lot_mode_ball')",
    ]) {
      assert.ok(wizard.includes(label), `一级选择里缺少 ${label}`)
    }
  })

  test('玩法切换走带归位规则的投影函数', () => {
    // 直接 patch `kind` 会留下 `draw_mode='ball'` 或一个孤儿 `series_no`，
    // 而后端对带期次的普通抽奖是拒绝 —— 界面上看不出请求为什么失败。
    assert.ok(wizard.includes('qyLotDraftForPlay'))
  })
})

describe('大厅空态（需求 6）', () => {
  test('空态按「进行中 / 已结束」分叉，不是一句通用的「暂无活动」', () => {
    // 写同一句话，用户会以为自己点错了标签：「这一阵没开新场」与「这个玩法
    // 从来没跑过一次」是两个完全不同的结论。
    assert.ok(hall.includes("'qy_lot_empty_open_title'"))
    assert.ok(hall.includes("'qy_lot_empty_done_title'"))
  })

  test('管理员在空态上拿得到「去创建」的落点', () => {
    assert.ok(hall.includes('emptyAction'), '空态必须能挂动作')
    assert.ok(
      hall.includes("to='/qy/admin/lottery'"),
      '一条活动都没有时，这是运营唯一能被指向创建入口的地方'
    )
    // 普通用户点进去只会吃一个 403，所以这颗按钮必须挂在角色判定后面。
    assert.ok(hall.includes('ROLE.ADMIN'))
  })
})

describe('双色球卡片带着它自己的三个数（需求 6）', () => {
  test('期号、号池、当期可派发奖池都在卡片上', () => {
    for (const key of [
      'qy_lot_ball_issue_no',
      'qy_lot_ball_pool_desc',
      'pool_open_quota',
    ]) {
      assert.ok(card.includes(key), `双色球卡片缺少 ${key}`)
    }
  })

  test('奖池读的是 pool_open_quota 而不是本期投注额', () => {
    // 滚存几期之后两者能差一个数量级，而它正是用户用来决定要不要参与的那个数。
    assert.ok(card.includes('isBall ? (activity.pool_open_quota ?? 0)'))
  })
})
