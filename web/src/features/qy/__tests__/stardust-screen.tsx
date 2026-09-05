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
/* oxlint-disable react/only-export-components -- 测试夹具：同时导出挂载器与桩，不参与 Fast Refresh */
/**
 * 星屑三张页面（用户端选择夹 / 管理端账本 / 管理端配置）的 happy-dom 渲染夹具。
 *
 * 形状照 `pages/lottery/__tests__/screen-harness.tsx`：真的把一屏渲染出来、把
 * 可见文本与发出去的请求交出去，判断标准写在各自的测试文件里。三张页面共用一份
 * 是因为它们要的东西完全一样 —— 真路由器（选择夹的 hash）、真 react-query、
 * 真 i18n 语言包、以及一个按 URL 分派的假 axios adapter。
 *
 * 语言包 = `zh.json`(星屑文案已并入主包;集成前曾叠过片段文件 `pending-stardust.zh.json`)。
 * 键缺失时 i18next 原样吐键名,下面的中文断言当场变红。
 */
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

// __tests__ → qy → features → src
const srcDir = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..')

const domWindow = new Window({
  height: 900,
  url: 'http://localhost/',
  width: 1280,
})
for (const key of [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
  'HTMLElement',
  'HTMLInputElement',
  'HTMLSelectElement',
  'HTMLTextAreaElement',
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
] as const) {
  const value = domWindow[key as keyof Window]
  if (value === undefined) continue
  Object.defineProperty(globalThis, key, { configurable: true, value })
}
// happy-dom 没实现 scrollTo / scrollIntoView，而路由器每次导航都会调前者 ——
// 缺了它导航会在半路抛错，整棵树掉进 CatchBoundary，看起来像是断言写错了。
for (const name of ['scrollTo', 'scrollIntoView'] as const) {
  const noop = () => {}
  Object.defineProperty(globalThis, name, { configurable: true, value: noop })
  Object.defineProperty(domWindow, name, { configurable: true, value: noop })
}
Object.defineProperty(globalThis.Element.prototype, 'scrollIntoView', {
  configurable: true,
  value: () => {},
})

const { act } = await import('react')
const React = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const {
  Outlet,
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} = await import('@tanstack/react-router')

function readBundle(name: string): Record<string, string> {
  return JSON.parse(
    readFileSync(join(srcDir, 'i18n', 'qy', name), 'utf8')
  ) as Record<string, string>
}

/** 主语言包。断言用它查中文文案，不从产品代码回读。 */
export const zh: Record<string, string> = {
  ...readBundle('zh.json'),
}

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zh',
  nsSeparator: false,
  resources: { zh: { translation: zh } },
})

const { api } = await import('@/lib/api')
const { ROLE } = await import('@/lib/roles')
const { useAuthStore } = await import('@/stores/auth-store')
const { QY_DISABLED_CONFIG } = await import('@/features/qy/lib/config-query')
const { qyKeys } = await import('@/features/qy/lib/query-keys')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

/** 一条发出去的请求：方法、路径（不含查询串）、查询参数、请求体。 */
export type QyProbeRequest = {
  method: string
  url: string
  params: Record<string, unknown>
  body: unknown
}

/**
 * 一条桩响应。`status` 缺省 200；给 4xx / 5xx 时 adapter 会按 axios 的方式
 * reject，走的是产品代码里真实的 `toQyError` 那条路。
 */
export type QyProbeReply = {
  data?: unknown
  status?: number
  code?: string
  message?: string
}

type Responder = (request: QyProbeRequest) => QyProbeReply | undefined

const mounted: { container: HTMLElement; root: { unmount: () => void } }[] = []

/** 拆掉本文件挂过的所有根。每个测试文件的 `after` 里调一次。 */
export async function cleanupQyStardustScreens() {
  // 先让上一屏还在路上的异步更新（mutation 收尾、invalidate 之后的重取）落地，
  // 否则它们会在下一屏挂上之后才到，落在 act 之外。
  if (mounted.length > 0) await tick(80)
  for (const entry of mounted) {
    await act(async () => {
      entry.root.unmount()
    })
    entry.container.remove()
  }
  mounted.length = 0
  document.body.innerHTML = ''
}

/**
 * 把登录态设成给定角色。手调闸门与选择夹的可见性都读它。
 *
 * 包在 act 里：上一个用例的那一屏此刻可能还挂着，zustand 会同步通知它重渲染，
 * 不包的话那一次更新就落在 act 之外。
 */
export async function setQyProbeRole(role: number) {
  await act(async () => {
    useAuthStore.setState((state) => ({
      ...state,
      auth: {
        ...state.auth,
        user: { id: 1, username: 'qy-probe', role, status: 1 },
        accessToken: 'probe',
      },
    }))
  })
}

export { ROLE }

export type QyStardustScreen = {
  container: HTMLElement
  /** 可见文本（空白折叠）。弹窗走 portal，所以从整个 body 上读。 */
  text: () => string
  /** 每一条 qy 请求，按发出顺序。 */
  sent: QyProbeRequest[]
  /** 点一颗按钮（按可见文字精确匹配）并等一拍。找不到返回 false。 */
  click: (label: string) => Promise<boolean>
  /** 往一个输入框里敲字（按 placeholder 或 label 文案定位）。 */
  type: (locator: string, value: string) => Promise<boolean>
  /** 选一个原生下拉的值（按 aria-label / label 文案定位）。 */
  select: (locator: string, value: string) => Promise<boolean>
  /** 等一拍，让 react-query / 路由器把异步状态冲完。 */
  settle: () => Promise<void>
  router: { state: { location: { hash: string } } }
}

type MountOptions = {
  element: React.ReactNode
  /** 路由路径与初始 URL。缺省都是 `/qy/stardust`。 */
  path?: string
  initial?: string
  respond: Responder
  /** 引导端点的 features 覆盖；缺省 stardust + mall 开。 */
  features?: Partial<Record<string, boolean>>
}

function findByLocator(locator: string): HTMLElement | null {
  const fields = [
    ...document.body.querySelectorAll('input, textarea, select'),
  ] as HTMLElement[]
  for (const field of fields) {
    if (field.getAttribute('placeholder') === locator) return field
    if (field.getAttribute('aria-label') === locator) return field
    const id = field.getAttribute('id')
    if (id != null) {
      const label = [...document.body.querySelectorAll('label')].find(
        (node) =>
          node.getAttribute('for') === id &&
          (node.textContent ?? '').trim() === locator
      )
      if (label != null) return field
    }
  }
  return null
}

async function tick(ms = 60) {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms))
  })
}

/**
 * 挂一屏。
 *
 * 引导端点直接预置进缓存：它是 `useQyConfig` 的数据源，不预置的话每次挂载都会
 * 多一条 `/config` 请求，而且首帧会先渲染"未启用"再长出来。
 */
export async function mountQyStardustScreen(
  options: MountOptions
): Promise<QyStardustScreen> {
  // 同一进程里别的测试文件会用只含 zh.json 的资源重新 init 这个单例,把片段
  // (pending-visual.zh.json)冲掉;挂载前把本夹具的语言包再并回去,与生产侧
  // registerQyResources 同一条路径(deep + overwrite),文件顺序不再影响结果。
  i18next.addResourceBundle('zh', 'translation', zh, true, true)
  await cleanupQyStardustScreens()

  const sent: QyProbeRequest[] = []
  api.defaults.adapter = async (config) => {
    const url = String(config.url ?? '').split('?')[0]
    const method = String(config.method ?? 'get').toUpperCase()
    let body: unknown = config.data
    if (typeof body === 'string') {
      try {
        body = JSON.parse(body)
      } catch {
        /* 不是 JSON 就原样留着 */
      }
    }
    const request: QyProbeRequest = {
      method,
      url,
      params: (config.params ?? {}) as Record<string, unknown>,
      body,
    }
    sent.push(request)
    const reply = options.respond(request) ?? { data: {} }
    const status = reply.status ?? 200
    if (status >= 400) {
      const response = {
        config,
        data: {
          success: false,
          code: reply.code ?? '',
          message: reply.message ?? '',
        },
        headers: {},
        status,
        statusText: 'ERR',
      }
      throw Object.assign(new Error(`probe ${status}`), { response })
    }
    // 上游套餐清单走的是上游信封（同一形状），所以两边共用这一份。
    return {
      config,
      data: { success: true, message: '', data: reply.data ?? {} },
      headers: {},
      status,
      statusText: 'OK',
    }
  }

  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: Infinity } },
  })
  queryClient.setQueryData(qyKeys.config(), {
    ...QY_DISABLED_CONFIG,
    enabled: true,
    available: true,
    features: {
      ...QY_DISABLED_CONFIG.features,
      stardust: true,
      mall: true,
      ...options.features,
    },
    stardust: { show_entry: true, name: '' },
    mall: { show_entry: true },
  })

  const Screen = () => (
    <QueryClientProvider client={queryClient}>
      {options.element}
    </QueryClientProvider>
  )
  const path = options.path ?? '/qy/stardust'
  const rootRoute = createRootRoute({ component: Outlet })
  const authRoute = createRoute({
    getParentRoute: () => rootRoute,
    id: '_authenticated',
    component: Outlet,
  })
  const pageRoute = createRoute({
    getParentRoute: () => authRoute,
    path,
    component: Screen,
  })
  const router = createRouter({
    history: createMemoryHistory({
      initialEntries: [options.initial ?? path],
    }),
    routeTree: rootRoute.addChildren([authRoute.addChildren([pageRoute])]),
  })

  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(<RouterProvider router={router as never} />)
  })
  await tick(80)
  mounted.push({ container, root })

  return {
    container,
    sent,
    router: router as unknown as QyStardustScreen['router'],
    text: () => (document.body.textContent ?? '').replaceAll(/\s+/g, ' '),
    settle: () => tick(80),
    click: async (label) => {
      const node = [
        ...document.body.querySelectorAll('button,[role="button"]'),
      ].find((item) => (item.textContent ?? '').trim() === label)
      if (node == null) return false
      await act(async () => {
        node.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      })
      await tick(60)
      return true
    },
    type: async (locator, value) => {
      const field = findByLocator(locator)
      if (field == null) return false
      await act(async () => {
        // React 19 用自己的 value tracker 拦截 setter：直接赋 .value 不会触发
        // onChange，必须走原型上的 setter 再派发 input 事件。
        const setter = Object.getOwnPropertyDescriptor(
          field.constructor.prototype as object,
          'value'
        )?.set
        setter?.call(field, value)
        field.dispatchEvent(new Event('input', { bubbles: true }))
      })
      await tick(60)
      return true
    },
    select: async (locator, value) => {
      const field = findByLocator(locator)
      if (!(field instanceof HTMLSelectElement)) return false
      await act(async () => {
        field.value = value
        field.dispatchEvent(new Event('change', { bubbles: true }))
      })
      await tick(60)
      return true
    },
  }
}

export { React, act }
