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
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import type { TFunction } from 'i18next'

import type { NavCollapsible, NavGroup } from '@/components/layout/types'
import type { QyFeatures } from '@/features/qy/lib/types'
import { mergeQyNavGroups } from '@/features/qy/nav'
import { ROLE } from '@/lib/roles'

/**
 * 「新增的功能菜单不要都聚在一起」的回归测试。
 *
 * 三件必须钉死的事：
 *   1. **落点正确**：qy 的页面并进上游分组时，插在指定锚点之后而不是一律堆到
 *      组尾；新增分组插在指定上游分组之后。
 *   2. **空分组不泄漏**：管理专属分组对普通用户是"整组不生成"，而不是靠
 *      `requiredRole` 把项裁光 —— 上游 `use-sidebar-view.ts` 只裁项不裁空组，
 *      后者会给普通用户留下一个只剩标题的「结算」分组。
 *   3. **drill-in 不许复活**：`/qy/*` 的嵌套侧栏视图一旦回来，用户点进任何一个
 *      qy 页面侧栏就会整体切回那一坨，本次重排等于白做。
 */

const t = ((key: string) => key) as unknown as TFunction

const ALL_ON: QyFeatures = {
  transfer: true,
  commission: true,
  withdraw: true,
  availability: true,
  violation: true,
}

const ALL_OFF: QyFeatures = {
  transfer: false,
  commission: false,
  withdraw: false,
  availability: false,
  violation: false,
}

/** 上游根导航的最小复刻，只保留本测试用到的锚点。 */
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
        { title: 'Dashboard', url: '/dashboard/models' },
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
        { title: 'Channels', url: '/channels' },
        { title: 'Models', url: '/models/metadata' },
        { title: 'Users', url: '/users' },
        { title: 'System Info', url: '/system-info' },
        { title: 'System Settings', url: '/system-settings/site' },
      ],
    },
  ]
}

function urlsOf(groups: NavGroup[], id: string): (string | undefined)[] {
  const group = groups.find((g) => g.id === id)
  assert.ok(group != null, `找不到分组 ${id}`)
  return group.items.map((item) =>
    typeof item.url === 'string' ? item.url : `[collapsible:${item.title}]`
  )
}

function subUrlsOf(groups: NavGroup[], id: string, title: string): string[] {
  const group = groups.find((g) => g.id === id)
  assert.ok(group != null, `找不到分组 ${id}`)
  const item = group.items.find((i) => i.title === title) as
    | NavCollapsible
    | undefined
  assert.ok(item?.items != null, `分组 ${id} 里没有折叠项 ${title}`)
  return item.items.map((sub) => String(sub.url))
}

describe('qy nav merge — admin, all features on', () => {
  const merged = mergeQyNavGroups(baseGroups(), ALL_ON, ROLE.ADMIN, t)

  test('分组顺序：新组紧跟它锚定的上游分组', () => {
    assert.deepEqual(
      merged.map((group) => group.id),
      [
        'chat',
        'general',
        'personal',
        'qy-growth',
        'admin',
        'qy-settlement',
        'qy-risk',
      ]
    )
  })

  test('可用率插在 Dashboard 之后而不是 General 组末尾', () => {
    assert.deepEqual(urlsOf(merged, 'general'), [
      '/dashboard/overview',
      '/dashboard/models',
      '/qy/availability',
      '/keys',
    ])
  })

  test('Personal 回到上游原样：划转三页已收进钱包页的选择夹', () => {
    // 需求 2 之后，支付密码 / 发起划转 / 划转记录都是 `/wallet` 上的标签，
    // 侧栏不再为它们各留一行 —— 这正是项目方要的"菜单少几行"。
    assert.deepEqual(urlsOf(merged, 'personal'), ['/wallet', '/profile'])
  })

  test('收进选择夹的 6 个页面在整棵导航里一次都不出现', () => {
    const all = new Set(
      merged.flatMap((group) =>
        group.items.flatMap((item) => [
          typeof item.url === 'string' ? item.url : '',
          ...(item.items ?? []).map((sub) => String(sub.url)),
        ])
      )
    )
    for (const url of [
      '/qy/transfer',
      '/qy/transfer-logs',
      '/qy/pay-password',
      '/qy/invitees',
      '/qy/withdraw',
      '/qy/withdrawals',
    ]) {
      assert.ok(
        !all.has(url),
        `${url} 还留在侧栏里：点进去会被立刻重定向到宿主页，侧栏高亮跟着跳走`
      )
    }
  })

  test('Admin：四个管理页各自紧跟语义最近的上游项', () => {
    assert.deepEqual(urlsOf(merged, 'admin'), [
      '/channels',
      '/models/metadata',
      '/qy/admin/group-pricing',
      '/users',
      '/qy/admin/user-group',
      '/system-info',
      '/qy/admin/health',
      '/system-settings/site',
    ])
  })

  test('三个新分组的内容与规模', () => {
    assert.deepEqual(urlsOf(merged, 'qy-growth'), [
      '/qy/affiliate',
      '/qy/violations',
    ])
    assert.deepEqual(urlsOf(merged, 'qy-settlement'), [
      '/qy/admin/commission',
      '/qy/admin/commission-records',
      '/qy/admin/withdrawals',
      '/qy/admin/transfer-records',
      '/qy/admin/fund-orders',
      '[collapsible:qy_nav_cluster_transfer_settings]',
    ])
    assert.deepEqual(
      subUrlsOf(merged, 'qy-settlement', 'qy_nav_cluster_transfer_settings'),
      ['/qy/admin/transfer-config', '/qy/admin/transfer-group-rules']
    )
    assert.deepEqual(urlsOf(merged, 'qy-risk'), [
      '/qy/admin/violation-rules',
      '/qy/admin/violations',
      '/qy/admin/audit-logs',
    ])
    for (const group of merged) {
      assert.ok(
        group.items.length <= 9,
        `分组 ${group.id} 有 ${group.items.length} 行，又平铺成一长条了`
      )
    }
  })

  test('每个 qy 一级项都带图标（否则与上游项左对齐错位）', () => {
    for (const group of merged) {
      for (const item of group.items) {
        const url = typeof item.url === 'string' ? item.url : ''
        if (!url.startsWith('/qy')) continue
        assert.ok(item.icon != null, `${url} 没有图标`)
      }
    }
  })
})

describe('qy nav merge — 普通用户', () => {
  const merged = mergeQyNavGroups(baseGroups(), ALL_ON, ROLE.USER, t)

  test('管理专属分组整组不生成，而不是留一个空标题', () => {
    for (const id of ['qy-settlement', 'qy-risk']) {
      const group = merged.find((g) => g.id === id)
      assert.equal(
        group,
        undefined,
        `普通用户看到了 ${id} 分组（哪怕是空的，标题本身就是信息泄漏）`
      )
    }
  })

  test('隐藏管理项靠"整组不生成"，绝不靠 requiredRole', () => {
    // 上游 `use-sidebar-view.ts` 的角色过滤只裁项、不裁"裁空之后的组"。
    // 一旦有人改用 `requiredRole` 隐藏管理项，普通用户就会拿到一个
    // items 为空、标题还在的分组 —— 下面两条把这条实现路径直接堵死。
    for (const role of [ROLE.USER, ROLE.ADMIN]) {
      for (const group of mergeQyNavGroups(baseGroups(), ALL_ON, role, t)) {
        assert.ok(
          group.items.length > 0,
          `role=${role} 时分组 ${group.id} 是空的，只会渲染出一个孤零零的标题`
        )
        for (const item of group.items) {
          const url = typeof item.url === 'string' ? item.url : ''
          if (!url.startsWith('/qy') && item.items == null) continue
          assert.equal(
            item.requiredRole,
            undefined,
            `${url || item.title} 用了 requiredRole，会在上游过滤后留下空分组`
          )
        }
      }
    }
  })

  test('任何管理页 url 都不出现在结果里', () => {
    for (const group of merged) {
      for (const item of group.items) {
        const urls = [
          typeof item.url === 'string' ? item.url : '',
          ...(item.items ?? []).map((sub) => String(sub.url)),
        ]
        for (const url of urls) {
          assert.ok(
            !url.startsWith('/qy/admin/'),
            `普通用户的侧栏里出现了管理页 ${url}`
          )
        }
      }
    }
  })

  test('用户自己的页面照常并入上游分组', () => {
    assert.ok(urlsOf(merged, 'general').includes('/qy/availability'))
    // Personal 组不再有 qy 项：划转三页已经是钱包页上的标签了。
    assert.deepEqual(urlsOf(merged, 'personal'), ['/wallet', '/profile'])
    assert.deepEqual(urlsOf(merged, 'qy-growth'), [
      '/qy/affiliate',
      '/qy/violations',
    ])
  })
})

describe('qy nav merge — 边界', () => {
  test('功能全关的普通用户：上游导航逐项不变', () => {
    const base = baseGroups()
    assert.deepEqual(mergeQyNavGroups(base, ALL_OFF, ROLE.USER, t), base)
  })

  test('折叠项在子项被功能开关关光时不生成空壳', () => {
    const merged = mergeQyNavGroups(
      baseGroups(),
      { ...ALL_ON, transfer: false },
      ROLE.ADMIN,
      t
    )
    assert.ok(
      !urlsOf(merged, 'qy-settlement').includes(
        '[collapsible:qy_nav_cluster_transfer_settings]'
      ),
      '划转功能关掉后仍然渲染了一个没有子项的折叠菜单'
    )
    assert.ok(
      urlsOf(merged, 'qy-settlement').includes('/qy/admin/fund-orders'),
      '关掉划转不应该连带影响同组里与划转无关的项'
    )
  })

  test('宿主页那一行显示的是组名而不是第一张标签的名字', () => {
    const merged = mergeQyNavGroups(baseGroups(), ALL_ON, ROLE.USER, t)
    const growth = merged.find((group) => group.id === 'qy-growth')
    const hub = growth?.items.find((item) => item.url === '/qy/affiliate')
    assert.equal(
      hub?.title,
      'qy_nav_commission_hub',
      '侧栏用「推广概览」给整组命名，会让另外三张标签看起来像是藏起来的'
    )
  })

  test('新组锚定的上游分组消失时退到导航末尾，而不是整组蒸发', () => {
    const base = baseGroups().filter((group) => group.id !== 'admin')
    const merged = mergeQyNavGroups(base, ALL_ON, ROLE.ADMIN, t)
    const ids = merged.map((group) => group.id)
    assert.ok(
      ids.includes('qy-settlement') && ids.includes('qy-risk'),
      '上游分组改名就把结算/风控整组丢了 —— 热路径必须 fail-open'
    )
    assert.deepEqual(ids.slice(-2), ['qy-settlement', 'qy-risk'])
  })

  test('锚点在上游消失时 fail-open 追加到组尾，而不是把入口吞掉', () => {
    const base = baseGroups()
    const admin = base.find((group) => group.id === 'admin')
    assert.ok(admin != null)
    // 模拟上游把 Models 的 url 改成了 /models
    admin.items = admin.items.filter((item) => item.url !== '/models/metadata')

    const merged = mergeQyNavGroups(base, ALL_ON, ROLE.ADMIN, t)
    const urls = urlsOf(merged, 'admin')
    assert.ok(
      urls.includes('/qy/admin/group-pricing'),
      '锚点消失就把分组定价入口丢了 —— 热路径必须 fail-open'
    )
    assert.equal(urls.at(-1), '/qy/admin/group-pricing')
  })
})

describe('qy drill-in 侧栏视图已删除', () => {
  // __tests__ → lib → qy → features → src
  const layoutDir = join(
    dirname(fileURLToPath(import.meta.url)),
    '..',
    '..',
    '..',
    '..',
    'components',
    'layout'
  )

  test('config/qy-workspace.config.ts 不存在', () => {
    assert.ok(
      !existsSync(join(layoutDir, 'config', 'qy-workspace.config.ts')),
      'drill-in 视图回来了：用户点开任何 qy 页面，侧栏都会整体切回 qy 那一坨，重排等于白做'
    )
  })

  test('sidebar-view-registry.ts 与上游一致，不再登记 qy 视图', () => {
    const source = readFileSync(
      join(layoutDir, 'lib', 'sidebar-view-registry.ts'),
      'utf8'
    )
    assert.ok(
      !source.includes('qy'),
      'sidebar-view-registry.ts 又被改出 qy 依赖'
    )
  })
})
