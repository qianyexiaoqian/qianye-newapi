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
 * 「我的推广」选择夹（概览 / 下线 / 返星屑明细）在**真实渲染**上的形状（D-14）。
 *
 * # 这里守的四件事
 *
 *  1. 概览把五种 kind 的累计**逐项**摆出来，并且它们加起来就是那个大数字：
 *     少一种 kind 图例就少一行，而那一行对用户是"我这类返还去哪了"。昨日的
 *     「已返 / 暂缓」两个数都要在（只给已返，被暂缓的看起来像被吞了），合规门
 *     关着时那句话要在数字之前出现。
 *  2. 不可见的标签一个请求都不发：一进页面只有 `/invite/summary`；切到明细才
 *     打 `/invite/records`，按 kind 筛选把 `kind` 带进请求并回到第一页。
 *  3. 全部收益只有星屑：屏幕上凡是返还都带单位名印出来，下线消费基数按额度印，
 *     绝不能出现「佣金」「提现」两个词 —— 那两样东西已经不存在了。
 *  4. 上游那张「推荐计划」卡（主库 `users.aff_*`，星辉口径）只在还有存量
 *     `aff_quota` 时挂出来：它是存量清退通道，不是本站的邀请返利。存量为 0
 *     还挂着，用户会以为星屑之外另有一笔按星辉计的邀请奖励；反过来把卡焊死，
 *     老用户手里那笔 `aff_quota` 就再没有出口。两头都要钉。
 *
 * 文案来自 `zh.json` + `pending-invite.zh.json`（见 `__tests__/invite-screen.ts`）。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import {
  ROLE,
  cleanupQyStardustScreens,
  inviteZh as zh,
  mountQyInviteScreen,
  setQyProbeRole,
  type QyProbeRequest,
} from '../../../__tests__/invite-screen'
import { qyTabHash } from '../../../lib/pages'
import { QyInviteHub } from '../hub'

after(cleanupQyStardustScreens)

const SUMMARY = {
  invitee_count: 3,
  blocked_count: 1,
  totals: {
    invite_consume: 600,
    invite_topup: 300,
    invite_redeem: 100,
    invite_register: 200,
    plan_inviter: 34,
    all: 1234,
  },
  yesterday: { base_quota: 3_700_000, granted: 7, held: 2 },
  pending_today_base_quota: 1_000_000,
  rate: {
    group: 'vip',
    invite_consume_bps: 500,
    invite_topup_bps: 1000,
    invite_redeem_bps: 0,
    invite_register_stardust: 50,
  },
  compliance_confirmed: false,
  day_offset_minutes: 480,
}

const RECORDS = [
  {
    ledger_no: 'SDL-1001',
    kind: 'invite_consume',
    amount: 6,
    invitee_id: 42,
    invitee_masked: 'zh***ng',
    ref_no: '20260903',
    remark: '',
    created_at: 1_787_000_000,
  },
  {
    ledger_no: 'SDL-1002',
    kind: 'invite_topup',
    amount: 30,
    invitee_id: 0,
    invitee_masked: '',
    ref_no: 'TP-9',
    remark: '',
    created_at: 1_787_000_100,
  },
]

/**
 * 主库 `users.aff_*` 三个数字。默认 0 = 上游注册奖已按 design-15 D-G ① 置 0、
 * 这个账号也没有留下任何存量；`mountHub` 的第二个参数把它改成"还有存量"。
 */
let affSelf = { id: 1, aff_quota: 0, aff_history_quota: 0, aff_count: 0 }

function respond(request: QyProbeRequest) {
  if (request.url.endsWith('/invite/summary')) return { data: SUMMARY }
  if (request.url.endsWith('/invite/records')) {
    const items =
      request.params.kind == null
        ? RECORDS
        : RECORDS.filter((row) => row.kind === request.params.kind)
    return { data: { items, total: items.length, p: 1, page_size: 20 } }
  }
  // 上游那张「推荐计划」卡读的三条主库接口：给最小合法数据，别让它们的
  // 解析把整棵树打进错误边界。
  if (request.url.endsWith('/api/user/aff')) return { data: 'ABC123' }
  if (request.url.endsWith('/api/user/self')) return { data: affSelf }
  if (request.url.includes('/api/user/topup/info')) {
    return {
      data: {
        pay_methods: [],
        amount_options: [],
        discount: {},
        creem_products: [],
        stripe_min_topup: 0,
        payment_compliance_confirmed: true,
      },
    }
  }
  return undefined
}

async function mountHub(hash?: string, aff?: Partial<typeof affSelf>) {
  affSelf = { id: 1, aff_quota: 0, aff_history_quota: 0, aff_count: 0, ...aff }
  await setQyProbeRole(ROLE.USER)
  return mountQyInviteScreen({
    element: <QyInviteHub />,
    path: '/qy/affiliate',
    initial: hash == null ? '/qy/affiliate' : `/qy/affiliate#${hash}`,
    features: { invite: true },
    respond,
  })
}

/** 只数 qy 的请求路径（上游主库接口不算）。 */
function qyUrls(sent: QyProbeRequest[]): string[] {
  return sent
    .filter((row) => row.url.startsWith('/api/qy/'))
    .map((row) => row.url)
}

describe('概览标签', () => {
  test('一进页面只打 /invite/summary；五种 kind 逐项摆出来且合计等于大数字', async () => {
    const screen = await mountHub()

    assert.deepEqual(qyUrls(screen.sent), ['/api/qy/invite/summary'])
    const text = screen.text()
    assert.ok(text.includes('1,234 星屑'), `大数字没带单位名渲染出来：${text}`)

    // 堆叠条上的五格，每一格带着翻译好的 kind 名、金额与占比。
    const segments = [
      ...document.body.querySelectorAll<HTMLElement>('[data-segment]'),
    ]
    assert.deepEqual(
      segments.map((node) => node.dataset.segment),
      [
        'invite_consume',
        'invite_topup',
        'invite_redeem',
        'invite_register',
        'plan_inviter',
      ],
      '五种 kind 必须各占一格，且按契约顺序'
    )
    const widths = segments.map((node) =>
      Number.parseFloat(node.style.width.replace('%', ''))
    )
    assert.ok(
      Math.abs(widths.reduce((a, b) => a + b, 0) - 100) < 0.01,
      `五格宽度加起来必须是 100%：${widths.join(' + ')}`
    )
    assert.ok(
      segments[0]?.getAttribute('aria-label')?.includes('600 星屑'),
      '第一格的读屏文本没带金额'
    )
    assert.ok(
      segments[0]?.getAttribute('aria-label')?.includes('49%'),
      '第一格的读屏文本没带占比（600 / 1234）'
    )
    // 图例逐行：五个数字都在。
    for (const amount of [
      '600 星屑',
      '300 星屑',
      '100 星屑',
      '200 星屑',
      '34 星屑',
    ]) {
      assert.ok(text.includes(amount), `图例缺 ${amount}`)
    }
    assert.ok(!text.includes('qy_sd_kind_'), '种类的 i18n 键没翻译')
    assert.ok(!text.includes('qy_inv_'), `有 qy_inv_ 键没翻译：${text}`)
  })

  test('昨日「已返 / 暂缓」两个数并排，基数按额度印而不是星屑', async () => {
    const screen = await mountHub()
    const text = screen.text()
    assert.ok(text.includes('已返 7 星屑'), '昨日已返没渲染')
    assert.ok(text.includes('暂缓 2 星屑'), '昨日暂缓没渲染')
    // 基数 3,700,000 额度是 $7.40：它必须走额度口径，绝不能印成 3,700,000 星屑。
    assert.ok(!text.includes('3,700,000 星屑'), '消费基数被当成星屑渲染了')
  })

  test('合规门关着时那句话在数字之前出现；费率卡说出所在分组档', async () => {
    const screen = await mountHub()
    const text = screen.text()
    const alertAt = text.indexOf(zh.qy_inv_compliance_title)
    assert.ok(alertAt >= 0, '缺「邀请返已暂停」')
    assert.ok(alertAt < text.indexOf('1,234 星屑'), '合规提示排在大数字后面')
    assert.ok(text.includes('vip'), '所在分组档没渲染')
    assert.ok(text.includes('5%'), '下线消费返比例没换算成百分数')
  })

  test('没有存量 aff_quota 时，顶部不挂上游那张星辉口径的推荐计划卡', async () => {
    // 本站的邀请返利是星屑。上游 `aff_quota` 那条通道在 `QuotaForInviter` 置 0
    // 之后不再产生新钱，卡上只剩三个 0：留着它，用户会以为除了星屑还有一笔
    // 按星辉计的邀请奖励在别处等着自己领。
    const screen = await mountHub()
    const text = screen.text()
    assert.ok(
      !text.includes('Referral Program'),
      `存量为 0 还挂着推荐计划卡：${text}`
    )
    assert.ok(!text.includes('Transfer to Balance'), '划转入口不该出现')
  })

  test('还有存量时推荐计划卡回来，且带「转入余额」的出口', async () => {
    // 清退通道不能连同卡片一起焊死：老用户手里那笔 aff_quota 只有这一个出口
    // （`/api/user/aff_transfer`），卡没了就再也转不出来。
    const screen = await mountHub(undefined, { aff_quota: 500_000 })
    const text = screen.text()
    assert.ok(text.includes('Referral Program'), `有存量却不渲染卡片：${text}`)
    assert.ok(text.includes('Transfer to Balance'), '有存量却没有划转入口')
  })

  test('整屏不出现「佣金」「提现」', async () => {
    const screen = await mountHub()
    const text = screen.text()
    assert.ok(!text.includes('佣金'), `出现了「佣金」：${text}`)
    assert.ok(!text.includes('提现'), `出现了「提现」：${text}`)
  })
})

describe('返星屑明细标签', () => {
  test('切到明细：只发 /invite/records、hash 写进地址栏、金额带 + 号', async () => {
    const screen = await mountHub()
    screen.sent.length = 0

    assert.ok(
      await screen.click(zh.qy_nav_invite_records),
      '标签栏上没有「返星屑明细」'
    )

    assert.deepEqual(qyUrls(screen.sent), ['/api/qy/invite/records'])
    assert.equal(
      screen.router.state.location.hash,
      qyTabHash('/qy/invite-records')
    )
    const text = screen.text()
    assert.ok(text.includes('+6 星屑'), '入账金额没带 + 号')
    assert.ok(text.includes('SDL-1001'), '流水号没渲染')
    assert.ok(text.includes('zh***ng'), '脱敏下线名没渲染')
  })

  test('按 kind 筛选会把 kind 带进请求并回到第一页，列表只剩那一种', async () => {
    const screen = await mountHub(qyTabHash('/qy/invite-records'))
    screen.sent.length = 0

    assert.ok(
      await screen.select(zh.qy_sd_col_kind, 'invite_topup'),
      '找不到种类下拉'
    )

    const asked = screen.sent.filter((row) =>
      row.url.endsWith('/invite/records')
    )
    assert.equal(asked.length, 1)
    assert.equal(asked[0].params.kind, 'invite_topup')
    assert.equal(asked[0].params.p, 1, '换筛选必须回到第一页')
    const text = screen.text()
    assert.ok(text.includes('SDL-1002'))
    assert.ok(!text.includes('SDL-1001'), '筛掉的那一种还在列表里')
  })
})
