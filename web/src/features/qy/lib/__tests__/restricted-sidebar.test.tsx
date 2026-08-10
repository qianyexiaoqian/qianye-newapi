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
 * 「受限账号的侧栏真的只剩工单」——对 `useQySidebarGroups` 本体的渲染测试。
 *
 * # 为什么必须渲染，而不是只测那两个纯函数
 *
 * `qyRestrictedNavGroups` 再正确，只要 `useQySidebarGroups` 里那一行
 * `if (restricted) return …` 被谁删掉，受限账号就会拿回整棵上游导航 ——
 * 而纯函数测试、`bun run typecheck`、其余 600 条测试**全都是绿的**。
 * 变异实验证实过：删掉那一行，只测纯函数的版本一条都不红。
 *
 * 这条测试挂在 hook 的返回值上，所以接线断掉它就会红。它同时守住反方向：
 * 同一个 hook、同一份入参，正常用户必须原样拿回全部上游项。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

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

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
await i18next
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: {} } } })

const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { useQySidebarGroups } = await import('../../nav')
const { qyKeys } = await import('../query-keys')
const { QY_DISABLED_CONFIG } = await import('../config-query')
const { QY_RESTRICTED_NAV_GROUP_ID } = await import('../account-status')
const { useAuthStore } = await import('@/stores/auth-store')
const { USER_STATUS } = await import('@/features/users/constants')

type NavGroup = import('@/components/layout/types').NavGroup

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

/** 上游根导航的最小复刻，含会话链上的 relay 通道 `/playground`。 */
function baseGroups(): NavGroup[] {
  return [
    {
      id: 'chat',
      title: 'Chat',
      items: [{ title: 'Playground', url: '/playground' }],
    },
    {
      id: 'general',
      title: 'General',
      items: [
        { title: 'Overview', url: '/dashboard/overview' },
        { title: 'API Keys', url: '/keys' },
      ],
    },
    {
      id: 'personal',
      title: 'Personal',
      items: [
        { title: 'Wallet', url: '/wallet' },
        { title: 'Profile', url: '/profile' },
      ],
    },
    {
      id: 'admin',
      title: 'Admin',
      items: [{ title: 'Users', url: '/users' }],
    },
  ]
}

/** 挂载一个只做一件事的探针组件：把 hook 的返回值抄进 `captured`。 */
async function renderSidebarGroups(status: number): Promise<NavGroup[]> {
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

  let captured: NavGroup[] = []
  function Probe() {
    captured = useQySidebarGroups(baseGroups())
    return null
  }

  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(
      <QueryClientProvider client={queryClient}>
        <Probe />
      </QueryClientProvider>
    )
  })
  // react-query 的观察者在挂载之后还会再通知一轮（placeholder → 已缓存数据）。
  // 不把它冲干净的话，那一轮 setState 落在 act 之外，React 会告警，而更糟的是
  // 断言读到的是**冲刷之前**的返回值。
  await act(async () => {})
  // 当场卸载。留着不卸的话，下一个用例改 auth store 会把**上一个**探针也重渲染
  // 一遍，那次 setState 落在 act 之外 —— 表现是一串 act 告警，而根因是测试之间
  // 串了状态。
  await act(async () => root.unmount())
  container.remove()
  return captured
}

function allUrls(groups: NavGroup[]): string[] {
  const urls: string[] = []
  for (const group of groups) {
    for (const item of group.items) {
      if (typeof item.url === 'string') urls.push(item.url)
      for (const sub of item.items ?? []) {
        if (typeof sub.url === 'string') urls.push(sub.url)
      }
    }
  }
  return urls
}

describe('useQySidebarGroups × 账号状态', () => {
  test('受限账号：整棵上游导航被换成唯一的受限分组', async () => {
    const groups = await renderSidebarGroups(USER_STATUS.DISABLED)
    assert.deepEqual(
      groups.map((group) => group.id),
      [QY_RESTRICTED_NAV_GROUP_ID]
    )
    assert.deepEqual(allUrls(groups), ['/qy/tickets', '/qy/violations'])
  })

  test('受限账号：会话链上的 relay 通道与钱/密钥入口一条都不剩', async () => {
    const groups = await renderSidebarGroups(USER_STATUS.DISABLED)
    const urls = allUrls(groups)
    for (const gone of [
      '/playground',
      '/keys',
      '/wallet',
      '/profile',
      '/users',
      '/dashboard/overview',
    ]) {
      assert.ok(!urls.includes(gone), `${gone} 还留在受限侧栏里`)
    }
  })

  test('正常账号：上游项一条不少，且没有受限分组', async () => {
    const groups = await renderSidebarGroups(USER_STATUS.ENABLED)
    const urls = allUrls(groups)
    for (const kept of allUrls(baseGroups())) {
      assert.ok(urls.includes(kept), `上游项 ${kept} 被弄丢了`)
    }
    assert.ok(
      !groups.some((group) => group.id === QY_RESTRICTED_NAV_GROUP_ID),
      '受限分组漏进了正常导航'
    )
    // qy 自己的页面照旧并进来 —— 证明这条测试确实跑到了正常那条分支上。
    assert.ok(urls.includes('/qy/tickets'))
  })
})
