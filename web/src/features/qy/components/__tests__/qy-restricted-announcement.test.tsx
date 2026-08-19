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
 * 受限账号公告在**真实路由器 + 真实闸**里跑一遍。
 *
 * # 守什么
 *
 *   1. 配了公告 → 顶部说明条与落地页都显示它，而它顶掉的**只有**"怎么申诉"
 *      那一句固定文案（标题与不可用清单是站点无关的事实，不能被顶掉）；
 *   2. 没配 / 关掉 → 回落那条固定文案，**不能是空白**。空白是这个功能最贵的
 *      失败方式：一个刚被封号的人打开控制台，看到一块什么都没写的区域；
 *   3. 正常账号一个字都看不到。这段文案的形状是「你的账号已被限制」，
 *      误发给正常用户会让全站以为自己被封了 —— 与这个功能的目的正好相反。
 *
 * i18n 用空 resources 初始化，`t('x')` 回落成键名本身，所以固定文案的断言直接
 * 打在键名上：文案改写不会把测试改红，键被删掉会。公告是运营输入的真实字符串，
 * 因此按字面断言。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { Window } from 'happy-dom'

const domWindow = new Window()
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
  'localStorage',
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

for (const target of [globalThis, domWindow]) {
  Object.defineProperty(target, 'scrollTo', {
    configurable: true,
    value: () => {},
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
await i18next
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: {} } } })

const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
} = await import('@tanstack/react-router')
const { QyRestrictedGate } = await import('../qy-restricted-gate')
const { qyKeys } = await import('../../lib/query-keys')
const { QY_DISABLED_CONFIG } = await import('../../lib/config-query')
const { useAuthStore } = await import('@/stores/auth-store')
const { USER_STATUS } = await import('@/features/users/constants')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const ENABLED_CONFIG = {
  ...QY_DISABLED_CONFIG,
  enabled: true,
  available: true,
  features: {
    transfer: true,
    commission: true,
    withdraw: true,
    availability: true,
    violation: true,
    lottery: true,
    ticket: true,
    group_matrix: true,
  },
}

const NOTICE = {
  enabled: true,
  title: '解除限制的流程',
  body: '请在**工作日 10:00–18:00** 发工单，附上你的订单号。',
}

function PageBody() {
  return <div data-testid='page-body'>page body</div>
}

function Shell() {
  return (
    <QyRestrictedGate>
      <PageBody />
    </QyRestrictedGate>
  )
}

const rootRoute = createRootRoute({ component: Outlet })
const routeTree = rootRoute.addChildren([
  createRoute({
    getParentRoute: () => rootRoute,
    path: '/wallet',
    component: Shell,
  }),
  createRoute({
    getParentRoute: () => rootRoute,
    path: '/qy/tickets',
    component: Shell,
  }),
])

type Snapshot = { html: string; text: string }

/**
 * 挂载一次。
 *
 * `notice` 为 null 表示"后端回了 enabled:false"（没配 / 关掉 / 读取失败三者
 * 在契约上收敛成同一个值，见 lib/restricted-notice.ts）。把它预写进查询缓存，
 * 于是这条链路上唯一没被真的执行的只有 HTTP 本身。
 */
async function mountAt(
  path: string,
  status: number,
  notice: Partial<typeof NOTICE> | null
): Promise<Snapshot> {
  useAuthStore.setState((state) => ({
    ...state,
    auth: {
      ...state.auth,
      user: { id: 1, username: 'probe', role: 1, status },
      accessToken: 'probe',
    },
  }))

  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  queryClient.setQueryData(qyKeys.config(), ENABLED_CONFIG)
  queryClient.setQueryData(
    qyKeys.restrictedNotice(),
    notice ?? { enabled: false, title: '', body: '' }
  )

  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  })

  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)

  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        {/* eslint-disable-next-line @typescript-eslint/no-explicit-any */}
        <RouterProvider router={router as any} />
      </QueryClientProvider>
    )
  })
  await act(async () => {})

  const snapshot: Snapshot = {
    html: container.innerHTML,
    text: container.textContent ?? '',
  }

  await act(async () => root.unmount())
  container.remove()
  return snapshot
}

describe('受限账号公告', () => {
  test('配了公告 → 顶部说明条显示它，并顶掉"怎么申诉"那一句固定文案', async () => {
    const view = await mountAt('/qy/tickets', USER_STATUS.DISABLED, NOTICE)

    assert.ok(view.text.includes(NOTICE.title), '公告标题没出现在横幅上')
    assert.ok(
      view.text.includes('工作日 10:00–18:00'),
      '公告正文没出现在横幅上'
    )
    assert.ok(
      !view.text.includes('qy_restricted_appeal_hint'),
      '公告没有顶掉固定的申诉指引 —— 同一屏上两套互相矛盾的申诉说明'
    )
    // 顶掉的**只有**那一句：这两条是站点无关的事实，公告不会重写它们，
    // 让公告顶掉它们等于要求每个站点在自己的文案里再抄一遍。
    assert.ok(view.text.includes('qy_restricted_banner_title'))
    assert.ok(view.text.includes('qy_restricted_blocked'))
  })

  test('没配公告 → 回落固定文案，绝不是空白', async () => {
    const view = await mountAt('/qy/tickets', USER_STATUS.DISABLED, null)

    assert.ok(
      view.text.includes('qy_restricted_appeal_hint'),
      '公告关掉之后申诉指引必须回来，否则受限用户没有任何下一步'
    )
    assert.ok(!view.text.includes(NOTICE.title))
    assert.ok(view.text.includes('qy_restricted_banner_title'))
  })

  test('开着但内容是空的 → 仍然回落固定文案，不能留一块空白', async () => {
    // 这个形状是真的会出现的：库里的行可以被人手工 UPDATE 清空，缓存也可以被
    // 别处的 setQueryData 写进一份半成品。三道 normalize（后端写入校验、后端
    // 读取、前端出口）里任何一道漏掉，受限用户的首屏上就会出现一块什么都没写
    // 的区域 —— 一个刚被封号的人会以为页面坏了，然后去发工单。
    const view = await mountAt('/qy/tickets', USER_STATUS.DISABLED, {
      enabled: true,
      title: '   ',
      body: '',
    })
    assert.ok(
      view.text.includes('qy_restricted_appeal_hint'),
      '内容为空时必须回落固定文案'
    )
  })

  test('落地页那一屏也看得到公告，而且只印一遍', async () => {
    // 横幅与落地页永远同时在场，所以"落地页上看得到"= 横幅那一份就在这一屏里。
    // 两边各渲染一份的结果是同一段话印两遍，那是上一轮实测后被钉住的形状。
    const view = await mountAt('/wallet', USER_STATUS.DISABLED, NOTICE)
    assert.ok(view.text.includes('qy_restricted_landing_title'))
    assert.ok(view.text.includes('工作日 10:00–18:00'), '这一屏上看不到公告')
    assert.ok(
      view.text.includes('qy_restricted_kept'),
      '「你的东西还在」必须仍在'
    )

    const hits = view.text.split(NOTICE.title).length - 1
    assert.equal(hits, 1, `公告在同一屏里出现了 ${hits} 次`)

    const without = await mountAt('/wallet', USER_STATUS.DISABLED, null)
    assert.ok(without.text.includes('qy_restricted_landing_title'))
    assert.ok(without.text.includes('qy_restricted_kept'))
    assert.ok(!without.text.includes(NOTICE.title))
  })

  test('白名单内的页面(工单页)上公告仍然在 —— 落地页不渲染的那一档', async () => {
    // 这是选横幅而不是落地页的全部理由:受限账号点进工单页时落地页不渲染，
    // 而工单页正是他去申诉的地方，"怎么申诉"那段话在那里必须还在。
    const view = await mountAt('/qy/tickets', USER_STATUS.DISABLED, NOTICE)
    assert.ok(view.text.includes('page body'), '工单页应当照常渲染')
    assert.ok(!view.text.includes('qy_restricted_landing_title'))
    assert.ok(view.text.includes(NOTICE.title))
  })

  test('正文按 Markdown 渲染，而不是当成纯文本印出来', async () => {
    const view = await mountAt('/qy/tickets', USER_STATUS.DISABLED, NOTICE)
    assert.ok(
      view.html.includes('<strong>工作日 10:00–18:00</strong>'),
      'Markdown 没有被解析 —— 运营会在界面上看到一堆星号'
    )
    assert.ok(
      !view.text.includes('**工作日'),
      '源码里的星号不该原样出现在页面上'
    )
  })

  test('公告与管理端预览都必须走不可信净化档', () => {
    /*
     * 这段内容由管理员写、给**受限用户**看，是一条真实的跨信任边界通道：
     * 一个被拿下的管理员账号（或一次管理端 XSS）由此直接升级成对全体受限用户
     * 的钓鱼。默认那份净化配置是给「管理员写、管理员看」的公告/更新日志设计的，
     * 它放行 `<form>` / `<input>` / 任意 `style` / 外链图片 —— 凑起来正好是一个
     * 铺满视口的假「验证身份以解除限制」表单。
     *
     * 断言的是**开关传没传**而不是渲染结果：DOMPurify 在 happy-dom 下与真实
     * 浏览器行为不一致（理由与 tickets/__tests__/ticket-untrusted-markdown.ts
     * 里写的完全相同），白名单内容本身由那条测试守。少传一个布尔 prop 不会有
     * 任何报错，所以只能这样钉。
     */
    const here = dirname(fileURLToPath(import.meta.url))
    const targets = [
      join(here, '..', 'qy-restricted-notice.tsx'),
      // 管理端编辑面：**已从「系统设置 → 内容管理」搬到扩展的「受限账号」页**
      // （项目方原话：「受限制账号，在系统设置里面单独进行配置。」）。
      // 路径跟着改，而不是把这一条删掉 —— 净化档是搬家最容易在复制粘贴里
      // 掉队的东西，而它掉队的表现是运营预览没问题、上线后受限用户首屏上
      // 多一个假登录框。
      join(
        here,
        '..',
        '..',
        'pages',
        'admin-restricted-accounts',
        'components',
        'notice-card.tsx'
      ),
    ]
    for (const target of targets) {
      const source = readFileSync(target, 'utf8')
      assert.match(
        source,
        /<Markdown[^>]*\suntrusted/,
        `${target} 里的公告正文必须走不可信净化档`
      )
    }
  })

  test('正常账号一个字都看不到 —— 公告不能漏给没被限制的人', async () => {
    // 缓存里**放着**一份已启用的公告，这是关键：正常账号不显示它，靠的不是
    // "数据恰好没取到"，而是这段界面根本不为他渲染。后端那一层
    // （middleware.IsRestrictedUser）另有 Go 侧用例钉住。
    const view = await mountAt('/wallet', USER_STATUS.ENABLED, NOTICE)

    assert.ok(view.text.includes('page body'), '正常账号的页面必须照常渲染')
    assert.ok(
      !view.text.includes(NOTICE.title),
      '公告漏给了正常用户 —— 全站都会以为自己被封了'
    )
    assert.ok(!view.text.includes('工作日 10:00–18:00'))
    assert.ok(!view.text.includes('qy_restricted_banner_title'))
    assert.ok(!view.text.includes('qy_restricted_landing_title'))
  })
})
