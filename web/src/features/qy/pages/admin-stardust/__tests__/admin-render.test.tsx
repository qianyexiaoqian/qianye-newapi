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
 * 星屑账本（管理端）在**真实渲染**上的形状。
 *
 * # 超管闸门
 *
 * 手调是超级管理员专属（后端 `RootActionStardustAdjust`）。口径与
 * `__tests__/root-action-gates.test.tsx` 相同：每一格都断言两件事 —— 该角色
 * **能不能看到那颗按钮**，以及**看不到时那句解释在不在**。只断言其一都杀不掉
 * "判据反了"与"只藏按钮、不给出口"这两种坏法。其余动作一个都不连坐。
 *
 * # 幂等键
 *
 * 弹窗打开时生成一次、重试沿用：第一次提交撞上 5xx 后再按一次，两条 POST 的
 * `client_request_id` 必须逐字相同 —— 否则一次网络超时就是第二笔。
 *
 * # 三处后端偏差
 *
 * `ledger-check` 多下发 `ok` / `error`：`ok=false` 时要展示 error 而不是把零值
 * 当成"全部自洽"；`settle/status` 是独立接口；重跑多两个计数。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import {
  ROLE,
  act,
  cleanupQyStardustScreens,
  mountQyStardustScreen,
  setQyProbeRole,
  zh,
  type QyProbeRequest,
} from '../../../__tests__/stardust-screen'
import { QyAdminStardust } from '../index'

after(cleanupQyStardustScreens)

const BALANCES = {
  items: [
    {
      user_id: 42,
      username: 'alice',
      available: 500,
      total_earned: 600,
      total_spent: 100,
      total_refunded: 0,
      total_adjusted: 0,
      carry: '0.2500000000',
      hold_reason: '',
      updated_at: 1_787_000_000,
    },
  ],
  total: 1,
  page: 1,
  page_size: 20,
}

const SETTLE_STATUS = {
  last_run: {
    run_date: '20260904',
    target_date: '20260903',
    status: 'done',
    attempts: 1,
    started_at: 1_787_000_000,
    finished_at: 1_787_000_050,
    processed: 12,
    failed: 0,
    held: 2,
    granted: 340,
    remark: '',
  },
  next_settle_at: 1_800_000_000,
  target_day: '20260903',
  run_date: '20260904',
  ready: true,
  max_attempts: 5,
}

/** 每个用例自己决定体检回什么、手调回什么。 */
type Scenario = {
  check: unknown
  adjust: (request: QyProbeRequest, n: number) => ReturnType<typeof respondWith>
}

function respondWith(data: unknown, status?: number, code?: string) {
  return { data, status, code }
}

function makeResponder(scenario: Scenario) {
  let adjustCalls = 0
  return (request: QyProbeRequest) => {
    if (request.url.endsWith('/admin/stardust/balances')) {
      return { data: BALANCES }
    }
    if (request.url.endsWith('/admin/stardust/settle/status')) {
      return { data: SETTLE_STATUS }
    }
    if (request.url.endsWith('/admin/stardust/ledger-check')) {
      return { data: scenario.check }
    }
    if (request.url.endsWith('/admin/stardust/adjust')) {
      adjustCalls += 1
      return scenario.adjust(request, adjustCalls)
    }
    return { data: { items: [], total: 0, page: 1, page_size: 20 } }
  }
}

const CHECK_OK = {
  ok: true,
  error: '',
  checked_users: 3,
  drifted_users: 0,
  worst_user_id: 0,
  worst_drift: '0',
  held_rows: 2,
  oldest_held_day: '20260820',
  held_alert: true,
}

async function mountAdmin(role: number, scenario: Scenario) {
  await setQyProbeRole(role)
  return mountQyStardustScreen({
    element: <QyAdminStardust />,
    path: '/qy/admin/stardust',
    respond: makeResponder(scenario),
  })
}

function buttons(): string[] {
  return [...document.body.querySelectorAll('button')].map((node) =>
    (node.textContent ?? '').trim()
  )
}

const NO_ADJUST: Scenario = {
  check: CHECK_OK,
  adjust: () => respondWith({}, 500, 'qy_internal_error'),
}

describe('手调的超管闸门', () => {
  test('role=10：没有「手调」按钮，但有一句解释；「流水」那颗行内按钮不连坐', async () => {
    const screen = await mountAdmin(ROLE.ADMIN, NO_ADJUST)

    const labels = buttons()
    assert.ok(
      !labels.includes(zh.qy_sdadm_adjust_action),
      `普通管理员不该看到「手调」—— 点了就是 403：${labels.join(' | ')}`
    )
    const hint = zh.qy_sdadm_adjust_root_only
    assert.ok(hint != null && hint !== '', '这句解释在语言包里不存在')
    assert.ok(screen.text().includes(hint), '按钮消失了却没有任何解释')
    assert.ok(
      labels.includes(zh.qy_nav_stardust_ledger),
      '行内「流水」被连坐藏掉了'
    )
    assert.ok(
      screen.text().includes('alice'),
      '余额表没渲染出来，上面的断言不算数'
    )
  })

  test('role=100：有「手调」按钮（页面级 + 行内），没有那句解释', async () => {
    const screen = await mountAdmin(ROLE.SUPER_ADMIN, NO_ADJUST)

    const count = buttons().filter(
      (label) => label === zh.qy_sdadm_adjust_action
    ).length
    assert.equal(count, 2, '超管应当在页面右上角与每一行各看到一颗「手调」')
    assert.ok(!screen.text().includes(zh.qy_sdadm_adjust_root_only))
  })
})

describe('手调弹窗', () => {
  test('一进页面只有余额表在取数，另外三张标签一条请求都没发', async () => {
    const screen = await mountAdmin(ROLE.SUPER_ADMIN, NO_ADJUST)
    const urls = screen.sent
      .filter((row) => row.url.startsWith('/api/qy/'))
      .map((row) => row.url)
    assert.deepEqual(urls, ['/api/qy/admin/stardust/balances'])
  })

  test('第一次提交撞上 5xx 后再提交：两条 POST 的幂等键逐字相同，delta 带符号', async () => {
    let seenIds: string[] = []
    const screen = await mountAdmin(ROLE.SUPER_ADMIN, {
      check: CHECK_OK,
      adjust: (request, n) => {
        seenIds.push(
          String(
            (request.body as { client_request_id: string }).client_request_id
          )
        )
        if (n === 1) return respondWith({}, 500, 'qy_internal_error')
        return respondWith({
          ledger_no: 'SDL-9',
          user_id: 42,
          delta: -50,
          balance_after: 450,
          replayed: false,
        })
      },
    })
    seenIds = []

    // 行内那颗「手调」（第二颗；第一颗是页面右上角的）。
    const rowButton = [...document.body.querySelectorAll('button')].filter(
      (node) => (node.textContent ?? '').trim() === zh.qy_sdadm_adjust_action
    )[1]
    assert.ok(rowButton != null)
    await act(async () => {
      rowButton.click()
    })
    await screen.settle()
    assert.ok(screen.text().includes('alice (#42)'), '弹窗没带上这个人')

    assert.ok(await screen.select(zh.qy_sdadm_adjust_direction, 'sub'))
    assert.ok(
      await screen.type(zh.qy_sd_input_placeholder, '50'),
      '找不到金额框'
    )
    assert.ok(
      await screen.type(zh.qy_common_reason, '补偿工单 123'),
      '找不到事由框'
    )

    assert.ok(await screen.click(zh.qy_sdadm_adjust_submit), '找不到提交键')
    assert.ok(
      await screen.click(zh.qy_sdadm_adjust_submit),
      '5xx 之后弹窗应当还开着、能再提交'
    )

    const posts = screen.sent.filter((row) =>
      row.url.endsWith('/admin/stardust/adjust')
    )
    assert.equal(posts.length, 2)
    assert.deepEqual(
      posts.map((row) => row.body),
      [
        {
          user_id: 42,
          delta: -50,
          reason: '补偿工单 123',
          client_request_id: seenIds[0],
        },
        {
          user_id: 42,
          delta: -50,
          reason: '补偿工单 123',
          client_request_id: seenIds[1],
        },
      ]
    )
    assert.equal(seenIds[0], seenIds[1], '重试换了幂等键：一次超时就是第二笔')
    assert.ok(seenIds[0] !== '', '幂等键不能是空串')
  })

  test('扣减超过当前余额时提交键禁用并说明', async () => {
    const screen = await mountAdmin(ROLE.SUPER_ADMIN, NO_ADJUST)
    const rowButton = [...document.body.querySelectorAll('button')].filter(
      (node) => (node.textContent ?? '').trim() === zh.qy_sdadm_adjust_action
    )[1]
    await act(async () => {
      rowButton.click()
    })
    await screen.settle()

    await screen.select(zh.qy_sdadm_adjust_direction, 'sub')
    await screen.type(zh.qy_sd_input_placeholder, '501')
    await screen.type(zh.qy_common_reason, '补偿工单 123')

    const submit = [...document.body.querySelectorAll('button')].find(
      (node) => (node.textContent ?? '').trim() === zh.qy_sdadm_adjust_submit
    )
    assert.ok(submit != null)
    assert.equal(submit.disabled, true, '超过余额的扣减不该能提交')
    assert.ok(screen.text().includes('500 星屑'), '超额提示里没带当前余额')
  })
})

describe('结算标签', () => {
  test('调度状态 + 体检：目标日、上一跑、held 红点都在；体检 ok=false 时展示 error', async () => {
    const screen = await mountAdmin(ROLE.SUPER_ADMIN, {
      check: { ...CHECK_OK, ok: false, error: '扩展库读不到(probe)' },
      adjust: NO_ADJUST.adjust,
    })
    screen.sent.length = 0

    assert.ok(
      await screen.click(zh.qy_sdadm_tab_settle),
      '标签栏上没有「结算」'
    )

    const urls = screen.sent
      .filter((row) => row.url.startsWith('/api/qy/'))
      .map((row) => row.url)
      .sort()
    assert.deepEqual(urls, [
      '/api/qy/admin/stardust/ledger-check',
      '/api/qy/admin/stardust/settle/status',
    ])

    const text = screen.text()
    assert.ok(text.includes('2026-09-03'), '目标日没按 YYYY-MM-DD 渲染')
    assert.ok(
      text.includes(zh.qy_sdadm_settle_state_done),
      '今天已跑完的徽章没渲染'
    )
    assert.ok(text.includes('340 星屑'), '上一跑发出的星屑没渲染')
    assert.ok(
      text.includes('扩展库读不到(probe)'),
      'ok=false 时必须把 error 摆出来'
    )
    assert.ok(
      !text.includes(zh.qy_sdadm_check_ok),
      'ok=false 却显示了「全部自洽」—— 读失败的零值被当成了正常'
    )
  })

  test('体检 ok=true：漂移为 0 显示自洽，held_alert 亮红点', async () => {
    const screen = await mountAdmin(ROLE.SUPER_ADMIN, NO_ADJUST)
    await screen.click(zh.qy_sdadm_tab_settle)

    const text = screen.text()
    assert.ok(text.includes(zh.qy_sdadm_check_ok))
    assert.ok(
      text.includes(zh.qy_sdadm_check_held_alert),
      'held_alert 没有渲染成红点/徽章'
    )
    assert.ok(text.includes('2026-08-20'), '最老的暂缓桶日没渲染')
  })
})
