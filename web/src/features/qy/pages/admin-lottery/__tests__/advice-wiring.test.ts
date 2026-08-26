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

const here = dirname(fileURLToPath(import.meta.url))

function source(...parts: string[]): string {
  return readFileSync(join(here, '..', ...parts), 'utf8')
}

const wizard = source('components', 'lottery-create-wizard.tsx')
const seriesPanel = source('components', 'lottery-series-panel.tsx')
const draft = source('lib', 'draft.ts')

/**
 * 推荐值这套东西**接在了该接的地方**。
 *
 * `lib/__tests__/advice.test.ts` 已经证明了那几个函数算得对、而且算出来的数
 * 能过校验。它证不了的是接线：一个算得再对的推荐值，如果按钮按下去写进去的
 * 是另一个数、或者字段旁边的红字用的是另一条判据，运营看到的仍然是"界面说
 * OK、提交被拒"。
 *
 * 接线这件事没有类型可以守（都是 number），也没有运行期信号 —— 所以按源码守。
 * 下面每一条断言的都是"这一处引用的是那个共享函数"，而不是某段代码长什么样。
 */

describe('自动填写进去的就是算出来的那个数', () => {
  test('单份 / 份数两颗按钮各自写的是对应的下限函数', () => {
    // 按钮的 onApply 必须把 `qyLotTierAmountFloor` / `qyLotTierCountFloor`
    // 的返回值直接写进草稿。写一个常量、或者写另一个字段，运营点完还是被拒。
    // 数**两支**：概率制与双色球各一颗，它们是被点名那条报错的两半玩法。
    // 只断言"至少有一处"会让"改坏其中一支"这种变异存活 —— 实测如此。
    assert.equal(
      wizard.match(/amount_quota:\s*qyLotTierAmountFloor\(/g)?.length,
      2,
      '「自动填」没有把单份下限写进两支的 amount_quota'
    )
    assert.equal(
      wizard.match(/count:\s*qyLotTierCountFloor\(/g)?.length,
      2,
      '「自动填」没有把份数下限写进两支的 count'
    )
  })

  test('最低成场人数填的是保本人数，不是一个拍脑袋的常量', () => {
    assert.ok(
      /min_entries_to_hold:\s*minEntriesAdvice/.test(wizard),
      '最低成场人数的自动填没有接到 qyLotRecommendedMinEntries'
    )
    assert.ok(wizard.includes('qyLotRecommendedMinEntries('))
  })

  test('竞猜单注上限**没有**自动填 —— 这一格是运营决策，不是解出来的唯一解', () => {
    // 上一版这里是「单注额 × 20」，理由写着"一个大户最多顶 20 个普通参与者"。
    // 那句话是假的：`bet_max_quota` 约束的是一笔投注，而每人参与上限填 0 就是
    // 不限，同一个人开 20 笔顶格投注就是 400 个普通参与者的量。
    // 后端 applyBetBounds 对这一格也没有任何可解的不等式（只有三条"不得超过"），
    // 所以任何推荐值都只能是一个凭空选的常数。
    //
    // 按源码守"没有自动填"这件事，是因为它没有别的信号：一个被重新加回来的
    // 推荐值在类型、运行期、快照上全都是合法的。
    // 全文只许有**一处**往 bet_max_quota 里写值，就是这一格输入框自己的
    // onChange。多出来的那一处必然是某种"替运营填一个数"，而这一格没有任何
    // 算得出来的数可填。
    assert.deepEqual(
      wizard.match(/bet_max_quota:\s*\S+/g),
      ['bet_max_quota: quota'],
      '单注上限又被接上了自动填 —— 先去 lib/advice.ts 读那一段为什么不能有'
    )
    assert.ok(
      !wizard.includes('RecommendedBetMax') &&
        !wizard.includes('BET_MAX_MULTIPLE') &&
        !wizard.includes('qy_lot_advice_bet_max'),
      '推荐倍数被重新引进了向导'
    )
    // 拿掉推荐值不等于拿掉说明：范围与后果两行必须还在，否则这一格退回到
    // "一个没有任何提示的 0"，而 0 = 不限。
    assert.ok(
      wizard.includes("t('qy_lot_range_bet_max')") &&
        wizard.includes("t('qy_lot_bet_max_zero_note')"),
      '单注上限的范围/零值提示被一并删掉了'
    )
  })

  test('保本人数带着全场票数上界算 —— 算得出来但达不到的不是推荐值', () => {
    assert.ok(
      /qyLotRecommendedMinEntries\(draft,\s*entriesCap\)/.test(wizard),
      '保本人数没有带上 entriesCap：那会一键造出一场必然流局的活动'
    )
  })
})

describe('实时提示与提交校验用同一条判据', () => {
  test('两处都调 qyLotTierBudgetShort，没有人就地再写一遍不等式', () => {
    assert.ok(
      wizard.includes('qyLotTierBudgetShort('),
      '字段旁边的红字没有走共享判据'
    )
    assert.ok(
      draft.includes('qyLotTierBudgetShort('),
      '提交前的跨步校验没有走共享判据'
    )
    // 就地写的不等式是这条同源性唯一的敌人：它不会有任何编译期信号，
    // 而漂移之后界面与提交会给出两个答案。
    for (const [name, text] of [
      ['向导', wizard],
      ['跨步校验', draft],
    ] as const) {
      assert.ok(
        !/amount_quota\s*\*\s*(tier\.)?count\s*<\s*entriesCap/.test(text),
        `${name} 里又出现了就地写的预算不等式`
      )
    }
  })
})

describe('系列的发行上限：两道上限分开、而且都在提交之前拦住', () => {
  test('物理上限与策略上限各有一条错误，不合成一句', () => {
    for (const key of [
      'qy_lot_v_ball_cap_over_physical',
      'qy_lot_v_ball_cap_over_policy',
    ]) {
      assert.ok(
        seriesPanel.includes(`errors.push('${key}')`),
        `${key} 没有进 errors —— 字段旁边说"超了"而创建按钮照样能点，运营点下去吃一个 400`
      )
    }
  })

  test('念出来的上限用的是边界值展示，不是账本口径', () => {
    // 账本口径最多 4 位小数（≥1 时只有 2 位），把 2147483647 印成 $4,294.97 ——
    // 比真上限多 1353 额度。运营照着界面上那行字填，四步全绿、后端 400。
    for (const [name, text] of [
      ['创建向导', wizard],
      ['系列面板', seriesPanel],
    ] as const) {
      assert.ok(
        !text.includes('formatQyQuotaLedger(systemMax)'),
        `${name} 的系统上界还在用账本口径印，照着填会被后端拒`
      )
      assert.ok(
        text.includes('formatQyQuotaBound('),
        `${name} 没有用边界值展示`
      )
    }
    // 推荐值同理，只是方向相反：印矮了会让照着填的人撞上界面自己的红字。
    assert.ok(
      !/formatQyQuotaLedger\(\s*qyLotTierAmountFloor/.test(wizard),
      '单份推荐值还在用账本口径印，照着填回去比推荐值还小'
    )
  })

  test('物理上限的那个数来自后端下发，不是前端写死的常量', () => {
    assert.ok(
      seriesPanel.includes('yaml_readonly.system_max_quota'),
      '系列面板没有读后端下发的物理上限'
    )
    // 2147483647 / 4294967294 一旦被抄进前端，后端某天改了口径而界面还在
    // 教人填旧的那个数。
    assert.ok(
      !seriesPanel.includes('2147483647') &&
        !seriesPanel.includes('2_147_483_647'),
      '系列面板里抄了一份 int32 上界的常量'
    )
    assert.ok(
      !wizard.includes('2147483647') && !wizard.includes('2_147_483_647'),
      '创建向导里抄了一份 int32 上界的常量'
    )
  })
})
