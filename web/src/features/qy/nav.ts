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
import type { TFunction } from 'i18next'
import { Megaphone, ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import type { NavGroup, NavItem } from '@/components/layout/types'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { useQyConfig } from './hooks/use-qy-config'
import { getQyConfigSnapshot } from './lib/config-query'
import type { QyFeatures } from './lib/types'

/**
 * qy 扩展的侧边栏定义。
 *
 * 两层结构：
 *   1. **根侧边栏**（{@link useQySidebarGroups}）只占一个分组、最多两项，
 *      避免把 13 个子页面全糊到主导航上；
 *   2. **工作区侧边栏**（{@link getQyWorkspaceNavGroups}）在用户进入 `/qy/*`
 *      后替换根导航，13 个子页面全挂这里。
 *
 * 新增页面 = 往下面的 `WORKSPACE_PAGES` / `ADMIN_PAGES` 加一项，别处零改动。
 */

type QyPageEntry = {
  titleKey: string
  url: string
  /** 该页依赖的功能开关；`undefined` 表示只要扩展开着就显示。 */
  feature?: keyof QyFeatures
}

/** 用户端页面。顺序即侧边栏顺序。 */
const WORKSPACE_PAGES: QyPageEntry[] = [
  { titleKey: 'qy_nav_affiliate', url: '/qy/affiliate', feature: 'commission' },
  { titleKey: 'qy_nav_invitees', url: '/qy/invitees', feature: 'commission' },
  { titleKey: 'qy_nav_transfer', url: '/qy/transfer', feature: 'transfer' },
  {
    titleKey: 'qy_nav_transfer_logs',
    url: '/qy/transfer-logs',
    feature: 'transfer',
  },
  { titleKey: 'qy_nav_withdraw', url: '/qy/withdraw', feature: 'withdraw' },
  {
    titleKey: 'qy_nav_withdrawals',
    url: '/qy/withdrawals',
    feature: 'withdraw',
  },
  {
    titleKey: 'qy_nav_my_violations',
    url: '/qy/violations',
    feature: 'violation',
  },
  {
    titleKey: 'qy_nav_availability',
    url: '/qy/availability',
    feature: 'availability',
  },
]

/** 管理端页面。仅 role >= ADMIN 可见，见下方 §角色过滤。 */
const ADMIN_PAGES: QyPageEntry[] = [
  {
    titleKey: 'qy_nav_a_commission',
    url: '/qy/admin/commission',
    feature: 'commission',
  },
  {
    titleKey: 'qy_nav_a_commission_records',
    url: '/qy/admin/commission-records',
    feature: 'commission',
  },
  {
    titleKey: 'qy_nav_a_transfer_records',
    url: '/qy/admin/transfer-records',
    feature: 'transfer',
  },
  {
    titleKey: 'qy_nav_a_transfer_group_rules',
    url: '/qy/admin/transfer-group-rules',
    feature: 'transfer',
  },
  {
    titleKey: 'qy_nav_a_withdrawals',
    url: '/qy/admin/withdrawals',
    feature: 'withdraw',
  },
  {
    titleKey: 'qy_nav_a_violation_rules',
    url: '/qy/admin/violation-rules',
    feature: 'violation',
  },
  {
    titleKey: 'qy_nav_a_violations',
    url: '/qy/admin/violations',
    feature: 'violation',
  },
  {
    titleKey: 'qy_nav_a_availability',
    url: '/qy/admin/availability',
    feature: 'availability',
  },
  // 无 feature 键：新用户默认分组没有独立开关，扩展开着就能配。
  { titleKey: 'qy_nav_a_user_group', url: '/qy/admin/user-group' },
  // 分组定价同样暂无独立开关（`QyFeatures` 里没有对应字段）。后端若为它加了
  // `features.group_pricing`，这里补一个 `feature:` 即可；在那之前入口常驻，
  // 功能关闭时页面会走 guard 的 404 → `QyPageBoundary` 的中性空态，不弹红。
  { titleKey: 'qy_nav_a_group_pricing', url: '/qy/admin/group-pricing' },
  // 站点默认主题同样没有独立开关：它只改展示层，扩展开着就能配。
  { titleKey: 'qy_nav_a_site_theme', url: '/qy/admin/site-theme' },
  { titleKey: 'qy_nav_a_fund_orders', url: '/qy/admin/fund-orders' },
  { titleKey: 'qy_nav_a_audit_logs', url: '/qy/admin/audit-logs' },
  { titleKey: 'qy_nav_a_health', url: '/qy/admin/health' },
]

/**
 * 页面的**静态声明顺序**，Steins Gate 主题的区段头序号（`LAB MEMO — 07`）由它派生。
 *
 * 刻意用声明顺序而不是"当前侧边栏里可见的顺序"：后者随功能开关增删，同一个页面
 * 昨天是 05 今天变 03，序号就失去了"实验记录编号"的含义。
 */
export const QY_PAGE_URL_ORDER: readonly string[] = [
  ...WORKSPACE_PAGES,
  ...ADMIN_PAGES,
].map((page) => page.url)

function toNavItems(
  pages: QyPageEntry[],
  features: QyFeatures,
  t: TFunction
): NavItem[] {
  return pages
    .filter((page) => page.feature == null || features[page.feature])
    .map((page) => ({ title: t(page.titleKey), url: page.url }))
}

/**
 * 根侧边栏分组。挂进 `use-sidebar-data.ts` 的 `navGroups` 数组尾部。
 *
 * **必须无条件调用**（不能包在 if 里）：它内部的 `useQyConfig()` 是一个
 * `useQuery`，正是这个订阅让 `/api/qy/config` 返回后侧边栏能重新渲染。
 * 改成条件调用会同时违反 hooks 规则并打断响应式链路。
 */
export function useQySidebarGroups(): NavGroup[] {
  const { t } = useTranslation()
  const config = useQyConfig()

  // 扩展未启用 → 返回空数组，菜单、⌘K 命令面板、移动端抽屉同时零痕迹。
  if (!config.enabled) return []

  const items: NavItem[] = [
    {
      title: t('qy_nav_workspace'),
      url: '/qy',
      activeUrls: ['/qy'],
      icon: Megaphone,
    },
    {
      title: t('qy_nav_admin_workspace'),
      url: '/qy/admin',
      activeUrls: ['/qy/admin'],
      icon: ShieldCheck,
      // 复用上游 use-sidebar-view.ts 的单项角色过滤，不自造轮子。
      requiredRole: ROLE.ADMIN,
    },
  ]

  // 分组 id 刻意不叫 'admin'：上游会把 id==='admin' 的整组对普通用户隐藏，
  // 而本分组里有面向所有用户的入口。
  return [{ id: 'qy', title: t('qy_nav_group'), items }]
}

/**
 * 工作区（drill-in）侧边栏分组。
 *
 * ⚠️ 高危：上游 `use-sidebar-view.ts` 的嵌套视图分支**不做任何角色过滤**，
 * 直接把 `getNavGroups(t)` 的结果原样返回。因此管理项必须在这里自行按 role
 * 裁剪，否则普通用户进入 `/qy/*` 会在侧栏看到"提现审核""违规记录"等全部
 * 管理入口的名称 —— 那是信息泄漏。
 *
 * 这里用 `getState()` / 模块快照而不是 hook 是因为本函数不是组件；响应式由
 * `use-sidebar-view.ts` 无条件订阅的 `useAuthStore(role)` 与
 * `useSidebarData()`（内部含 `useQyConfig`）提供。
 */
export function getQyWorkspaceNavGroups(t: TFunction): NavGroup[] {
  const role = useAuthStore.getState().auth.user?.role ?? ROLE.GUEST
  const { features } = getQyConfigSnapshot()
  const groups: NavGroup[] = []

  const personal = toNavItems(WORKSPACE_PAGES, features, t)
  if (personal.length > 0) {
    groups.push({
      id: 'qy-personal',
      title: t('qy_nav_group_personal'),
      items: personal,
    })
  }

  if (role >= ROLE.ADMIN) {
    const admin = toNavItems(ADMIN_PAGES, features, t)
    if (admin.length > 0) {
      groups.push({
        id: 'qy-admin',
        title: t('qy_nav_group_admin'),
        items: admin,
      })
    }
  }

  return groups
}
