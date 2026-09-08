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
 * 「明日预计到账」那张卡在真实渲染上的行为。
 *
 * # 这里守的四件事
 *
 *  1. **两条线各自摆全**：基数（额度口径）、比例、计提、结转余数、这一条预计到账。
 *     少摆一个，「我今天消费了一整天为什么预计还是 0」就没有答案 —— 答案通常是
 *     计提的零头还没攒够 1 颗，而那只有把 gross 与 carry 并排印出来才看得见。
 *  2. **手动刷新真的带 `refresh=1`**：不带就只是再拿一次服务端缓存里那一份，
 *     按钮变成一个什么都不做的装饰。
 *  3. **节流窗口内按钮按住**：服务端在 `refresh_after` 之前原样退回旧的那一份，
 *     不禁用的话用户按下去看到数字纹丝不动，会当成坏了。
 *  4. **三种"这条线不成立"各说各的**：账号暂缓（预计 0 + 那句人话）、下线消费返
 *     整条不适用（不出现）、下线太多没统计（出现但带一句说明）——"没统计"与
 *     "统计了、是 0"在界面上必须分得开。
 *
 * 文案来自 `zh.json`，期望值在本文件手写。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import {
  ROLE,
  cleanupQyStardustScreens,
  mountQyStardustScreen,
  setQyProbeRole,
  zh,
  type QyProbeRequest,
} from '../../../__tests__/stardust-screen'
import { QyStardustForecastCard } from '../components/forecast-card'
import type { QyStardustForecast } from '../types'

after(cleanupQyStardustScreens)

const NOW_SECONDS = Math.floor(Date.now() / 1000)

/**
 * 一份基线估算：消费返 1,000,000 额度 × 100% = 计提 2、加上余数 0.37 发 2 颗；
 * 下线消费返 500,000 额度 × 10% = 计提 0.1、加上余数 0.95 发 1 颗。合计 3。
 *
 * `refresh_after` 默认落在过去：绝大多数用例要的是"按钮可以按"。
 */
function forecast(patch: Partial<QyStardustForecast> = {}): QyStardustForecast {
  return {
    day: '20260906',
    settle_at: 1_800_000_000,
    computed_at: NOW_SECONDS - 600,
    refresh_after: NOW_SECONDS - 540,
    expires_at: NOW_SECONDS + 3_000,
    quota_per_unit: 500_000,
    hold_reason: '',
    consume: {
      base_quota: 1_000_000,
      rate_bps: 10_000,
      gross: '2.0000000000',
      carry: '0.3700000000',
      estimated: 2,
    },
    invite: {
      applies: true,
      counted: true,
      base_quota: 500_000,
      rate_bps: 1_000,
      gross: '0.1000000000',
      carry: '0.9500000000',
      estimated: 1,
    },
    estimated_total: 3,
    ...patch,
  }
}

async function mountCard(options: {
  first: QyStardustForecast
  refreshed?: QyStardustForecast
}) {
  await setQyProbeRole(ROLE.USER)
  return mountQyStardustScreen({
    element: <QyStardustForecastCard />,
    respond: (request: QyProbeRequest) => {
      if (!request.url.endsWith('/stardust/forecast')) return undefined
      if (request.params.refresh != null && options.refreshed != null) {
        return { data: options.refreshed }
      }
      return { data: options.first }
    },
  })
}

/** 卡片右上角那颗刷新按钮。禁用状态直接读属性，不靠"点了没反应"反推。 */
function refreshButton(): HTMLButtonElement | null {
  const node = [...document.body.querySelectorAll('button')].find(
    (item) => (item.textContent ?? '').trim() === zh.qy_common_refresh
  )
  return (node as HTMLButtonElement | undefined) ?? null
}

describe('明日预计到账', () => {
  test('两条线各自摆出基数、比例、计提、余数与预计，合计是那个大数字', async () => {
    const screen = await mountCard({ first: forecast() })

    const text = screen.text()
    assert.ok(text.includes('+3 星屑'), `合计没按带符号的星屑渲染：${text}`)
    // 基数是额度口径（$2.00 / $1.00），绝不能印成星屑。
    assert.ok(!text.includes('1,000,000 星屑'), '消费基数被当成星屑渲染了')
    assert.ok(text.includes('2.0000000000 星屑'), '消费返的计提没原样渲染')
    assert.ok(text.includes('0.3700000000 星屑'), '消费返的结转余数没渲染')
    assert.ok(text.includes('0.1000000000 星屑'), '下线消费返的计提没渲染')
    assert.ok(text.includes('0.9500000000 星屑'), '下线消费返的结转余数没渲染')
    assert.ok(text.includes('100%'), '消费返比例没渲染')
    assert.ok(text.includes('10%'), '下线消费返比例没渲染')
    assert.ok(text.includes('+2 星屑'), '消费返这一条的预计没渲染')
    assert.ok(text.includes('+1 星屑'), '下线消费返这一条的预计没渲染')
    assert.ok(!text.includes('qy_sd_fc_'), '这张卡的 i18n 键没翻译')
  })

  test('按刷新会带上 refresh=1，并把重算的那一份换到屏幕上', async () => {
    const screen = await mountCard({
      first: forecast(),
      refreshed: forecast({
        computed_at: NOW_SECONDS,
        refresh_after: NOW_SECONDS + 60,
        consume: {
          base_quota: 4_000_000,
          rate_bps: 10_000,
          gross: '8.0000000000',
          carry: '0.3700000000',
          estimated: 8,
        },
        estimated_total: 9,
      }),
    })
    screen.sent.length = 0

    assert.ok(await screen.click(zh.qy_common_refresh), '卡片上没有刷新按钮')

    const asked = screen.sent.filter((row) =>
      row.url.endsWith('/stardust/forecast')
    )
    assert.equal(asked.length, 1, '刷新必须只打一次')
    assert.equal(
      asked[0].params.refresh,
      1,
      '不带 refresh=1 只会拿回同一份缓存'
    )
    const text = screen.text()
    assert.ok(text.includes('+9 星屑'), `重算后的合计没换上去：${text}`)
    assert.ok(text.includes('8.0000000000 星屑'), '重算后的计提没换上去')
  })

  test('节流窗口内刷新按钮是禁用的', async () => {
    await mountCard({
      first: forecast({ refresh_after: NOW_SECONDS + 60 }),
    })

    const button = refreshButton()
    assert.ok(button != null, '卡片上没有刷新按钮')
    assert.ok(
      button.hasAttribute('disabled'),
      '服务端还不会重算，按钮却是可按的'
    )
  })

  test('账号被暂缓时预计为 0，并写明是哪一种暂缓', async () => {
    const base = forecast()
    const screen = await mountCard({
      first: forecast({
        hold_reason: 'overdraft',
        consume: { ...base.consume, estimated: 0 },
        invite: { ...base.invite, estimated: 0 },
        estimated_total: 0,
      }),
    })

    const text = screen.text()
    assert.ok(text.includes(zh.qy_sd_hold_overdraft), '没写明暂缓原因')
    assert.ok(!text.includes('qy_sd_hold_'), '暂缓原因的 i18n 键没翻译')
    // 计提照常摆出来：钱是算出来了的，只是要等账号恢复才发。
    assert.ok(text.includes('2.0000000000 星屑'), '暂缓时计提也必须摆出来')
  })

  test('下线消费返不适用时整条线不出现', async () => {
    const screen = await mountCard({
      first: forecast({
        invite: {
          applies: false,
          counted: true,
          base_quota: 0,
          rate_bps: 0,
          gross: '0',
          carry: '0',
          estimated: 0,
        },
        estimated_total: 2,
      }),
    })

    const text = screen.text()
    assert.ok(
      !text.includes(zh.qy_sd_fc_base_invitees),
      '这条线对我不成立，却摆了一行永远为 0 的数'
    )
    assert.ok(text.includes('+2 星屑'), '消费返那一条仍然要在')
  })

  test('下线太多没统计时那一行写清是"没统计"而不是"没有"', async () => {
    const base = forecast()
    const screen = await mountCard({
      first: forecast({
        invite: {
          ...base.invite,
          counted: false,
          base_quota: 0,
          gross: '0',
          estimated: 0,
        },
        estimated_total: 2,
      }),
    })

    assert.ok(
      screen.text().includes(zh.qy_sd_fc_invitees_too_many),
      '没统计与统计了是 0 在界面上分不开'
    )
  })
})
