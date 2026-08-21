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
 * 桌面表格里 CC Switch 的**第一次点击**必须有反应，而且弹出来的窗口不会
 * 被下一次表格刷新吞掉。
 *
 * # 缺陷形状
 *
 * `useApiKeysColumns(now)` 每次渲染都返回全新的数组字面量。操作列的 cell 原先
 * 写成内联箭头，于是每次渲染都是一个新函数；flexRender 走的是
 * `createElement(cell, props)`，React 把「新的函数」当成**新的组件类型**，
 * 整个单元格子树连同 `DataTableRowActions` 一起被卸载重挂。
 *
 * 两个后果都住在这一行上：
 *   ① 第一次点 CC Switch 什么都不会发生 —— onClick 里 `await resolveRealKey(id)`
 *      先写 Provider 的 loadingKeys，Provider 一变表格重渲染 → 本行重挂 →
 *      await 回来之后 setPendingKey 落在已卸载的旧实例上，无声丢弃。必须点第二次。
 *   ② 好不容易点开的线路选择窗最多活 30 秒：ApiKeysTable 每 30 秒推进一次 now。
 *
 * 这条测试把**真实**的 columns → useDataTable → DataTableRow(flexRender) →
 * DataTableRowActions 整条链挂起来，只问两句：第一次点开没开、刷新一次还在不在。
 * 手机卡片视图不走 flexRender，所以不受影响，也不在这里验。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

import upstreamEn from '@/i18n/locales/en.json'
import qyEn from '@/i18n/qy/en.json'

const domWindow = new Window({ width: 1440, height: 900 })
const domGlobals = [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
  'HTMLElement',
  'HTMLInputElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'KeyboardEvent',
  'MouseEvent',
  'PointerEvent',
  'MutationObserver',
  'ResizeObserver',
  'IntersectionObserver',
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

const { act, useState } = await import('react')
const { createRoot } = await import('react-dom/client')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
await i18next.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: { ...upstreamEn, ...qyEn } } },
})

const { api } = await import('@/lib/api')
const { qyKeys } = await import('../../../lib/query-keys')
const { ApiKeysProvider } =
  await import('@/features/keys/components/api-keys-provider')
const { useApiKeysColumns } =
  await import('@/features/keys/components/api-keys-columns')
const { flexRender } = await import('@tanstack/react-table')
const { TooltipProvider } = await import('@/components/ui/tooltip')

/*
 * 解析真实密钥必须是**异步**的：同步返回会让 setPendingKey 与写 loadingKeys 落在
 * 同一批更新里，重挂发生在它之后，缺陷就复现不出来 —— 那样这条测试会以错误的
 * 理由变绿。
 */
api.defaults.adapter = async (config) => {
  await new Promise((resolve) => setTimeout(resolve, 20))
  return {
    data: { success: true, data: { key: 'real-key' } },
    status: 200,
    statusText: 'OK',
    headers: {},
    config,
  }
}

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

localStorage.setItem(
  'status',
  JSON.stringify({ server_address: 'https://site.example.com' })
)

const API_KEY = {
  id: 42,
  name: 'my key',
  key: 'abcd',
  status: 1,
  remain_quota: 500000,
  used_quota: 0,
  unlimited_quota: false,
  expired_time: -1,
  created_time: 1700000000,
  accessed_time: 1700000000,
  group: 'default',
  auto_groups: null,
  cross_group_retry: false,
  model_limits_enabled: false,
  model_limits: '',
  allow_ips: '',
}

/** 两条线路：只有一条时 CC Switch 直通配置窗，验不到选择窗被吞掉。 */
const LINES = [
  { id: 7, name: 'Primary', remark: '', url: 'https://primary.example.com' },
  { id: 9, name: 'Overseas', remark: 'CDN', url: 'https://cdn.example.com' },
]

let bumpNow: (() => void) | null = null

/*
 * 与 ApiKeysTable 同形的最小复现：now 每 30 秒推进一次，columns 随之整份重建，
 * 操作列那一格由**真实的 flexRender** 渲染。
 *
 * 刻意不搭整张 DataTable：这条用例要验的只有一件事 —— flexRender 拿到的
 * cell 是不是同一个组件类型。把分页/筛选/列宽持久化拉进来，只会让失败原因
 * 多出十几种可能，而它们与这条缺陷没有任何关系。
 */
function TableHarness() {
  const [now, setNow] = useState(() => Date.now())
  bumpNow = () => setNow((current) => current + 30_000)
  const columns = useApiKeysColumns(now)
  const actions = columns.find((column) => column.id === 'actions')
  if (actions?.cell == null) return null
  return (
    <>{flexRender(actions.cell, { row: { original: API_KEY } } as never)}</>
  )
}

let mounted: {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
} | null = null

async function unmount() {
  if (mounted == null) return
  const current = mounted
  mounted = null
  await act(async () => current.root.unmount())
  current.container.remove()
}

after(unmount)

async function mount() {
  await unmount()
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  queryClient.setQueryData(qyKeys.apiAddresses(), LINES)
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <TooltipProvider>
          <ApiKeysProvider>
            <TableHarness />
          </ApiKeysProvider>
        </TooltipProvider>
      </QueryClientProvider>
    )
  })
  mounted = { container, root }
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 30))
  })
}

function ccSwitchButton(): HTMLElement | null {
  return document.body.querySelector('button[aria-label="CC Switch"]')
}

/** 线路选择窗口上的单选项，id 形如 qy-aa-<线路 id>。 */
function routeOptionCount(): number {
  return document.body.querySelectorAll('[id^="qy-aa-"]').length
}

describe('CC Switch：第一次点就有反应', () => {
  test('一次点击就弹出线路选择，不需要点两次', async () => {
    await mount()

    const button = ccSwitchButton()
    assert.ok(button != null, '操作列里没有 CC Switch 按钮')

    await act(async () => {
      button.click()
      await new Promise((resolve) => setTimeout(resolve, 60))
    })

    assert.ok(
      routeOptionCount() > 0,
      '第一次点击什么都没发生 —— 密钥请求正常返回了，结果却被一次重挂丢掉了。' +
        '操作列的 cell 必须是模块级的稳定组件引用，不能写成内联箭头'
    )
  })

  test('表格定时刷新一次，已经打开的线路选择窗不会凭空消失', async () => {
    await mount()

    const button = ccSwitchButton()
    assert.ok(button != null)
    await act(async () => {
      button.click()
      await new Promise((resolve) => setTimeout(resolve, 60))
    })
    assert.ok(routeOptionCount() > 0, '窗口没打开，这一轮验不到它会不会被吞掉')

    // ApiKeysTable 的 30 秒 setInterval 跳一次。
    await act(async () => {
      bumpNow?.()
      await new Promise((resolve) => setTimeout(resolve, 20))
    })

    assert.ok(
      routeOptionCount() > 0,
      '表格刷新一次就把线路选择窗吞掉了 —— 线路最多 30 条，用户读到一半窗口没了'
    )
  })

  test('同一个 now 下重复渲染，操作列的 cell 必须是同一个引用', async () => {
    const seen: unknown[] = []
    function Probe() {
      const columns = useApiKeysColumns(1_700_000_000_000)
      seen.push(columns.find((column) => column.id === 'actions')?.cell)
      return null
    }
    const container = document.createElement('div')
    document.body.appendChild(container)
    const root = createRoot(container)
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <Probe />
        </QueryClientProvider>
      )
    })
    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <Probe />
        </QueryClientProvider>
      )
    })
    await act(async () => root.unmount())
    container.remove()

    assert.ok(seen.length >= 2, '探针没渲染两次')
    assert.equal(
      seen[0],
      seen[seen.length - 1],
      'actions 列的 cell 每次渲染都是新函数 —— flexRender 会把它当成新的组件类型，' +
        '整个单元格连同两个弹窗的本地 state 一起被卸载重挂'
    )
  })
})
