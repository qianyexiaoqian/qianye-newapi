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
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import type { TFunction } from 'i18next'

import type { NavGroup } from '@/components/layout/types'
import {
  QY_RESTRICTED_NAV_GROUP_ID,
  QY_RESTRICTED_URLS,
  isQyRestrictedPathAllowed,
  isQyRestrictedUser,
  qyRestrictedNavGroups,
} from '@/features/qy/lib/account-status'
import { QY_PAGES } from '@/features/qy/lib/pages'
import type { QyFeatures } from '@/features/qy/lib/types'
import { mergeQyNavGroups } from '@/features/qy/nav'
import { USER_STATUS } from '@/features/users/constants'
import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'
import { ROLE } from '@/lib/roles'

/**
 * 「受限账号只看得到工单」的前端回归测试。
 *
 * 钉死四件事，每一件都对应一种"漏了不会有任何信号"的失败：
 *   1. **状态判据的两个方向**：字段缺失时不能把所有人判成受限（整站只剩工单），
 *      而后端将来加第三态时也不能静默放行。
 *   2. **白名单是白名单**：路径匹配到路径段为止，且导航是**构造**出来的 ——
 *      往上游 `baseGroups` 里塞任何东西都不该漏进受限导航。这条是本次最关键的
 *      设计约束，黑名单实现会让今后每个新增菜单项默认对受限账号可见。
 *   3. **正常用户零影响**：同一份 `baseGroups` 在非受限下必须原样带着全部上游项。
 *   4. **文案真的存在**：i18next 找不到键时直接把键名渲染出来，而受限页正是
 *      用户第一眼看到的东西。
 */

const t = ((key: string) => key) as unknown as TFunction

const ALL_ON: QyFeatures = {
  transfer: true,
  commission: true,
  withdraw: true,
  availability: true,
  lottery: true,
  violation: true,
  ticket: true,
  group_matrix: true,
  pay_password: true,
}

const ALL_OFF: QyFeatures = { ...ALL_ON }
for (const key of Object.keys(ALL_OFF) as (keyof QyFeatures)[]) {
  ALL_OFF[key] = false
}

/** 上游根导航的最小复刻，含一条最危险的项：会话链上的 relay 通道 `/playground`。 */
function baseGroups(): NavGroup[] {
  return [
    {
      id: 'chat',
      title: 'Chat',
      items: [
        { title: 'Playground', url: '/playground' },
        { title: 'Chat', type: 'chat-presets' },
      ],
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
      items: [
        { title: 'Users', url: '/users' },
        { title: 'System Settings', url: '/system-settings/site' },
      ],
    },
  ]
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

describe('受限判据：isQyRestrictedUser', () => {
  const cases: {
    name: string
    user: { status?: number } | null
    want: boolean
  }[] = [
    {
      name: 'enabled(1) → 正常',
      user: { status: USER_STATUS.ENABLED },
      want: false,
    },
    {
      name: 'disabled(2) → 受限',
      user: { status: USER_STATUS.DISABLED },
      want: true,
    },
    {
      name: 'deleted(-1) → 受限',
      user: { status: USER_STATUS.DELETED },
      want: true,
    },
    // 后端设计路产出里提到「将来可能引入第三态」。写成 === DISABLED 的实现
    // 会在这里静默放行，而那是一条没有任何信号的死路。
    { name: '未来的第三态(3) → 受限', user: { status: 3 }, want: true },
    // 反方向：字段没来时必须按正常账号处理。写成 !== ENABLED 的实现会在这里
    // 把所有人判成受限 —— 冷启动的一瞬间整站只剩工单。
    { name: 'status 缺失 → 正常（展示层 fail-open）', user: {}, want: false },
    { name: '未登录 → 正常', user: null, want: false },
  ]

  for (const item of cases) {
    test(item.name, () => {
      assert.equal(isQyRestrictedUser(item.user), item.want)
    })
  }
})

describe('路径白名单：isQyRestrictedPathAllowed', () => {
  const cases: { path: string; want: boolean }[] = [
    { path: '/qy/tickets', want: true },
    { path: '/qy/tickets/', want: true },
    { path: '/qy/tickets/T-2026-0001', want: true },
    { path: '/qy/violations', want: true },
    // 前缀匹配必须停在路径段上：裸 startsWith 会让这三条全部误放行，
    // 白名单当场退化成黑名单。
    { path: '/qy/tickets-admin', want: false },
    { path: '/qy/violations-export', want: false },
    { path: '/qy/admin/tickets', want: false },
    // 会话链上的 relay 通道。它长得像"普通管理台页面"，是最容易漏的一条。
    { path: '/playground', want: false },
    { path: '/keys', want: false },
    { path: '/wallet', want: false },
    { path: '/profile', want: false },
    { path: '/dashboard/overview', want: false },
    { path: '/system-settings/site', want: false },
    { path: '/qy', want: false },
    { path: '/', want: false },
  ]

  for (const item of cases) {
    test(`${item.path} → ${item.want ? '放行' : '落地页'}`, () => {
      assert.equal(isQyRestrictedPathAllowed(item.path), item.want)
    })
  }
})

describe('受限导航由白名单构造', () => {
  test('功能全开：只有白名单那两行，且分组自成一组', () => {
    const groups = qyRestrictedNavGroups(ALL_ON, t)
    assert.equal(groups.length, 1)
    assert.equal(groups[0].id, QY_RESTRICTED_NAV_GROUP_ID)
    assert.deepEqual(allUrls(groups), [...QY_RESTRICTED_URLS])
  })

  test('标题与图标取自 QY_PAGES 那张单一登记表，不是第二份拷贝', () => {
    const groups = qyRestrictedNavGroups(ALL_ON, t)
    for (const item of groups[0].items) {
      const page = QY_PAGES.find((candidate) => candidate.url === item.url)
      assert.ok(page != null, `${String(item.url)} 不在 QY_PAGES 里`)
      assert.equal(item.title, page.titleKey)
      assert.equal(item.icon, page.icon)
    }
  })

  test('ticket 功能关掉时那一行消失，violation 那行还在', () => {
    const groups = qyRestrictedNavGroups({ ...ALL_ON, ticket: false }, t)
    assert.deepEqual(allUrls(groups), ['/qy/violations'])
  })

  test('两项都被功能开关关掉 → 整组不生成（空侧栏而不是空标题）', () => {
    assert.deepEqual(qyRestrictedNavGroups(ALL_OFF, t), [])
  })

  test('白名单里的每个 url 都能在 QY_PAGES 里查到', () => {
    for (const url of QY_RESTRICTED_URLS) {
      assert.ok(
        QY_PAGES.some((page) => page.url === url),
        `${url} 不在 QY_PAGES 里，受限侧栏会少一行`
      )
    }
  })
})

describe('变异验证：往上游导航里塞东西不会漏进受限导航', () => {
  // 这一组是白名单/黑名单之争的机器判据。黑名单实现（"把正常导航过滤一遍"）
  // 在下面第二个用例上仍然会过，但只要有人给新页面配上 `/qy/tickets/...` 之外
  // 的 url 就会漏；构造式实现则与 baseGroups 完全无关。
  test('受限导航与 baseGroups 无关：加一个新管理页也不会出现', () => {
    const polluted = baseGroups()
    polluted.push({
      id: 'brand-new',
      title: 'Brand New',
      items: [{ title: 'Danger', url: '/brand-new/danger' }],
    })
    const groups = qyRestrictedNavGroups(ALL_ON, t)
    assert.ok(!allUrls(groups).includes('/brand-new/danger'))
  })

  test('哪怕上游把一项伪装成工单同名，也只按 url 白名单放行', () => {
    const polluted = baseGroups()
    polluted[0].items.push({ title: 'qy_nav_tickets', url: '/playground' })
    assert.equal(isQyRestrictedPathAllowed('/playground'), false)
  })
})

describe('正常用户完全不受影响', () => {
  test('非受限时 mergeQyNavGroups 仍然带着全部上游项', () => {
    const merged = mergeQyNavGroups(baseGroups(), ALL_ON, ROLE.ADMIN, t)
    const urls = allUrls(merged)
    for (const url of allUrls(baseGroups())) {
      assert.ok(urls.includes(url), `上游项 ${url} 被弄丢了`)
    }
  })

  test('受限分组 id 不会出现在正常用户的导航里', () => {
    const merged = mergeQyNavGroups(baseGroups(), ALL_ON, ROLE.ADMIN, t)
    assert.ok(
      !merged.some((group) => group.id === QY_RESTRICTED_NAV_GROUP_ID),
      '受限分组漏进了正常导航'
    )
  })
})

describe('接线不许被摘掉', () => {
  // 受限状态要同时压住五个界面表面：主内容区、drill-in 侧栏、⌘K 命令面板、
  // 头像下拉、站点顶栏。摘掉任何一处，`bun run typecheck` 与其余测试全都是绿的，
  // 而那一处的入口原样露出来 —— 正是本仓反复出现的"漏一处就是一条静默死路"。
  //
  // 根侧栏那一处**不在这张表里**：它由 `restricted-sidebar.test.tsx` 直接渲染
  // `useQySidebarGroups` 断言返回值，比扫源码强得多。这里的 needle 一律取
  // **调用形状**而不是标识符 —— 只留一个 import 不算接线，而只扫标识符的版本
  // 会把那种情况判成通过（变异实验证实过）。
  const srcDir = join(
    dirname(fileURLToPath(import.meta.url)),
    '..',
    '..',
    '..',
    '..'
  )
  const wiring: { file: string[]; needle: string }[] = [
    {
      file: ['components', 'layout', 'components', 'authenticated-layout.tsx'],
      needle: '<QyRestrictedGate>',
    },
    {
      file: ['hooks', 'use-sidebar-view.ts'],
      needle: 'restricted ? null : resolveSidebarView(',
    },
    {
      file: ['components', 'command-menu.tsx'],
      needle: 'restricted ? null : getNavGroupsForPath(',
    },
    {
      file: ['components', 'profile-dropdown.tsx'],
      needle: '{!restricted && (',
    },
    {
      file: ['hooks', 'use-top-nav-links.ts'],
      needle: 'restricted ? undefined : modules?.pricing',
    },
  ]

  for (const item of wiring) {
    test(`${item.file.join('/')} 仍然接着 ${item.needle}`, () => {
      const source = readFileSync(join(srcDir, ...item.file), 'utf8')
      assert.ok(source.includes(item.needle))
    })
  }
})

describe('受限文案在两份语言包里都存在', () => {
  const enKeys = en as Record<string, string>
  const zhKeys = zh as Record<string, string>
  const keys = [
    'qy_restricted_nav_group',
    'qy_restricted_banner_title',
    'qy_restricted_blocked',
    'qy_restricted_kept',
    'qy_restricted_appeal_hint',
    'qy_restricted_no_channel',
    'qy_restricted_landing_title',
    'qy_restricted_landing_desc',
    'qy_restricted_go_tickets',
    'qy_restricted_go_violations',
  ]

  for (const key of keys) {
    test(key, () => {
      assert.ok(enKeys[key], `en 缺 ${key}`)
      assert.ok(zhKeys[key], `zh 缺 ${key}`)
    })
  }
})
