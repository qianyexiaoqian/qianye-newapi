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
 * 清零直达入口**在真实页面上到底渲不渲染**。
 *
 * # 为什么必须有这一层
 *
 * 上一轮这个入口是"接线正确但从不显示"：`channels-columns.tsx` 里
 * `header: ({ table }) => <BalanceColumnHeader .../>` 写得好好的，同一列上
 * 为了修「显示/隐藏列」菜单又补了一句 `meta: { label: ... }` ——
 * 而上游 `renderHeaderContent` 当时把 `meta.label` 排在函数型 header **前面**，
 * 于是那一行 header 永远走不到 `flexRender`，整个入口连同挑选面板一起消失。
 *
 * 这个缺陷在当时的两个测试里全绿：
 *
 *   · 源码/AST 锁断言的是"那一行 header 里写了 `<BalanceColumnHeader`" ——
 *     缺陷存在时这句话依然为真；
 *   · 行为测试直接 `mount(<QyChannelResetUsageColumnAction/>)`，**绕开了
 *     react-table 的表头渲染器**，而那正是唯一会出事的那一层。
 *
 * 所以这里改成：把真实的 `useChannelsColumns()` 交给真实的 react-table，
 * 再交给真实的 `DataTableHeader`，然后去 DOM 里数按钮。判据落在"用户能不能
 * 看见"上，中间少接一根线就变红。
 *
 * 另外守两条同样只在真实渲染下才暴露的：
 *
 *   1. 表头换成组件之后，这一列原有的排序 / 隐藏列**不许丢**；
 *   2. 这一页的默认视图是**卡片视图**（`enableCardView` 且没传
 *      `defaultViewMode`），卡片视图连表头都没有 —— 所以多渠道入口在工具条上
 *      还必须有一份，否则"不打开批量操作开关也能多选清零"在默认视图下仍然
 *      是空话。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

// 类型导入会被完全擦除，不会在 DOM 全局装好之前把模块拉起来。
import type { Channel } from '@/features/channels/types'

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
  // 渠道页的组件树里有 web component（差异视图）：模块加载时就会去碰
  // `customElements`，没有它 import 阶段直接抛。
  'customElements',
  'HTMLDivElement',
  'CSS',
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
// 上游文案不进 resources：i18next 找不到键时原样返回键名，而上游的键就是英文
// 原文（`t('Used / Remaining')` → "Used / Remaining"），页面上看到的是同一串。
await i18next
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: qyEn } } })

const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { getCoreRowModel, useReactTable } = await import('@tanstack/react-table')
const { DataTableHeader } =
  await import('@/components/data-table/core/data-table-header')
const { DataTablePage } =
  await import('@/components/data-table/layout/data-table-page')
const { ChannelsProvider } =
  await import('@/features/channels/components/channels-provider')
const { useChannelsColumns } =
  await import('@/features/channels/components/channels-columns')
const { useAuthStore } = await import('@/stores/auth-store')
const { ROLE } = await import('@/lib/roles')
const { qyKeys } = await import('../../lib/query-keys')
const { QY_DISABLED_CONFIG } = await import('../../lib/config-query')
const { QyChannelResetUsageToolbarAction } = await import('../reset-usage')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

/* ── 夹具 ───────────────────────────────────────────────────────────── */

const ENABLED_CONFIG = {
  ...QY_DISABLED_CONFIG,
  enabled: true,
  available: true,
}

// 只有这几个字段参与本文件的判据（列名、入口、金额）。整份 Channel 有 20+ 个
// 必填字段，逐个填出来只会把这一屏变成一份与判据无关的夹具。
const CHANNELS = [
  { id: 11, name: 'alpha', used_quota: 500_000, balance: 12.5 },
  { id: 22, name: 'beta', used_quota: 1_000_000, balance: 7.25 },
] as unknown as Channel[]

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

  // 超管：`hasPermission` 对 SUPER_ADMIN 直接放行。权限本身由 reset-usage 那组
  // 测试与后端 RequirePermission 守，这里只关心"看得见看不见"。
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
      <QueryClientProvider client={queryClient}>
        <ChannelsProvider>{node}</ChannelsProvider>
      </QueryClientProvider>
    )
  })
  await act(async () => {})
  roots.push({ container, root })
}

function findAll(selector: string): HTMLElement[] {
  return [...document.body.querySelectorAll(selector)] as HTMLElement[]
}

async function click(el: HTMLElement) {
  await act(async () => {
    el.click()
  })
  await act(async () => {})
}

/* ── 1. 真实列定义 → 真实 react-table → 真实表头渲染器 ───────────────── */

/**
 * 渠道页的表头，接线与生产一致。
 *
 * `enableSelection: false` 是刻意的：它模拟「批量操作」开关**关着**的那一页，
 * 也就是项目方三次看到的那一页。入口必须在这种状态下就已经在屏幕上。
 */
function ChannelsHeaderProbe() {
  const columns = useChannelsColumns({ enableSelection: false })
  const table = useReactTable({
    data: CHANNELS,
    columns,
    getCoreRowModel: getCoreRowModel(),
  })
  return (
    <table>
      <DataTableHeader table={table} />
    </table>
  )
}

describe('表头上的多渠道入口真的渲染到了 DOM 里', () => {
  test('「已使用 / 剩余」这一列的表头里有清零入口', async () => {
    await mount(<ChannelsHeaderProbe />)

    const balanceHead = findAll('th[data-column-id="balance"]')
    assert.equal(balanceHead.length, 1, '「已使用 / 剩余」这一列不见了')
    assert.equal(
      balanceHead[0].querySelectorAll('[data-qy-reset-entry="column"]').length,
      1,
      '这一列的表头里没有清零入口 —— 接线写对了不等于用户看得见：' +
        '上一轮它就是被同一列上的 meta.label 在 renderHeaderContent 里' +
        '整个短路掉的，全套测试照样全绿'
    )
  })

  test('点表头入口出的是自带勾选的挑选面板，不碰「批量操作」开关', async () => {
    await mount(<ChannelsHeaderProbe />)

    const entry = findAll('[data-qy-reset-entry="column"]')[0]
    assert.ok(entry != null)
    await click(entry)

    const text = document.body.textContent ?? ''
    assert.ok(
      text.includes(qyEn.qy_chops_reset_pick_all),
      '点了表头入口却没有出现挑选面板'
    )
    // 两个渠道各一个 + 一个全选。整个探针里根本没有「批量操作」开关，
    // 也没有勾选列（enableSelection: false）—— 面板的勾选是它自带的。
    assert.equal(
      findAll('[data-slot="checkbox"]').length,
      CHANNELS.length + 1,
      '挑选面板没有自己的勾选框'
    )
  })

  test('表头换成组件之后，这一列原有的排序与隐藏列没有丢', async () => {
    await mount(<ChannelsHeaderProbe />)

    const balanceHead = findAll('th[data-column-id="balance"]')[0]
    assert.ok(balanceHead.textContent?.includes('Used / Remaining'), '列名没了')
    assert.equal(
      balanceHead.querySelectorAll('[data-slot="dropdown-menu-trigger"]')
        .length,
      1,
      '排序 / 隐藏列的下拉不见了 —— 加一条更短的路不该以弄坏这一列原有的' +
        '功能为代价'
    )
  })
})

/* ── 2. 默认视图（卡片）下多渠道入口仍然在屏幕上 ────────────────────── */

/**
 * 渠道页的外壳，`DataTablePage` 的关键入参与生产一致：
 * `enableCardView` 且**不传** `defaultViewMode` —— 这两条加起来，
 * 首次进页面（localStorage 里还没有视图选择）默认就是卡片视图。
 */
function ChannelsPageProbe() {
  const columns = useChannelsColumns({ enableSelection: false })
  const table = useReactTable({
    data: CHANNELS,
    columns,
    getCoreRowModel: getCoreRowModel(),
  })
  return (
    <DataTablePage
      table={table}
      columns={columns}
      enableCardView
      renderCard={(row) => <div>{row.original.name}</div>}
      toolbarProps={{
        preActions: (
          <QyChannelResetUsageToolbarAction
            channels={CHANNELS.map((channel) => ({
              id: channel.id,
              name: channel.name,
              used_quota: channel.used_quota,
            }))}
          />
        ),
      }}
    />
  )
}

describe('默认视图下多渠道入口仍然够得着', () => {
  test('默认是卡片视图：一个表头都没有，但工具条上的入口在', async () => {
    localStorage.clear()
    await mount(<ChannelsPageProbe />)

    assert.equal(
      findAll('th[data-column-id="balance"]').length,
      0,
      '默认视图已经不是卡片视图了 —— 这条测试的前提需要重新校准'
    )
    assert.equal(
      findAll('[data-qy-reset-entry="column"]').length,
      0,
      '卡片视图下不该有表头入口（它长在表头上，而卡片视图没有表头）'
    )
    assert.equal(
      findAll('[data-qy-reset-entry="toolbar"]').length,
      1,
      '默认视图下多渠道清零一个入口都没有 —— 只把它挂在表头上，' +
        '等于把"能不能找到"押在用户此前恰好切过一次视图上'
    )
  })

  test('工具条入口点一下就是同一个挑选面板', async () => {
    localStorage.clear()
    await mount(<ChannelsPageProbe />)

    await click(findAll('[data-qy-reset-entry="toolbar"]')[0])

    assert.ok(
      (document.body.textContent ?? '').includes(qyEn.qy_chops_reset_pick_all),
      '工具条入口点开的不是自带勾选的挑选面板'
    )
  })

  test('工具条入口带文字，不是一个没有标签的图标', async () => {
    localStorage.clear()
    await mount(<ChannelsPageProbe />)

    const entry = findAll('[data-qy-reset-entry="toolbar"]')[0]
    assert.equal(
      (entry.textContent ?? '').trim(),
      qyEn.qy_chops_reset_action,
      '项目方已经找了三次，一个没有标签的图标不足以被第四次找到'
    )
  })
})
