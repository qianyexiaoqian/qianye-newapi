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

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

import { qyLotBoundContains, qyLotIsUnlimitedZero } from '../lib/bounds'
import {
  qyLotDraftToInput,
  qyLotEmptyDraft,
  qyLotTotalPrizeQuota,
  qyLotValidateDraft,
  type QyLotDraft,
} from '../lib/draft'
import type { QyLotYamlReadonly } from '../types'

/**
 * 额度硬上限放开之后，界面这一侧必须同时成立的两件事。
 *
 * ## 放开的那些
 *
 * `max_stake_quota` / `max_total_prize_quota` 的 **0 = 不限制**，而且是默认值。
 * 界面若还照着一个不存在的上限标红，运营看到的仍然是「填不进去」，
 * 后端改了等于没改。
 *
 * ## 换上去的那个
 *
 * 越过二次确认阈值时，创建请求必须带上**精确金额**的回执。而这份回执只能在
 * 运营真的勾过那个不可逆确认框之后才产生 —— 无条件回填等于让这道确认自我满足，
 * 那样它一行代码都没少写，却什么都没拦住。所以这里既按契约守（默认必须是 0），
 * 也按源码守（向导里只有 `needsNetIssueConfirm` 成立时才把总额传进去）。
 */

const wizard = readFileSync(
  join(
    dirname(fileURLToPath(import.meta.url)),
    '..',
    'components',
    'lottery-create-wizard.tsx'
  ),
  'utf8'
)

/** 一份除了金额之外全部合法的名次制草稿。 */
function drawDraft(amountQuota: number, count = 1): QyLotDraft {
  const base = qyLotEmptyDraft(500)
  const now = Math.floor(Date.now() / 1000)
  return {
    ...base,
    kind: 'draw',
    draw_mode: 'rank',
    title: 'qy-测试活动',
    stake_quota: 1000,
    open_at: now + 600,
    close_at: now + 7200,
    draw_at: now + 14400,
    max_total_entries: 100,
    tiers: [{ tier: 1, name: '一等奖', amount_quota: amountQuota, count }],
  }
}

const yaml = {
  max_total_entries_hard: 50000,
  max_prize_tiers: 10,
  max_options: 12,
  reveal_delay_seconds: 60,
  spend_ready_from: 20240101,
  spend_max_lookback_days: 90,
  // 0 = 不限，这是新的默认值。
  max_stake_quota: 0,
} as QyLotYamlReadonly

describe('净增发的二次确认（界面侧）', () => {
  test('奖品总额上限为 0 时，超大金额不再被标红', () => {
    // 5 亿额度 = $1000，旧默认硬顶（5000 万 = $100）的十倍。
    const draft = drawDraft(100_000_000, 5)
    assert.equal(qyLotTotalPrizeQuota(draft), 500_000_000)

    const errors = qyLotValidateDraft(draft, yaml, 0, 2000)
    assert.ok(
      !errors.includes('qy_lot_v_prize_over_cap'),
      '上限 0 必须当成「不限制」，否则运营在界面上仍然填不进去'
    )
  })

  test('站点自己配了硬顶时仍然标红，且边界是闭区间', () => {
    const atCap = qyLotValidateDraft(
      drawDraft(1_000_000),
      yaml,
      1_000_000,
      2000
    )
    assert.ok(
      !atCap.includes('qy_lot_v_prize_over_cap'),
      '恰好等于硬顶必须放行 —— 与后端 buildPrizes 的 `total > ceiling` 同一个边界'
    )

    const overCap = qyLotValidateDraft(
      drawDraft(1_000_001),
      yaml,
      1_000_000,
      2000
    )
    assert.ok(overCap.includes('qy_lot_v_prize_over_cap'))
  })

  test('参与费上限：0 不限，配了正数才拦', () => {
    // 5000 万额度 = $100，远超旧默认的 500 万（$10）。
    const draft = { ...drawDraft(1000), stake_quota: 50_000_000 }

    assert.ok(
      !qyLotValidateDraft(draft, yaml, 0, 2000).includes(
        'qy_lot_v_stake_over_cap'
      ),
      'max_stake_quota=0 时大额参与费必须能填'
    )
    assert.ok(
      qyLotValidateDraft(
        draft,
        { ...yaml, max_stake_quota: 5_000_000 },
        0,
        2000
      ).includes('qy_lot_v_stake_over_cap'),
      '站点配了硬顶就要在按钮之前说出来，而不是让运营走完四步吃一个 400'
    )
  })

  test('竞猜单注上限同样按 0 = 不限', () => {
    const guess: QyLotDraft = {
      ...drawDraft(1000),
      kind: 'guess',
      tiers: [],
      options: [
        { id: 'a', label: '甲队胜', is_catch_all: false },
        { id: 'b', label: '乙队胜', is_catch_all: true },
      ],
      bet_max_quota: 50_000_000,
    }

    assert.ok(
      !qyLotValidateDraft(guess, yaml, 0, 2000).includes(
        'qy_lot_v_bet_over_cap'
      )
    )
    assert.ok(
      qyLotValidateDraft(
        guess,
        { ...yaml, max_stake_quota: 5_000_000 },
        0,
        2000
      ).includes('qy_lot_v_bet_over_cap')
    )
  })

  test('提交体默认不带确认回执 —— 没勾过就等于没确认', () => {
    const input = qyLotDraftToInput(drawDraft(100_000_000, 5))
    assert.equal(
      input.confirm_net_issue_quota,
      0,
      '默认必须是 0：后端把 0 当成「未确认」，这个零值要落在安全的一侧'
    )
  })

  test('勾过之后回执必须是精确总额，不是阈值也不是一个布尔', () => {
    const draft = drawDraft(100_000_000, 5)
    const input = qyLotDraftToInput(draft, qyLotTotalPrizeQuota(draft))
    assert.equal(input.confirm_net_issue_quota, 500_000_000)
  })

  test('向导只在越过阈值时才把总额传下去', () => {
    // 这一条按源码守，因为它是**否定性**契约：无条件回填照样能让所有其它
    // 用例全绿（请求发得出去、后端也放行），而那正是这道确认失效的形状。
    assert.match(
      wizard,
      /const echo = needsNetIssueConfirm \? totalPrize : 0/,
      '回执必须由 needsNetIssueConfirm 把关，无条件回填等于这道确认自我满足'
    )
    assert.match(
      wizard,
      /alertQuota > 0 && totalPrize >= alertQuota/,
      '阈值判据必须是 >=，与后端 requireNetIssueConfirm 同源；' +
        '写成 > 时恰好等于阈值的那一场会界面不弹、提交吃 400'
    )
    assert.match(
      wizard,
      /irreversible=\{needsNetIssueConfirm\}/,
      '越过阈值的那一屏必须强制勾选 —— 一个可以直接按确认的弹窗不是二次确认'
    )
    assert.match(
      wizard,
      /qy_lot_net_issue_confirm_desc/,
      '确认框正文必须写出金额，否则运营要确认的到底是哪个数没人说'
    )
  })

  test('两处提示都长在「填的时候」那一屏上，不是只在复核屏', () => {
    // 「点保存才报错」是这次要消灭的体验之一。奖品那一屏有实时的
    // NetIssueMeter，参与费那一格有就地的超限提示 —— 少了任何一处，
    // 运营都要走完四步才第一次看到问题。
    assert.match(
      wizard,
      /<NetIssueMeter\s/,
      '奖档那一屏必须挂上实时的净增发提示'
    )
    assert.match(
      wizard,
      /stakeCap > 0 && draft\.stake_quota > stakeCap/,
      '参与费超过站点硬顶要就地说，而且 0 必须当成「不限」'
    )
  })

  test('确认与提示文案里必须真的有金额占位符', () => {
    // 「明确写出金额」是这次改造的核心要求。文案掉了 {{amount}} 之后，
    // 弹窗仍然弹、勾选仍然要勾、所有断言仍然绿 —— 而运营看到的是一句
    // 「金额较大，请确认」，与从前那道没有信息量的硬拒绝一模一样。
    const locales: Record<string, Record<string, string>> = {
      zh: zh as Record<string, string>,
      en: en as Record<string, string>,
    }
    const withAmount = [
      'qy_lot_net_issue_confirm_desc',
      'qy_lot_net_issue_meter_title',
      'qy_lot_large_prize_desc',
      'qy_lot_publish_net_issue_note',
    ]
    for (const [lang, dict] of Object.entries(locales)) {
      for (const key of withAmount) {
        assert.ok(dict[key] != null, `${lang} 缺少 ${key}`)
        assert.match(
          dict[key],
          /\{\{amount\}\}/,
          `${lang}.${key} 必须把金额插进去`
        )
      }
      assert.match(
        dict.qy_lot_net_issue_meter_needs_confirm,
        /\{\{threshold\}\}/,
        `${lang} 的阈值提示要说清阈值是多少`
      )
      assert.match(
        dict.qy_lot_net_issue_meter_over_cap,
        /\{\{cap\}\}/,
        `${lang} 的超硬顶提示要说清硬顶是多少`
      )
    }
  })
})

describe('配置页上的「0 = 不限」', () => {
  test('两个额度上限的 0 是「不限制」，别的 _quota 键不是', () => {
    // 一行写着「单场奖品总额上限 $0」的只读文字，任何人读到的都是
    // "一分钱都不许发"，而真实语义恰好相反 —— 这与项目方那句
    // 「怎么在抽奖设置这里不能超过 100 站点余额」是同一种误读，方向相反。
    assert.equal(qyLotIsUnlimitedZero('max_stake_quota', 0), true)
    assert.equal(qyLotIsUnlimitedZero('max_total_prize_quota', 0), true)

    // 同样以 `_quota` 结尾、但 0 的意思完全不同的两个：按后缀猜就会把它们
    // 一起说成「不限」，而 pay_password 的 0 是「任何金额都不验支付密码」，
    // 阈值的 0 是「连确认都不要」。
    assert.equal(qyLotIsUnlimitedZero('pay_password_threshold_quota', 0), false)
    assert.equal(qyLotIsUnlimitedZero('large_prize_alert_quota', 0), false)

    // 非零取值一律按金额渲染。
    assert.equal(qyLotIsUnlimitedZero('max_total_prize_quota', 1), false)
  })

  test('区间端点不走取值渲染', () => {
    // 下界 0 是一个**边界**不是取值：套上「不限」会得到
    // 「可填范围：不小于 不限」。这一条只能按源码守（`displayValue` 是页面私有
    // 的渲染细节，为它开一个导出反而会让人以为那是个契约），所以判据写成
    // **否定式**：整页里一次都不许出现「拿取值渲染去画区间端点」。
    // 否定式挡得住"只改了两处里的一处"——那正是肯定式匹配漏掉的形状。
    const page = readFileSync(
      join(
        dirname(fileURLToPath(import.meta.url)),
        '..',
        '..',
        'admin-lottery-config',
        'index.tsx'
      ),
      'utf8'
    )
    assert.ok(
      !/displayValue\(\s*props\.fieldKey,\s*props\.bound\./.test(page),
      '区间端点必须走 boundText，不能走带「0 = 不限」语义的 displayValue'
    )
    assert.equal(
      (page.match(/boundText\(props\.fieldKey, props\.bound\./g) ?? []).length,
      3,
      '三处区间端点（无上界的 min、有上界的 min 与 max）必须都还在'
    )
  })
})

describe('无上界的配置区间', () => {
  test('后端不下发 max 时按「没有上界」处理', () => {
    const unlimited = { min: 0, unlimited: true }
    assert.equal(qyLotBoundContains(unlimited, 0), true, '0 是合法取值')
    assert.equal(qyLotBoundContains(unlimited, Number.MAX_SAFE_INTEGER), true)
    assert.equal(qyLotBoundContains(unlimited, -1), false, '负数仍然不是取值')
  })

  test('有 max 时仍然是闭区间', () => {
    const bound = { min: 1, max: 100 }
    assert.equal(qyLotBoundContains(bound, 1), true)
    assert.equal(qyLotBoundContains(bound, 100), true)
    assert.equal(qyLotBoundContains(bound, 101), false)
    assert.equal(qyLotBoundContains(bound, 0), false)
  })
})
