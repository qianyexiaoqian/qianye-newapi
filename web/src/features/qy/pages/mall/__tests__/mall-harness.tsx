/* oxlint-disable react/only-export-components -- 测试夹具：同时导出挂载器与工具函数，不参与 Fast Refresh */
/**
 * 星屑商城（用户端 + 管理端）渲染测试的夹具。
 *
 * 形状照 `pages/lottery/__tests__/screen-harness.tsx`，多出三样商城要用的东西：
 *   1. **语言包直接用 `zh.json`**:商城文案已并入主包(集成前曾叠过片段文件
 *      `pending-mall.zh.json`,合并后删除);
 *   2. 假 adapter 能**返回失败**（`qyMallHttpError`）：下单弹窗的核心行为是
 *      "先不带密码提交、吃到 qy_pay_pwd_* 才显示密码格"，没有 400 就测不到；
 *   3. 记下每一次请求的 method / body / headers：幂等键沿用、密码只在第二次
 *      带上、验密走请求头 —— 这些都只有"发出去的那条请求"能证明。
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
// happy-dom 的 matchMedia 只认它自己的查询；QyResponsiveDialog 靠
// `(max-width: 767px)` 判桌面 / 移动，这里固定成桌面。
Object.defineProperty(globalThis, 'matchMedia', {
  configurable: true,
  value: (queryText: string) => ({
    matches: false,
    media: queryText,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    onchange: null,
    dispatchEvent: () => false,
  }),
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
export async function cleanupQyMallScreens() {
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

const FAILURE = Symbol('qy-mall-http-failure')

type Failure = {
  [FAILURE]: true
  status: number
  code: string
  message: string
}

/** 让假 adapter 回一条**失败**响应（非 2xx + qy 信封）。 */
export function qyMallHttpError(
  status: number,
  code: string,
  message = ''
): Failure {
  return { [FAILURE]: true, status, code, message }
}

function isFailure(value: unknown): value is Failure {
  return typeof value === 'object' && value !== null && FAILURE in value
}

export type QyMallSentRequest = {
  method: string
  url: string
  params: Record<string, unknown>
  body: unknown
  headers: Record<string, unknown>
}

export type QyMallScreen = {
  container: HTMLElement
  /** 每一次 qy 请求，按发出顺序。 */
  sent: QyMallSentRequest[]
  /** 整个 body 的可见文本（空白折叠）。弹窗走 portal，所以不能只读 container。 */
  text: () => string
  /** 屏幕上所有按钮的文字。 */
  buttons: () => string[]
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
   * 返回 {@link qyMallHttpError} 的结果则让这条请求失败。
   */
  respond: (req: {
    method: string
    url: string
    params: Record<string, unknown>
    body: unknown
  }) => unknown
  /** 当前登录用户的角色，默认普通用户。 */
  role?: number
  path?: string
}

function collapse(raw: string): string {
  return raw.replaceAll(/\s+/g, ' ').trim()
}

/**
 * 挂一屏。
 *
 * 扩展配置直接塞进 react-query 缓存：它是 `useQyConfig` 的数据源，不预置的话
 * 每次挂载都会多一条 `/config` 请求混进 `sent`，而且星屑单位名要从它读。
 */
export async function mountQyMallScreen(
  options: MountOptions
): Promise<QyMallScreen> {
  // 同一进程里别的测试文件会用只含 zh.json 的资源重新 init 这个单例,把片段
  // (pending-visual.zh.json)冲掉;挂载前把本夹具的语言包再并回去,与生产侧
  // registerQyResources 同一条路径(deep + overwrite),文件顺序不再影响结果。
  i18next.addResourceBundle('zh', 'translation', zhKeys, true, true)
  await cleanupQyMallScreens()

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

  const sent: QyMallSentRequest[] = []
  api.defaults.adapter = async (config) => {
    const method = String(config.method ?? 'get').toUpperCase()
    const url = String(config.url ?? '')
    const params = (config.params ?? {}) as Record<string, unknown>
    let body: unknown = config.data
    if (typeof body === 'string') {
      try {
        body = JSON.parse(body)
      } catch {
        // 不是 JSON（multipart 之类）：原样记下。
      }
    }
    // AxiosHeaders 是一个类实例，自有可枚举属性正好就是各个请求头。
    const headers = Object.fromEntries(
      Object.entries(config.headers ?? {})
    ) as Record<string, unknown>
    sent.push({ method, url, params, body, headers })

    const reply = options.respond({ method, url, params, body })
    if (isFailure(reply)) {
      const error = Object.assign(new Error(reply.message), {
        config,
        response: {
          status: reply.status,
          data: { success: false, code: reply.code, message: reply.message },
        },
      })
      throw error
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
      stardust: true,
      mall: true,
      pay_password: true,
    },
    stardust: { show_entry: true, name: '' },
    mall: { show_entry: true },
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
  const mallRoute = createRoute({
    getParentRoute: () => authRoute,
    path: '/qy/mall',
    component: Screen,
  })
  const adminRoute = createRoute({
    getParentRoute: () => authRoute,
    path: '/qy/admin/mall',
    component: Screen,
  })
  const router = createRouter({
    history: createMemoryHistory({
      initialEntries: [options.path ?? '/qy/mall'],
    }),
    routeTree: rootRoute.addChildren([
      authRoute.addChildren([mallRoute, adminRoute]),
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
    buttons: () => allButtons().map((node) => collapse(node.textContent ?? '')),
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
      // useId 生成的 id 带着 `«»` 这类字符，属性选择器不需要转义它们。
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
