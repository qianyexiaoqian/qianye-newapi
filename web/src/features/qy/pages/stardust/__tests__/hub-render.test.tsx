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
 * 星屑用户端选择夹（余额 / 流水 / 待结算）在**真实渲染**上的形状。
 *
 * # 这里守的三件事
 *
 *  1. 余额那一屏把后端下发的东西**原样**摆出来：大数字 + 单位名、暂缓原因的那句
 *     人话、下次结算时刻（后端算的 `next_settle_at`，前端不复刻日界）、昨日摘要里
 *     "消费 X 额度 → 已发 Y 星屑"两种单位各印各的。
 *  2. 不可见的标签一个请求都不发：一进页面只有余额那一张标签自己的两条
 *     （`/stardust/me` + `/stardust/forecast`）；切到流水才打 `/stardust/ledger`，
 *     而且 hash 写进地址栏。
 *  3. 流水与日桶的枚举各有一句人话（kind 十二种、状态三种、暂缓原因三种）；
 *     键漏译时 i18next 原样吐键名，下面的中文断言当场变红。
 *
 * 文案来自 `zh.json`,期望值在本文件手写。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { formatTimestampToDate } from '@/lib/format'

import {
  ROLE,
  cleanupQyStardustScreens,
  mountQyStardustScreen,
  setQyProbeRole,
  zh,
  type QyProbeRequest,
} from '../../../__tests__/stardust-screen'
import { qyTabHash } from '../../../lib/pages'
import { QyStardustHub } from '../hub'

after(cleanupQyStardustScreens)

const ME = {
  name: '星屑',
  quota_per_unit: 500_000,
  balance: {
    available: 1234,
    total_earned: 2000,
    total_spent: 800,
    total_refunded: 34,
    total_adjusted: 0,
    carry: '0.3700000000',
    hold_reason: 'overdraft',
  },
  next_settle_at: 1_800_000_000,
  yesterday: {
    bucket_date: '20260903',
    base_quota: 3_700_000,
    gross: '7.4000000000',
    status: 'settled',
    settled_amount: 7,
    hold_reason: '',
    rate_bps: 10_000,
    quota_per_unit: 500_000,
  },
  pending_held_count: 2,
}

/** 明日预计到账。这里只要它能渲染出来；口径与刷新在 forecast-card.test.tsx。 */
const FORECAST = {
  day: '20260904',
  settle_at: 1_800_000_000,
  computed_at: 1_787_000_000,
  refresh_after: 1_787_000_060,
  expires_at: 1_787_003_600,
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
    applies: false,
    counted: true,
    base_quota: 0,
    rate_bps: 0,
    gross: '0',
    carry: '0',
    estimated: 0,
  },
  estimated_total: 2,
}

const LEDGER = [
  {
    ledger_no: 'SDL-0001',
    kind: 'consume_rebate',
    amount: 7,
    balance_after: 1234,
    ref_type: 'settle',
    ref_no: '20260904',
    act_no: '',
    peer_user_id: 0,
    remark: '',
    created_at: 1_787_000_000,
  },
  {
    ledger_no: 'SDL-0002',
    kind: 'lot_stake',
    amount: -40,
    balance_after: 1227,
    ref_type: 'lot_entry',
    ref_no: 'LE-9',
    act_no: 'LT-9',
    peer_user_id: 0,
    remark: '',
    created_at: 1_787_000_100,
  },
]

const ACCRUALS = [
  {
    bucket_date: '20260901',
    user_group: 'default',
    rate_bps: 10_000,
    quota_per_unit: 500_000,
    base_quota: 1_000_000,
    gross: '2.0000000000',
    status: 'held',
    hold_reason: 'overdraft',
    ledger_id: 0,
    computed_at: 1_787_000_000,
    settled_at: 0,
  },
  {
    bucket_date: '20260903',
    user_group: 'default',
    rate_bps: 10_000,
    quota_per_unit: 500_000,
    base_quota: 3_700_000,
    gross: '7.4000000000',
    status: 'settled',
    hold_reason: '',
    ledger_id: 1,
    computed_at: 1_787_000_000,
    settled_at: 1_787_090_000,
  },
]

function respond(request: QyProbeRequest) {
  if (request.url.endsWith('/stardust/me')) return { data: ME }
  if (request.url.endsWith('/stardust/forecast')) return { data: FORECAST }
  if (request.url.endsWith('/stardust/ledger')) {
    const items =
      request.params.kind == null
        ? LEDGER
        : LEDGER.filter((row) => row.kind === request.params.kind)
    return { data: { items, total: items.length, page: 1, page_size: 20 } }
  }
  if (request.url.endsWith('/stardust/accruals')) {
    return {
      data: { items: ACCRUALS, total: ACCRUALS.length, page: 1, page_size: 20 },
    }
  }
  return undefined
}

async function mountHub(hash?: string) {
  await setQyProbeRole(ROLE.USER)
  return mountQyStardustScreen({
    element: <QyStardustHub />,
    path: '/qy/stardust',
    initial: hash == null ? '/qy/stardust' : `/qy/stardust#${hash}`,
    respond,
  })
}

/** 只数 qy 的请求路径（上游会话探测不算）。 */
function qyUrls(sent: QyProbeRequest[]): string[] {
  return sent
    .filter((row) => row.url.startsWith('/api/qy/'))
    .map((row) => row.url)
}

describe('余额标签', () => {
  test('一进页面只打余额那一张标签的两条；大数字、单位名、四个累计与余数都在屏幕上', async () => {
    const screen = await mountHub()

    assert.deepEqual(qyUrls(screen.sent), [
      '/api/qy/stardust/me',
      // 明日预计到账自己取数：它在服务端一小时一份，塞进 /stardust/me 会让
      // 每一次进页面都拖一次 LOG_DB 聚合。
      '/api/qy/stardust/forecast',
    ])
    const text = screen.text()
    assert.ok(text.includes('1,234 星屑'), `大数字没带单位名渲染出来：${text}`)
    assert.ok(text.includes('2,000 星屑'), '累计获得没渲染')
    assert.ok(text.includes('800 星屑'), '累计支出没渲染')
    assert.ok(
      text.includes('0.3700000000 星屑'),
      '结转余数必须原样展示 decimal 字符串'
    )
  })

  test('暂缓原因渲染成那句人话，而不是 overdraft 这个枚举值', async () => {
    const screen = await mountHub()
    const text = screen.text()
    assert.ok(
      text.includes(zh.qy_sd_hold_overdraft),
      '缺「待复核(账户余额为负)」'
    )
    assert.ok(!text.includes('qy_sd_hold_'), '暂缓原因的 i18n 键没翻译')
  })

  test('下次结算时刻是后端下发的那个瞬间的格式化，不是前端自己算的日界', async () => {
    const screen = await mountHub()
    assert.ok(
      screen.text().includes(formatTimestampToDate(ME.next_settle_at)),
      '下次结算时刻没按 next_settle_at 渲染'
    )
  })

  test('昨日摘要：消费基数按额度印、已发按星屑印、暂缓天数带着去待结算的链接', async () => {
    const screen = await mountHub()
    const text = screen.text()
    // 计提 7.4、已发 +7：两个数都要在，否则"为什么 7.4 只发了 7"没有答案。
    assert.ok(text.includes('7.4000000000 星屑'), '计提 gross 没渲染')
    assert.ok(text.includes('+7 星屑'), '已发金额没按带符号的星屑渲染')
    assert.ok(
      text.includes(zh.qy_sd_accrual_st_settled),
      '昨日桶的状态徽章没翻译'
    )
    assert.ok(
      text.includes('有 2 天的消费返被暂缓'),
      'pending_held_count 没渲染成那句提示'
    )
    // 基数 3,700,000 额度是 $7.40：它必须走额度口径，绝不能印成 3,700,000 星屑。
    assert.ok(!text.includes('3,700,000 星屑'), '消费基数被当成星屑渲染了')
  })
})

describe('流水标签', () => {
  test('切到流水：只发 /stardust/ledger、hash 写进地址栏、kind 渲染成人话', async () => {
    const screen = await mountHub()
    screen.sent.length = 0

    assert.ok(
      await screen.click(zh.qy_nav_stardust_ledger),
      '标签栏上没有「流水」'
    )

    assert.deepEqual(qyUrls(screen.sent), ['/api/qy/stardust/ledger'])
    assert.equal(
      screen.router.state.location.hash,
      qyTabHash('/qy/stardust-ledger')
    )
    const text = screen.text()
    // 「种类」列改成了图标(task-C-visual):文字进 aria-label / title,
    // 读屏念的还是那句人话。原先断 `text.includes(种类文案)` 会被筛选下拉里的
    // <option> 蒙混过去 —— 它测的是"这个词在页面上",不是"这一行有没有说清种类"。
    const kindCells = [
      ...document.body.querySelectorAll<HTMLElement>('[role="img"][data-kind]'),
    ]
    assert.deepEqual(
      kindCells.map((node) => node.getAttribute('aria-label')),
      [zh.qy_sd_kind_consume_rebate, zh.qy_sd_kind_lot_stake],
      '每一行的种类图标必须带着翻译好的可访问名称'
    )
    assert.ok(!text.includes('qy_sd_kind_'), '种类的 i18n 键没翻译')
    assert.ok(text.includes('+7 星屑'), '入账金额没带 + 号')
    assert.ok(text.includes('-40 星屑'), '扣减金额没带 - 号')
    assert.ok(text.includes('LT-9'), '活动号没渲染')
    assert.ok(text.includes('SDL-0001'), '流水号没渲染')
  })

  test('按 kind 筛选会把 kind 带进请求，列表只剩那一种', async () => {
    const screen = await mountHub(qyTabHash('/qy/stardust-ledger'))
    screen.sent.length = 0

    assert.ok(
      await screen.select(zh.qy_sd_col_kind, 'lot_stake'),
      '找不到种类下拉'
    )

    const asked = screen.sent.filter((row) =>
      row.url.endsWith('/stardust/ledger')
    )
    assert.equal(asked.length, 1)
    assert.equal(asked[0].params.kind, 'lot_stake')
    assert.equal(asked[0].params.page, 1, '换筛选必须回到第一页')
    const text = screen.text()
    assert.ok(text.includes('SDL-0002'))
    assert.ok(!text.includes('SDL-0001'), '筛掉的那一种还在列表里')
  })
})

describe('待结算标签', () => {
  test('日桶按状态上徽章，被扣住的那一天旁边写着原因', async () => {
    const screen = await mountHub(qyTabHash('/qy/stardust-accruals'))

    assert.deepEqual(
      qyUrls(screen.sent),
      ['/api/qy/stardust/accruals'],
      '带 hash 打开时仍然先问了第一张标签'
    )
    const text = screen.text()
    assert.ok(text.includes('2026-09-01'), '桶日没按 YYYY-MM-DD 渲染')
    assert.ok(text.includes(zh.qy_sd_accrual_st_held), 'held 没翻译')
    assert.ok(text.includes(zh.qy_sd_accrual_st_settled), 'settled 没翻译')
    assert.ok(text.includes(zh.qy_sd_hold_overdraft), '被扣住的那一天没写原因')
    assert.ok(text.includes('2.0000000000 星屑'), 'gross 没带单位渲染')
  })
})
