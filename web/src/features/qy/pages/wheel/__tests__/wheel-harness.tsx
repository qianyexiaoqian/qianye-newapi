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
 * 星屑转盘（列表页 / 转动弹窗）的 happy-dom 渲染夹具。
 *
 * 形状照 `pages/mall/__tests__/mall-harness.tsx`，多出两样转盘要用的东西：
 *   1. **语言包直接用 `zh.json`**:转盘文案已并入主包(集成前曾叠过片段文件
 *      `pending-wheel.zh.json`,合并后删除)。键缺失时 i18next 原样吐键名,
 *      下面的中文断言当场变红；
 *   2. **`prefers-reduced-motion: reduce` 恒为真**：转盘在这一档下不旋转、直接
 *      显示结果，测试因此不必等 2 秒的过渡。这正是 design-14 §4 要求的那条降级路径，
 *      测的是真实代码路径而不是绕开动效。
 */
import { Window } from 'happy-dom'

const domWindow = new Window({ width: 1280, height: 900 })
for (const key of [
  'window',
  'document',
  'navigator',
  'localStorage',
  'sessionStorage',
  'HTMLElement',
  'HTMLInputElement',
  'HTMLTextAreaElement',
  'HTMLSelectElement',
  'HTMLButtonElement',
  'SVGElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MouseEvent',
  'PointerEvent',
  'KeyboardEvent',
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
for (const name of ['scrollTo', 'scrollIntoView'] as const) {
  const noop = () => {}
  Object.defineProperty(globalThis, name, { configurable: true, value: noop })
  Object.defineProperty(domWindow, name, { configurable: true, value: noop })
}
Object.defineProperty(globalThis.Element.prototype, 'scrollIntoView', {
  configurable: true,
  value: () => {},
})
// happy-dom 的 matchMedia 只认它自己的查询。QyResponsiveDialog 靠
// `(max-width: 767px)` 判桌面 / 移动，这里固定成桌面；转盘靠
// `(prefers-reduced-motion: reduce)` 决定要不要旋转，这里固定成"缩减动效"。
// 同一进程里二十几个测试文件各自在模块加载时定义 globalThis.matchMedia,后加载的
// 会把这一份覆盖掉;所以它不只在加载时装一次,每次 mount 前再装一次(与下面的
// addResourceBundle 同一条理由)。
function installWheelMatchMedia() {
  Object.defineProperty(globalThis, 'matchMedia', {
    configurable: true,
    value: (queryText: string) => ({
      matches: queryText.includes('prefers-reduced-motion'),
      media: queryText,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      onchange: null,
      dispatchEvent: () => false,
    }),
  })
}
installWheelMatchMedia()

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

/** 中文语言包:断言从这里取译文,而不是把某一句中文写死在测试里。 */
export const zhKeys = {
  ...((await import('@/i18n/qy/zh.json')).default as Record<string, string>),
}

await i18next.use(initReactI18next).init({
  interpolation: { escapeValue: false },
  lng: 'zh',
  nsSeparator: false,
  resources: { zh: { translation: zhKeys } },
})

const { api } = await import('@/lib/api')
const { ROLE } = await import('@/lib/roles')
const { useAuthStore } = await import('@/stores/auth-store')
const { QY_DISABLED_CONFIG } = await import('@/features/qy/lib/config-query')
const { qyKeys } = await import('@/features/qy/lib/query-keys')

const originalAdapter = api.defaults.adapter

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const mounted: { container: HTMLElement; root: { unmount: () => void } }[] = []

/** 拆掉本文件挂过的所有根，并把共享的 axios adapter 还回去。 */
export async function cleanupQyWheelScreens() {
  api.defaults.adapter = originalAdapter
  for (const entry of mounted) {
    await act(async () => {
      entry.root.unmount()
    })
    entry.container.remove()
  }
  mounted.length = 0
  document.body.innerHTML = ''
}

const FAILURE = Symbol('qy-wheel-http-failure')

type Failure = {
  [FAILURE]: true
  status: number
  code: string
  message: string
}

/** 让假 adapter 回一条**失败**响应（非 2xx + qy 信封）。 */
export function qyWheelHttpError(
  status: number,
  code: string,
  message = ''
): Failure {
  return { [FAILURE]: true, status, code, message }
}

function isFailure(value: unknown): value is Failure {
  return typeof value === 'object' && value !== null && FAILURE in value
}

export type QyWheelSentRequest = {
  method: string
  url: string
  params: Record<string, unknown>
  body: unknown
}

export type QyWheelScreen = {
  container: HTMLElement
  /** 每一次 qy 请求，按发出顺序。 */
  sent: QyWheelSentRequest[]
  /** 整个 body 的可见文本（空白折叠）。弹窗走 portal，所以不能只读 container。 */
  text: () => string
  /** 按可见文字精确找一颗按钮。 */
  button: (label: string) => HTMLButtonElement | null
  /** 点一颗按钮（按可见文字精确匹配）并等一拍。找不到返回 false。 */
  click: (label: string) => Promise<boolean>
  /** 按 `<label>` 文字找它关联的输入框。 */
  field: (label: string) => HTMLInputElement | HTMLTextAreaElement | null
  /** 往输入框里"打字"（走原型 setter + input 事件，React 19 的受控值才认）。 */
  type: (
    node: HTMLInputElement | HTMLTextAreaElement,
    text: string
  ) => Promise<void>
  /** 再等一拍（让在途的请求与 react-query 的通知落地）。 */
  settle: () => Promise<void>
}

type MountOptions = {
  element: React.ReactNode
  /**
   * 按请求回数据；返回 `undefined` 表示不认识这条请求（回一个空对象）。
   * 返回 {@link qyWheelHttpError} 的结果则让这条请求失败。
   */
  respond: (req: QyWheelSentRequest) => unknown
  /** 当前登录用户的角色，默认普通用户。 */
  role?: number
  /**
   * 初始 URL，默认 `/qy/lottery`（转盘所在的宿主页；`/qy/wheel` 在生产里只是
   * 一条重定向，2026-09-05 起转盘是「抽奖竞猜」选择夹的一张标签）。
   */
  path?: string
}

function collapse(raw: string): string {
  return raw.replaceAll(/\s+/g, ' ').trim()
}

/**
 * 挂一屏。
 *
 * 扩展配置直接塞进 react-query 缓存：它是 `useQyConfig` 的数据源，不预置的话
 * 每次挂载都会多一条 `/config` 请求混进 `sent`，而且星屑单位名与"转盘入口开没开"
 * 都要从它读。
 */
export async function mountQyWheelScreen(
  options: MountOptions
): Promise<QyWheelScreen> {
  // 同一进程里别的测试文件会用只含 zh.json 的资源重新 init 这个单例,把片段
  // (pending-visual.zh.json)冲掉;挂载前把本夹具的语言包再并回去,与生产侧
  // registerQyResources 同一条路径(deep + overwrite),文件顺序不再影响结果。
  installWheelMatchMedia()
  i18next.addResourceBundle('zh', 'translation', zhKeys, true, true)
  await cleanupQyWheelScreens()

  useAuthStore.setState((state) => ({
    ...state,
    auth: {
      ...state.auth,
      accessToken: 'probe',
      user: {
        id: 7,
        role: options.role ?? ROLE.USER,
        status: 1,
        username: 'qy-probe',
      },
    },
  }))

  const sent: QyWheelSentRequest[] = []
  api.defaults.adapter = async (config) => {
    const method = String(config.method ?? 'get').toUpperCase()
    const url = String(config.url ?? '')
    const params = (config.params ?? {}) as Record<string, unknown>
    let body: unknown = config.data
    if (typeof body === 'string') {
      try {
        body = JSON.parse(body)
      } catch {
        // 不是 JSON：原样记下。
      }
    }
    const req = { method, url, params, body }
    sent.push(req)

    const reply = options.respond(req)
    if (isFailure(reply)) {
      throw Object.assign(new Error(reply.message), {
        config,
        response: {
          status: reply.status,
          data: { success: false, code: reply.code, message: reply.message },
        },
      })
    }
    return {
      config,
      data: { success: true, message: '', data: reply ?? {} },
      headers: {},
      status: 200,
      statusText: 'OK',
    }
  }

  const queryClient = new QueryClient({
    defaultOptions: { queries: { gcTime: Infinity, retry: false } },
  })
  queryClient.setQueryData(qyKeys.config(), {
    ...QY_DISABLED_CONFIG,
    enabled: true,
    available: true,
    features: {
      ...QY_DISABLED_CONFIG.features,
      lottery: true,
      stardust: true,
      pay_password: true,
    },
    lottery: {
      show_entry: true,
      proof_public: true,
      plays: {
        draw_rank: true,
        draw_prob: true,
        draw_ball: true,
        guess: true,
        wheel: true,
      },
    },
    stardust: { show_entry: true, name: '' },
  })

  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)

  const Screen = () => (
    <QueryClientProvider client={queryClient}>
      {options.element}
    </QueryClientProvider>
  )
  const rootRoute = createRootRoute({ component: Outlet })
  const authRoute = createRoute({
    getParentRoute: () => rootRoute,
    id: '_authenticated',
    component: Outlet,
  })
  // 转盘正文挂在宿主 `/qy/lottery` 上（选择夹的一张标签）；被测元素直接渲染在
  // 这条路由里，标签栏本身由 `pages/lottery/__tests__/hub-tabs.test.tsx` 守。
  const hubRoute = createRoute({
    getParentRoute: () => authRoute,
    path: '/qy/lottery',
    component: Screen,
  })
  // 转盘详情与其余玩法共用 `/qy/lottery/$actNo`；卡片上的「详情」指向那里。
  const detailRoute = createRoute({
    getParentRoute: () => authRoute,
    path: '/qy/lottery/$actNo/',
    component: Screen,
  })
  const adminRoute = createRoute({
    getParentRoute: () => authRoute,
    path: '/qy/admin/lottery',
    component: Screen,
  })
  // 商品奖中奖之后「去查看」指向商城的「我的订单」（`/qy/mall` + hash）。
  const mallRoute = createRoute({
    getParentRoute: () => authRoute,
    path: '/qy/mall',
    component: Screen,
  })
  const router = createRouter({
    history: createMemoryHistory({
      initialEntries: [options.path ?? '/qy/lottery'],
    }),
    routeTree: rootRoute.addChildren([
      authRoute.addChildren([hubRoute, detailRoute, adminRoute, mallRoute]),
    ]),
  })

  await act(async () => {
    root.render(<RouterProvider router={router as never} />)
  })
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 80))
  })
  mounted.push({ container, root })

  const settle = async () => {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 60))
    })
  }
  const allButtons = () => [
    ...document.body.querySelectorAll<HTMLButtonElement>(
      'button,[role="button"]'
    ),
  ]
  const button = (label: string) =>
    allButtons().find((node) => collapse(node.textContent ?? '') === label) ??
    null

  return {
    container,
    sent,
    text: () => collapse(document.body.textContent ?? ''),
    button,
    click: async (label) => {
      const node = button(label)
      if (node == null) return false
      await act(async () => {
        node.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      })
      await settle()
      return true
    },
    field: (label) => {
      const tag = [...document.body.querySelectorAll('label')].find(
        (node) => collapse(node.textContent ?? '') === label
      )
      if (tag == null) return null
      const target = document.querySelector(`[id="${tag.htmlFor}"]`)
      if (
        target instanceof HTMLInputElement ||
        target instanceof HTMLTextAreaElement
      ) {
        return target
      }
      return null
    },
    type: async (node, text) => {
      await act(async () => {
        // React 19 + happy-dom：直接改 `.value` 会被 React 的受控值追踪吞掉，
        // 必须走原型上的 setter 再派发 input 事件。
        const proto =
          node instanceof HTMLTextAreaElement
            ? HTMLTextAreaElement.prototype
            : HTMLInputElement.prototype
        Object.getOwnPropertyDescriptor(proto, 'value')?.set?.call(node, text)
        node.dispatchEvent(new Event('input', { bubbles: true }))
      })
    },
    settle,
  }
}

export { React, act }
