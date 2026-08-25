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
 * 双色球「一次买多注」在按下确认之前那一屏上到底显示了什么。
 *
 * # 被投诉的那个形状
 *
 * 项目方原话：「用户购买，双色球不应该是可以选择买多少注的吗？现在只能选择
 * 一注？」在跑着的服务上复现之后，缺的**不是**后端能力 —— 后端一直允许同一个
 * 人在一场活动里有多条参与（`max_entries_per_user` 就是为它存在的），缺的是
 * 界面上"一次买几注"的入口：选号盘只有一组号，弹窗只提交一个 `pick`。
 *
 * # 这份测试守什么
 *
 *  1. **注数与总额是可见文字**，而且总额恒等于 `注数 × 单注参与费`。这是用户在
 *     按下确认之前唯一关心的量，而它必须与后端真正要扣的那个数一分不差 ——
 *     后端那一侧由 `ball_multi_entry_db_test.go` 真打余额核对。
 *  2. **"还能买几注"出现在按下确认之前**。改造前每人上限只在后端的活动行锁内
 *     判定，用户唯一知道自己超了的方式是提交完被顶回来。
 *  3. **两条闸门取更紧的那一条**：单次批量上限与本场剩余名额。
 *  4. **买满之后不许还能再加**：一个点得下去、后端一定拒的按钮，与本仓一直在补的
 *     "界面上点得到、后端不认"是同一种缺陷。
 *
 * 期望值一律在本文件里独立算出，不从被测组件回读。
 *
 * 变异实验见文件末尾。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { formatQyQuotaLedger } from '../../../lib/format'
import {
  cleanupQyLotScreens,
  mountQyLotScreen,
  qyLotDetailFixture,
  zhKeys,
} from './screen-harness'

after(cleanupQyLotScreens)

/** React `act` 的异步等待让单个用例超过 bun 默认的 5 秒。 */
const SLOW = { timeout: 120_000 }

const ACT_NO = 'LA-MULTI-1'
/** 单注参与费。总额的期望值全部由它乘出来，不从组件回读。 */
const STAKE = 1000
const NOW = Math.floor(Date.now() / 1000)

/** 红球 12 选 3、蓝球 4 选 1 —— 与线上那一期同形，而且**正在开放**。 */
const BALL = {
  act_no: ACT_NO,
  active_count: 3,
  close_at: NOW + 3600,
  draw_at: NOW + 7200,
  intro: '',
  min_entries_to_hold: 0,
  my_entry_count: 0,
  open_at: NOW - 3600,
  settle_deadline: NOW + 86_400,
  ball_blue_pick: 1,
  ball_blue_pool: 4,
  ball_red_pick: 3,
  ball_red_pool: 12,
  ball_result: '',
  draw_mode: 'ball' as const,
  issue_no: 1,
  kind: 'draw' as const,
  outcome: '',
  pool_open_quota: 200_000,
  series_no: 'LS-multi',
  spec: [
    {
      amount_quota: 0,
      blue_match: 1,
      count: 1,
      name: '一等奖',
      pool_share_bps: 5000,
      red_match: 3,
      tier: 1,
      win_ppm: 0,
    },
  ],
  stake_quota: STAKE,
  status: 'published' as const,
}

const EMPTY_PROOF = { entries: [], winners: [] }

function readQyLotDialog(): string {
  const dialog = document.body.querySelector('[role="dialog"]')
  return (dialog?.textContent ?? '').replace(/\s+/g, ' ')
}

/** 弹窗里那颗按可见文字精确匹配的按钮此刻是不是禁用的。 */
function dialogButtonDisabled(label: string): boolean | null {
  const node = Array.from(
    document.body.querySelectorAll('button,[role="button"]')
  ).find((item) => (item.textContent ?? '').trim() === label)
  if (node == null) return null
  return (
    node.hasAttribute('disabled') ||
    node.getAttribute('aria-disabled') === 'true'
  )
}

async function mountBallDetail(over: Record<string, unknown>) {
  const { QyLotteryDetail } = await import('../detail')
  const activity = { ...qyLotDetailFixture(), ...BALL, ...over }
  return mountQyLotScreen({
    path: `/qy/lottery/${ACT_NO}/`,
    element: <QyLotteryDetail />,
    respond: (url) => {
      if (url.includes('/eligibility')) return { eligible: true, missing: [] }
      if (url.includes('/proof')) return EMPTY_PROOF
      if (url.includes('/lottery/activities/')) return activity
      return undefined
    },
  })
}

/** 「机选补满 N 注」按钮此刻的文字。数字是它的一部分，必须自己算。 */
function fillLabel(slots: number): string {
  return zhKeys['qy_lot_ball_fill_random'].replace('{{count}}', String(slots))
}

describe('双色球买多注：按下确认之前那一屏', () => {
  test('机选补满之后，注数与总额都是可见文字且互相对得上', SLOW, async () => {
    // 单次批量上限 5、本场没有每人上限 → 可选名额 5。
    const screen = await mountBallDetail({
      max_entries_per_user: 0,
      max_picks_per_request: 5,
      my_entries_remaining: null,
    })
    assert.ok(await screen.click(zhKeys['qy_lot_join_title']), '参与按钮点不开')

    // 一注都没选时，注数是 0、总额是 0 —— 而不是"默认给你算一注"。
    const empty = readQyLotDialog()
    assert.ok(
      empty.includes(zhKeys['qy_lot_ball_line_count']),
      `弹窗里没有「本次注数」这一格：${empty}`
    )

    assert.ok(await screen.click(fillLabel(5)), '「机选补满 5 注」按钮点不到')

    const dialog = readQyLotDialog()
    assert.ok(
      dialog.includes(zhKeys['qy_lot_ball_lines_n'].replace('{{count}}', '5')),
      `补满之后注数没显示成 5 注：${dialog}`
    )
    // 期望的总额在这里独立乘出来。后端那一侧的实扣由 Go 用例真打余额核对，
    // 两处算出同一个数才叫"屏幕上的钱等于要扣的钱"。
    const total = formatQyQuotaLedger(STAKE * 5)
    assert.ok(
      dialog.includes(total),
      `合计没显示成 ${total}（5 注 × 单注 ${STAKE}）：${dialog}`
    )
    assert.equal(
      dialogButtonDisabled(fillLabel(0)),
      true,
      '补满之后「机选补满」还点得下去 —— 多出来的那一注后端一定会拒'
    )
  })

  test('选满但还没点「加入」的那一注照样算钱', SLOW, async () => {
    /*
      选号盘里选满的那一注**会被买走**（它进 `picks`），所以注数与总额必须当场
      把它算进去。不算的话，用户看到的是"1 注 $10"而按下确认扣的是"2 注 $20"
      —— 这一屏唯一的职责就是让那两个数相等。

      走选号盘自带的「机选」按钮而不是逐颗点球：红蓝两组里都有一颗写着 01 的球，
      按可见文字点会点中红球那一颗再把它点掉。
    */
    const screen = await mountBallDetail({
      max_entries_per_user: 0,
      max_picks_per_request: 5,
      my_entries_remaining: null,
    })
    assert.ok(await screen.click(zhKeys['qy_lot_join_title']))

    assert.ok(
      await screen.click(zhKeys['qy_lot_ball_quick_pick']),
      '选号盘的机选按钮点不到'
    )
    const pending = readQyLotDialog()
    assert.ok(
      pending.includes(zhKeys['qy_lot_ball_lines_n'].replace('{{count}}', '1')),
      `选满一注之后注数没变成 1 注：${pending}`
    )
    assert.ok(
      pending.includes(formatQyQuotaLedger(STAKE)),
      `合计没显示成一注的钱：${pending}`
    )

    // 「加入」只是把它挪进列表，注数不变 —— 挪进去之后再机选一注才是 2 注。
    assert.ok(await screen.click(zhKeys['qy_lot_ball_add_line']))
    const moved = readQyLotDialog()
    assert.ok(
      moved.includes(zhKeys['qy_lot_ball_lines_n'].replace('{{count}}', '1')),
      `「加入」之后注数变了：${moved}`
    )

    assert.ok(await screen.click(zhKeys['qy_lot_ball_quick_pick']))
    const two = readQyLotDialog()
    assert.ok(
      two.includes(zhKeys['qy_lot_ball_lines_n'].replace('{{count}}', '2')),
      `加入一注再机选一注之后注数没变成 2 注：${two}`
    )
    assert.ok(
      two.includes(formatQyQuotaLedger(STAKE * 2)),
      `合计没显示成两注的钱：${two}`
    )
  })

  test('单次批量上限与本场剩余名额取更紧的那一条', SLOW, async () => {
    // 单次最多 10 注，但本场只剩 2 注 → 只能买 2 注。
    const screen = await mountBallDetail({
      max_entries_per_user: 3,
      max_picks_per_request: 10,
      my_entries_remaining: 2,
    })
    assert.ok(await screen.click(zhKeys['qy_lot_join_title']))

    const dialog = readQyLotDialog()
    assert.ok(
      dialog.includes(
        zhKeys['qy_lot_ball_seats_left'].replace('{{count}}', '2')
      ),
      `「你在本场还能买 2 注」没有出现在按下确认之前：${dialog}`
    )
    assert.equal(
      dialogButtonDisabled(fillLabel(10)),
      null,
      '按 10 注给的补满按钮不该存在 —— 剩余名额只有 2 注'
    )

    assert.ok(await screen.click(fillLabel(2)), '「机选补满 2 注」按钮点不到')
    const after = readQyLotDialog()
    assert.ok(
      after.includes(zhKeys['qy_lot_ball_lines_n'].replace('{{count}}', '2')),
      `补满之后注数没显示成 2 注：${after}`
    )
    assert.ok(
      after.includes(formatQyQuotaLedger(STAKE * 2)),
      `合计没显示成 2 注的钱：${after}`
    )
  })

  test('本场买满时说的是"已达上限"，而不是"还能买 0 注"', SLOW, async () => {
    const screen = await mountBallDetail({
      max_entries_per_user: 3,
      max_picks_per_request: 10,
      my_entries_remaining: 0,
    })
    assert.ok(await screen.click(zhKeys['qy_lot_join_title']))

    const dialog = readQyLotDialog()
    assert.ok(
      dialog.includes(zhKeys['qy_lot_ball_seats_full']),
      `买满之后没说"已达上限"：${dialog}`
    )
    assert.ok(
      !dialog.includes(
        zhKeys['qy_lot_ball_seats_left'].replace('{{count}}', '0')
      ),
      '"还能买 0 注"是一句自相矛盾的话'
    )
    assert.equal(
      dialogButtonDisabled(fillLabel(0)),
      true,
      '买满之后「机选补满」还点得下去'
    )
  })

  test('老后端不下发 max_picks_per_request 时退回一注', SLOW, async () => {
    // 一个不下发这个字段的后端不认识 `picks`，多发几注只会被整批 400。
    // 前端在这种情况下必须退回"一次一注"，而不是按某个前端写死的数放行。
    const screen = await mountBallDetail({
      max_entries_per_user: 0,
      max_picks_per_request: undefined,
      my_entries_remaining: undefined,
    })
    assert.ok(await screen.click(zhKeys['qy_lot_join_title']))
    assert.equal(
      dialogButtonDisabled(fillLabel(1)),
      false,
      '退回一注时仍然要能机选那一注'
    )
    assert.equal(
      dialogButtonDisabled(fillLabel(10)),
      null,
      '按 10 注给的补满按钮不该存在 —— 老后端不认识 picks，多发只会被整批 400'
    )

    assert.ok(await screen.click(fillLabel(1)))
    const dialog = readQyLotDialog()
    assert.ok(
      dialog.includes(zhKeys['qy_lot_ball_lines_n'].replace('{{count}}', '1')),
      `退回一注之后注数没显示成 1 注：${dialog}`
    )
    assert.equal(
      dialogButtonDisabled(fillLabel(0)),
      true,
      '退回一注时买满了还能再加'
    )
  })

  test('单次只能买一注时不写"一次最多买 1 注"', SLOW, async () => {
    // 一句零信息量的提示挤掉的是真正要被读到的那几个数 —— 这一屏的字数预算
    // 是逐字算过的（见 text-budget.test.tsx）。
    const screen = await mountBallDetail({
      max_entries_per_user: 0,
      max_picks_per_request: 1,
      my_entries_remaining: null,
    })
    assert.ok(await screen.click(zhKeys['qy_lot_join_title']))
    assert.ok(
      !readQyLotDialog().includes(
        zhKeys['qy_lot_ball_per_request_cap'].replace('{{count}}', '1')
      ),
      '"一次最多买 1 注"没有信息量,不该占一行'
    )
  })
})

/*
 * ── 变异实验 ────────────────────────────────────────────────────────
 *
 * 全部改在 `components/lottery-entry-dialog.tsx` 上，改完跑
 * `bun test src/features/qy/pages/lottery/__tests__/ball-multi-line.test.tsx`。
 *
 * ① `totalQuota = activity.stake_quota * count` → `activity.stake_quota`
 *    （退回改造前"总是按一注算"）。
 *    → KILLED，3 fail：合计停在 $10，而 5 注应当是 $50。
 *
 * ② `seatCap = remaining == null ? perRequestCap : Math.min(...)` → `perRequestCap`
 *    （忽略本场剩余名额）。
 *    → KILLED，2 fail：「机选补满 2 注」渲染成了 10 注，买满那一场也放行了。
 *
 * ③ `perRequestCap = activity.max_picks_per_request ?? 1` 的兜底 → `?? 10`。
 *    → KILLED，1 fail：老后端那条用例按 10 注渲染了补满按钮。
 *
 * ④ `QyLotSeatHint` 里 `props.remaining <= 0` → `< 0`
 *    （把"买满"说成"还能买 0 注"）。
 *    → KILLED，1 fail：买满那条用例两条断言同时失败。
 *
 * ⑤ `submitLines = pendingComplete ? [...lines, pick] : lines` → `lines`
 *    （选满但没点「加入」的那一注不算进注数，而它照样会被买走）。
 *    → KILLED，1 fail：机选之后注数停在 0 注、合计停在 $0。
 *
 * ⑥ `QyLotSeatHint` 里 `props.perRequestCap > 1` → `> 0`
 *    （把零信息量的"一次最多买 1 注"放回去）。
 *    → KILLED，1 fail：最后一条用例。它同时是 `text-budget.test.tsx` 那条
 *      260 字预算的守卫 —— 两处一起红才说明这一行真的被读到了。
 */
