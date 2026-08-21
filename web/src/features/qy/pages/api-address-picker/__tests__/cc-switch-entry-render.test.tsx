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
 * 「一键导入 CC Switch」在密钥行的操作列里**真的渲染出来了**。
 *
 * # 为什么要真渲染一遍
 *
 * 项目方原话：「API密钥那把【一键导入CC Switch】做成一个按钮显示在操作列。」
 * 在这之前它是「⋯」下拉里的一项 —— 功能齐全，但用户要先想到去点那个三点
 * 图标。本仓的「实现了但界面上点不到」已经复发过五次以上，而它的共同特征
 * 恰恰是：源码里那一段完好无损，AST 守卫、typecheck、单元测试统统绿。
 *
 * 所以这条测试把真实的 `DataTableRowActions` 挂起来，只问一句：**不做任何
 * 展开动作**，DOM 里有没有那个按钮。同目录的 `cc-switch-wiring.test.ts` 用
 * AST 守"它必须先选线路"，两者互补：一条守接线，一条守可达。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

import upstreamEn from '@/i18n/locales/en.json'
import qyEn from '@/i18n/qy/en.json'

const domWindow = new Window({ width: 1280, height: 900 })
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

const { act } = await import('react')
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
const { DataTableRowActions } =
  await import('@/features/keys/components/data-table-row-actions')
const { TooltipProvider } = await import('@/components/ui/tooltip')

/** 行内解析真实密钥走 `/api/token/:id`。 */
api.defaults.adapter = async (config) => ({
  data: { success: true, data: { key: 'real-key' } },
  status: 200,
  statusText: 'OK',
  headers: {},
  config,
})

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

const LINES = [
  { id: 7, name: 'Primary', remark: '', url: 'https://primary.example.com' },
  { id: 9, name: 'Overseas', remark: 'CDN', url: 'https://cdn.example.com' },
]

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
            <DataTableRowActions row={{ original: API_KEY } as never} />
          </ApiKeysProvider>
        </TooltipProvider>
      </QueryClientProvider>
    )
  })
  mounted = { container, root }
  // 行内的 useChatPresets → useStatus 会在挂载后自己发一次 /api/status，
  // 不等它落地就往下走，那次 setState 会落在 act 之外。
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

function ccSwitchButton(): HTMLElement | null {
  return document.body.querySelector('button[aria-label="CC Switch"]')
}

describe('CC Switch 在密钥行的操作列里', () => {
  test('不展开任何菜单就能看到那个按钮', async () => {
    await mount()

    const button = ccSwitchButton()
    assert.ok(
      button != null,
      '操作列里没有「CC Switch」按钮 —— 它又只剩下拉菜单那一条路了'
    )

    // 「⋯」菜单此刻是关着的：按钮不可能是从展开的菜单里渲染出来的。
    const menuTrigger = document.body.querySelector(
      'button[aria-label="Open menu"]'
    )
    assert.ok(menuTrigger != null, '操作列里应当仍有「⋯」菜单')
    assert.notEqual(
      menuTrigger.getAttribute('aria-expanded'),
      'true',
      '菜单是展开的，这一轮证明不了按钮在菜单之外'
    )
    assert.equal(
      menuTrigger.compareDocumentPosition(button) &
        Node.DOCUMENT_POSITION_CONTAINED_BY,
      0,
      '「CC Switch」按钮长在「⋯」菜单里面 —— 那还是要先展开才点得到'
    )
  })

  test('点它先弹线路选择，而不是直接掀开配置窗口', async () => {
    await mount()

    const button = ccSwitchButton()
    assert.ok(button != null)
    // 解析真实密钥是一次异步请求，回来之后才轮到线路选择窗口 —— 所以这次
    // 点击要和它的落地一起裹进同一个 act。
    await act(async () => {
      button.click()
      await new Promise((resolve) => setTimeout(resolve, 0))
    })

    assert.ok(
      document.body.querySelector('#qy-aa-9') != null,
      '有两条线路可选，点「CC Switch」必须先弹出线路选择窗口'
    )
  })
})
