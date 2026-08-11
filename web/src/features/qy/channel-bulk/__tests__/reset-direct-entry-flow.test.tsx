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
 * 清零直达入口的**行为**测试：真的点，真的看请求。
 *
 * # 守什么
 *
 * 项目方三次都没找到清零功能，原因不是功能缺失而是入口埋得太深。所以这一轮
 * 加的两条短路径必须被钉住的是三件事，而它们各自的失败都很安静：
 *
 *   1. **点一下就能到。** 列表头的入口点开就是自带勾选的挑选面板，行内的入口
 *      点开就是这一个渠道的确认框 —— 全程不碰「批量操作」开关（本文件里连
 *      那个开关都不存在，组件依然能走完全程，这本身就是判据）。
 *   2. **确认框复述的是这一次真正要清的那些。** 条数与合计金额都必须来自
 *      被勾中的那几个，而不是整页。勾了 2 个却按 3 个的金额提示，用户是发现
 *      不了的 —— 直到钱已经清掉。
 *   3. **提交打到 `batch-reset-usage`，id 一个不多一个不少。** 单渠道路径复用
 *      同一个接口（ids 里只放一个），没有第二个端点。
 *
 * 另外守一条负向的：**不勾"我已知晓"就提交不了**。入口变短的是"找到它"的
 * 路径，不是"按下去"的代价。
 *
 * 取数走真实 axios 实例（只换 adapter），不 mock 模块：URL 拼接、信封解包、
 * 请求体字段名都是这条链路的一部分，绕过去就只是在测一份自己写的假数据。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

const domWindow = new Window({ width: 1280, height: 900 })
const domGlobals = [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
  'HTMLElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
  'ResizeObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
  'matchMedia',
  'DOMRect',
] as const

for (const key of domGlobals) {
  const value = domWindow[key as keyof Window]
  if (value === undefined) continue
  Object.defineProperty(globalThis, key, { configurable: true, value })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
const qyEn = (await import('@/i18n/qy/en.json')).default
await i18next
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: qyEn } } })

const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { api } = await import('@/lib/api')
const { useAuthStore } = await import('@/stores/auth-store')
const { ROLE } = await import('@/lib/roles')
const { qyKeys } = await import('../../lib/query-keys')
const { QY_DISABLED_CONFIG } = await import('../../lib/config-query')
const { formatQyQuotaLedger } = await import('../../lib/format')
const { QyChannelResetUsageColumnAction, QyChannelResetUsageRowAction } =
  await import('../reset-usage')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

/* ── 取数替身：真 axios 实例 + 假 adapter ───────────────────────────── */

type ResetBody = {
  ids: number[]
  reset_used_quota: boolean
  reset_balance: boolean
}

const posts: Array<{ url: string; body: ResetBody }> = []

api.defaults.adapter = async (config) => {
  const url = String(config.url ?? '')
  let data: unknown = { success: true, message: '', data: null }

  if (url.includes('/admin/channels/batch-reset-usage')) {
    const body = JSON.parse(String(config.data)) as ResetBody
    posts.push({ url, body })
    data = {
      success: true,
      message: '',
      data: {
        total: body.ids.length,
        succeeded: body.ids.length,
        skipped: 0,
        failed: 0,
        items: body.ids.map((id) => ({ id, name: `ch-${id}`, outcome: 'ok' })),
        cleared_used_quota: 0,
        cleared_balance: 0,
      },
    }
  }

  return {
    data,
    status: 200,
    statusText: 'OK',
    headers: {},
    config,
  }
}

/* ── 挂载脚手架 ─────────────────────────────────────────────────────── */

const ENABLED_CONFIG = { ...QY_DISABLED_CONFIG, enabled: true, available: true }

const CHANNELS = [
  { id: 11, name: 'alpha', used_quota: 500_000 },
  { id: 22, name: 'beta', used_quota: 1_000_000 },
  { id: 33, name: 'gamma', used_quota: 0 },
]

const roots: Array<{
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}> = []

async function unmountAll() {
  for (;;) {
    const mounted = roots.pop()
    if (mounted == null) return
    await act(async () => mounted.root.unmount())
    mounted.container.remove()
  }
}

after(unmountAll)

async function mount(node: React.ReactNode) {
  await unmountAll()
  posts.length = 0

  // 超管：`hasPermission` 对 SUPER_ADMIN 直接放行，省掉一份权限矩阵夹具。
  // 权限本身不是这个文件要守的东西（真正说了算的是后端）。
  useAuthStore.setState((state) => ({
    ...state,
    auth: {
      ...state.auth,
      user: { id: 1, username: 'probe', role: ROLE.SUPER_ADMIN, status: 1 },
      accessToken: 'probe',
    },
  }))

  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  queryClient.setQueryData(qyKeys.config(), ENABLED_CONFIG)

  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>{node}</QueryClientProvider>
    )
  })
  await act(async () => {})
  roots.push({ container, root })
}

/** 全文档范围找元素：弹窗渲染在 portal 里，不在挂载容器内。 */
function findAll(selector: string): HTMLElement[] {
  return [...document.body.querySelectorAll(selector)] as HTMLElement[]
}

function requireOne(selector: string, why: string): HTMLElement {
  const found = findAll(selector)
  assert.equal(found.length, 1, `${why}（命中 ${found.length} 个 ${selector}）`)
  return found[0]
}

/** 按可见文字找按钮。弹窗里的按钮没有稳定 id，文字是用户唯一的抓手。 */
function buttonWithText(text: string): HTMLElement {
  const hit = findAll('button').filter(
    (el) => (el.textContent ?? '').trim() === text
  )
  assert.equal(hit.length, 1, `找不到唯一一个写着「${text}」的按钮`)
  return hit[0]
}

async function click(el: HTMLElement) {
  await act(async () => {
    el.click()
  })
  await act(async () => {})
}

function screenText(): string {
  return document.body.textContent ?? ''
}

/**
 * 勾上「我已知晓」那道闸门。
 *
 * 点的是隐藏的原生 input 而不是那个 `role=checkbox` 的 span：base-ui 的
 * Checkbox 把可见部分做成 span、真正持有勾选态的是同级那个隐藏 input，
 * `<Label htmlFor>` 指向的也是它。点 span 在这个无布局的 DOM 里不会翻状态。
 *
 * 它是确认框里最后一个勾选框（前面两个是「清空已用额度」「清空上游余额」）。
 */
async function acknowledge() {
  const boxes = findAll('input[type="checkbox"]')
  const ack = boxes.at(-1)
  assert.ok(ack != null, '不可逆确认框里没有强制勾选项')
  await click(ack)
}

/* ── 1. 列表头的直达入口：一次点击到挑选面板 ────────────────────────── */

describe('列表头的清零直达入口', () => {
  test('点一下就出挑选面板，全程不经过「批量操作」开关', async () => {
    await mount(<QyChannelResetUsageColumnAction channels={CHANNELS} />)

    // 页面上此刻只有这一个按钮：既没有批量操作开关，也没有勾选列。
    const entry = requireOne(
      '[data-qy-reset-entry="column"]',
      '列表头的直达入口不见了'
    )
    assert.equal(
      screenText().includes(qyEn.qy_chops_reset_pick_all),
      false,
      '面板不点就开着'
    )

    await click(entry)

    assert.ok(
      screenText().includes(qyEn.qy_chops_reset_pick_all),
      '点了入口却没有出现自带勾选的挑选面板'
    )
    // 面板自带勾选：三个渠道各一个勾选框 + 一个全选。
    assert.equal(
      findAll('[data-slot="checkbox"]').length,
      CHANNELS.length + 1,
      '挑选面板没有自己的勾选框 —— 那就又要回去开「批量操作」了'
    )
  })

  test('确认框复述的是被勾中的那几个的条数与合计，不是整页', async () => {
    await mount(<QyChannelResetUsageColumnAction channels={CHANNELS} />)
    await click(requireOne('[data-qy-reset-entry="column"]', '入口丢了'))

    await click(requireOne('#qy-reset-pick-11', 'alpha 的勾选框丢了'))
    await click(requireOne('#qy-reset-pick-22', 'beta 的勾选框丢了'))
    await click(
      buttonWithText(i18next.t('qy_chops_reset_pick_next', { count: 2 }))
    )

    const text = screenText()
    assert.ok(
      text.includes(i18next.t('qy_chops_reset_channel_count', { count: 2 })),
      '确认框没有复述"这次清 2 个渠道"'
    )
    assert.ok(
      text.includes(formatQyQuotaLedger(1_500_000)),
      '确认框上的合计不是被勾中那两个的和（500000 + 1000000）'
    )
    assert.equal(
      text.includes(formatQyQuotaLedger(0)) && text.includes(CHANNELS[2].name),
      false,
      '没被勾中的 gamma 出现在了确认框里'
    )
    // 「清的是已使用、不是剩余」必须在按下确认之前就在同一屏上。
    assert.ok(
      text.includes(qyEn.qy_chops_reset_scope_hint),
      '确认框没有说清楚清的是「已使用」而不是「剩余」'
    )
  })

  test('提交打到 batch-reset-usage，id 与被勾中的完全一致', async () => {
    await mount(<QyChannelResetUsageColumnAction channels={CHANNELS} />)
    await click(requireOne('[data-qy-reset-entry="column"]', '入口丢了'))
    await click(requireOne('#qy-reset-pick-22', 'beta 的勾选框丢了'))
    await click(
      buttonWithText(i18next.t('qy_chops_reset_pick_next', { count: 1 }))
    )

    await acknowledge()
    await click(buttonWithText(qyEn.qy_chops_reset_action))

    assert.equal(posts.length, 1, `期望正好一次请求，实际 ${posts.length} 次`)
    assert.ok(
      posts[0].url.endsWith('/api/qy/admin/channels/batch-reset-usage'),
      `提交打到了别的地址：${posts[0].url}`
    )
    assert.deepEqual(posts[0].body.ids, [22])
    assert.equal(
      posts[0].body.reset_used_quota,
      true,
      '默认没有勾上「清空已用额度」—— 那是这个功能唯一真正有用的部分'
    )
    assert.equal(
      posts[0].body.reset_balance,
      false,
      '默认清了上游余额展示值：它只是缓存，下一次「更新余额」就覆盖回来了'
    )
  })
})

/* ── 2. 行内单渠道：一次点击到确认框 ────────────────────────────────── */

describe('行内的单渠道清零', () => {
  test('点一下就是这一个渠道的确认框，提交只带它一个 id', async () => {
    await mount(<QyChannelResetUsageRowAction channel={CHANNELS[1]} />)

    const entry = requireOne(
      '[data-qy-reset-entry="row"]',
      '行内的单渠道清零入口不见了'
    )
    await click(entry)

    const text = screenText()
    assert.ok(
      text.includes(CHANNELS[1].name),
      '确认框没有说清楚要清的是哪一个渠道'
    )
    assert.ok(
      text.includes(formatQyQuotaLedger(CHANNELS[1].used_quota)),
      '确认框没有复述这一个渠道的已用额度'
    )

    await acknowledge()
    await click(buttonWithText(qyEn.qy_chops_reset_action))

    assert.equal(posts.length, 1)
    assert.ok(
      posts[0].url.endsWith('/api/qy/admin/channels/batch-reset-usage'),
      '单渠道另开了一个端点 —— 审计与锁内重读金额那一整套都在批量那条路上'
    )
    assert.deepEqual(posts[0].body.ids, [CHANNELS[1].id])
  })

  test('不勾「我已知晓」就提交不了：一个请求都不会发出去', async () => {
    await mount(<QyChannelResetUsageRowAction channel={CHANNELS[1]} />)
    await click(requireOne('[data-qy-reset-entry="row"]', '入口丢了'))

    await click(buttonWithText(qyEn.qy_chops_reset_action))

    assert.equal(
      posts.length,
      0,
      '没勾确认就把不可逆动作提交出去了 —— 入口变短的是"找到它"的路径，' +
        '不是"按下去"的代价'
    )
  })
})
