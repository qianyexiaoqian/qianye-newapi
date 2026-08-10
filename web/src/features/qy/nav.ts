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
import { useTranslation } from 'react-i18next'

import type { NavGroup, NavItem, NavLink } from '@/components/layout/types'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { useQyConfig } from './hooks/use-qy-config'
import { qyRestrictedNavGroups, useQyIsRestricted } from './lib/account-status'
import { getQyConfigSnapshot } from './lib/config-query'
import { QY_PAGE_URL_ORDER } from './lib/page-order'
import {
  QY_NAV_GROUPS,
  QY_SETTINGS_GROUP,
  QY_TAB_GROUPS,
  isQyAdminPage,
  qyEntryPages,
  type QyEntrySwitches,
  type QyNavGroupId,
  type QyPageDef,
} from './lib/pages'
import type { QyConfig, QyFeatures } from './lib/types'

/**
 * qy 扩展的侧边栏定义。
 *
 * ── 本轮改动：不再"聚成一坨" ──
 * 此前 23 个页面全塞进一个 drill-in 工作区，侧栏上是一长条平铺列表；项目方的
 * 反馈正是"新增的功能菜单都聚在一起"。现在按语义拆开：
 *
 *   · 能挂进上游分组的就挂进去（可用率 → General、扩展健康 → Admin，
 *     各自紧跟语义最近的上游项）；
 *   · 配置类的管理页干脆不进根侧栏，并进上游「系统设置」抽屉
 *     （需求 8，见 `system-settings.ts`）；
 *   · 确实无处可挂的才建新组，且新组行数受控（均不超过上游 admin 组的 7 行）；
 *   · drill-in 工作区视图**已删除** —— 见下方 §为什么删掉 drill-in。
 *
 * ── 需求 2 / 3 之后：一部分页面根本不在这里出现 ──
 * 划转三页与推广三页已经收进「选择夹」（`lib/pages.ts` 的 `QY_TAB_GROUPS`），
 * 它们是宿主页上的标签而不是独立页面，因此 `qyEntryPages` 会把它们滤掉。
 * 侧栏因此比上一轮少了两个折叠项与两行 —— 这正是项目方要的。
 *
 * 落点、图标、日文副标全部来自 `lib/pages.ts` 的单张表，本文件只负责
 * "把表变成上游要的 NavGroup 结构"。
 *
 * ── 为什么删掉 drill-in ──
 * 原来的 `QY_WORKSPACE_VIEW` 用 `pathPattern: /^\/qy(\/|$)/` 匹配所有 `/qy/*`。
 * 页面拆开之后，用户在 Personal 里点「申请提现」落到 `/qy/withdraw`，侧栏会
 * 立刻整体切回 qy 那一坨 —— 重排的效果被 drill-in 原样吞掉，项目方的抱怨
 * 一字不差地回来。把 pattern 收窄到 `/^\/qy(\/admin)?$/` 也不行：那样只有两个
 * 索引页会换侧栏，而它们已经不在导航里，等于为一个无人经过的路径保留一套
 * 分支逻辑。所以直接删掉 `config/qy-workspace.config.ts` 并把
 * `lib/sidebar-view-registry.ts` 回滚到上游原样（上游被改文件数 −1）。
 */

// ───────────────────────── 纯函数：合并 ─────────────────────────

type QyInsertion = { item: NavItem; after?: string }

/**
 * 页面 → 侧栏项。
 *
 * 页面若是某个选项卡组的宿主（`/qy/affiliate`），侧栏那一行显示的是**组名**
 * 「推广佣金」而不是第一张标签的名字「推广概览」：那一行点进去得到的是四张
 * 标签，用第一张的名字给整组命名会让另外三张看起来像是藏起来的。
 */
function toNavLink(page: QyPageDef, t: TFunction): NavLink {
  const hosted = QY_TAB_GROUPS.find((group) => group.host === page.url)
  return {
    title: t(hosted?.titleKey ?? page.titleKey),
    url: page.url,
    icon: page.icon,
  }
}

/**
 * 配置 → 展示开关。
 *
 * 单独一个函数而不是在两个调用点各写一遍对象字面量：漏掉一处的表现是
 * "侧栏藏了、`/qy` 索引页还留着一行"，正是本仓反复出现的断链形状。
 */
function qyEntrySwitches(config: QyConfig): QyEntrySwitches {
  return { lottery: config.lottery.show_entry }
}

/**
 * 按分组收集要插入的导航项。
 *
 * 同一分组内的顺序 = 页面在 `QY_PAGES` 里的声明顺序；组内位置由 `after`
 * 锚点决定，找不到锚点就追加到组尾（见 {@link withQyItems}）。
 *
 * ── 折叠项（`NavCollapsible`）已经不再产出 ──
 * 上一轮的「划转设置」折叠项只有两个子项，需求 8 之后这两页都并进了系统设置
 * 抽屉，折叠项随之空掉。空壳没有留：留着就是一条永远走不到的分支，
 * 而"写了但没接上"是这个仓库反复出现的形状。真需要二级菜单时再加回来，
 * 那时它至少有成员可测。
 */
function collectInsertions(
  features: QyFeatures,
  isAdmin: boolean,
  t: TFunction,
  entries?: QyEntrySwitches
): Map<string, QyInsertion[]> {
  const byGroup = new Map<string, QyInsertion[]>()
  const push = (group: QyNavGroupId, insertion: QyInsertion) => {
    const list = byGroup.get(group)
    if (list) list.push(insertion)
    else byGroup.set(group, [insertion])
  }

  // `qyEntryPages` 已经把"角色 × 功能开关 × 是否已被收进别人的选择夹"三件事
  // 一起判完。这里不再自己 filter 一遍：判定多一处就多一处会漂移的地方，而
  // 被收进选择夹的页面留在侧栏里会得到一行点了就被重定向甩走的死链。
  for (const page of qyEntryPages(features, isAdmin, entries)) {
    if (page.group == null) continue
    // 系统设置抽屉里的页面**不进根侧栏**（需求 8）。必须显式跳过：靠
    // "byGroup 里多出一个没人认领的键、后面自然被丢掉"来实现，是这个仓库
    // 反复出现的断链形状 —— 哪天上游真出现同名分组，它们就会凭空冒出来。
    if (page.group === QY_SETTINGS_GROUP) continue
    push(page.group, { item: toNavLink(page, t), after: page.after })
  }

  return byGroup
}

/**
 * 把插入项按锚点放进一个分组。
 *
 * 锚点在上游改名或被 `sidebar_modules` 关掉时**不能把 qy 项一起吞掉**：
 * 找不到锚点就追加到组尾（fail-open），最坏情况只是位置不理想，而不是入口
 * 凭空消失。
 */
function withQyItems(group: NavGroup, insertions: QyInsertion[]): NavGroup {
  const placed = new Set<QyInsertion>()
  const items: NavItem[] = []

  for (const item of group.items) {
    items.push(item)
    const url = typeof item.url === 'string' ? item.url : null
    if (url == null) continue
    for (const insertion of insertions) {
      if (insertion.after !== url || placed.has(insertion)) continue
      items.push(insertion.item)
      placed.add(insertion)
    }
  }

  for (const insertion of insertions) {
    if (placed.has(insertion)) continue
    items.push(insertion.item)
  }

  return { ...group, items }
}

/**
 * 把 qy 的页面并入上游根导航。
 *
 * ── `sidebar_modules` 的边界（产品决定，别再改回来）──
 * 上游 `use-sidebar-config.ts` 的 `URL_TO_CONFIG_MAP` 没有 `/qy/*` 的条目，
 * 所以 qy 项对该过滤器恒为可见。结论是**刻意**的：`sidebar_modules` 是上游
 * 模块的显隐开关，qy 的开关是 YAML `features` × 角色。把 qy 项挂到上游开关下
 * 会出现"管理员关掉钱包展示，顺手把提现申请也关了"这种越权联动。代价是
 * 管理员关掉整段 personal 时，Personal 组里上游项消失而 qy 项还在 ——
 * 接受，那一组的标题仍然成立。
 */
export function mergeQyNavGroups(
  baseGroups: NavGroup[],
  features: QyFeatures,
  role: number,
  t: TFunction,
  entries?: QyEntrySwitches
): NavGroup[] {
  const isAdmin = role >= ROLE.ADMIN
  const insertions = collectInsertions(features, isAdmin, t, entries)

  // 成员一个都不可见（角色不够 / 功能关掉）→ 整组不生成。
  // 绝不能改成给项挂 `requiredRole`：上游 `use-sidebar-view.ts` 只裁项、
  // 不裁"裁空之后的组"，那样普通用户会看到一个只剩标题的「结算」分组。
  const newGroups = new Map<string, NavGroup>()
  for (const def of QY_NAV_GROUPS) {
    const items = insertions.get(def.id)
    if (items == null || items.length === 0) continue
    newGroups.set(def.id, {
      id: def.id,
      title: t(def.titleKey),
      items: items.map((insertion) => insertion.item),
    })
  }

  const merged: NavGroup[] = []
  for (const group of baseGroups) {
    const own = group.id == null ? undefined : insertions.get(group.id)
    merged.push(own == null ? group : withQyItems(group, own))

    for (const def of QY_NAV_GROUPS) {
      if (def.afterGroup !== group.id) continue
      const built = newGroups.get(def.id)
      if (built == null) continue
      merged.push(built)
      newGroups.delete(def.id)
    }
  }

  // 上游把锚定的分组改名/删掉时，新组不能跟着一起消失（热路径 fail-open）：
  // 位置退化到导航末尾，入口仍然在。
  for (const group of newGroups.values()) merged.push(group)

  return merged
}

// ───────────────────────────── Hook ─────────────────────────────

/**
 * 给上游根导航套一层：返回"上游分组 + 已并入的 qy 页面"。
 *
 * **必须无条件调用**（不能包在 if 里）：内部的 `useQyConfig()` 是一个
 * `useQuery`，正是这个订阅让 `/api/qy/config` 返回后侧边栏能重新渲染。
 * 扩展未启用时原样返回入参，菜单、⌘K 命令面板、移动端抽屉同时零痕迹。
 *
 * ── 受限账号（status ≠ enabled）走另一条分支 ──
 * 它**丢掉整棵 `baseGroups`**，改用 {@link qyRestrictedNavGroups} 由白名单
 * 现场构造。这是本函数唯一一处"不把入参往下传"的地方，理由见
 * `lib/account-status.ts`：过滤式实现会让今后每一个新增的上游侧栏项默认对
 * 受限账号可见，而没有任何东西会红。构造式则相反。
 *
 * 落在这个函数里而不是上游 `use-sidebar-data.ts`，是因为它已经是根导航的
 * 唯一出口：侧栏、⌘K 命令面板都从这里拿分组，一处改完两处生效，上游被改
 * 文件数不增加。
 */
export function useQySidebarGroups(baseGroups: NavGroup[]): NavGroup[] {
  const { t } = useTranslation()
  const config = useQyConfig()
  const role = useAuthStore((state) => state.auth.user?.role) ?? ROLE.GUEST
  const restricted = useQyIsRestricted()

  // 先判受限、再判角色 —— 与后端 `authHelper` 的"先判 status 再判 role"同序。
  // 反过来的话，一个被自动封禁模块封掉的管理员会带着完整管理菜单继续操作。
  if (restricted) return qyRestrictedNavGroups(config.features, t)
  if (!config.enabled) return baseGroups
  return mergeQyNavGroups(
    baseGroups,
    config.features,
    role,
    t,
    qyEntrySwitches(config)
  )
}

// ───────────────────── 工作区索引页的两个清单 ─────────────────────

/**
 * `/qy` 与 `/qy/admin` 索引页（`home/index.tsx`）用的扁平清单。
 *
 * 与侧栏是**同一张表的另一种投影**，不是第二份拷贝：侧栏按功能语义打散，
 * 索引页按"我的 / 管理"两栏平铺并按 `LAB MEMO` 编号升序排列 —— 那正是
 * 索引页该干的事（一屏看全），也是移动端侧栏收起时唯一的总览入口。
 *
 * 分组 id 保持 `qy-personal` / `qy-admin`：`home/index.tsx` 按 id 查表。
 *
 * 这里用 `getState()` / 模块快照而不是 hook 是因为本函数不是组件；响应式由
 * 调用方显式订阅的 `useQyConfig()` 与 `useAuthStore` 提供。
 */
export function getQyWorkspaceNavGroups(t: TFunction): NavGroup[] {
  const role = useAuthStore.getState().auth.user?.role ?? ROLE.GUEST
  const isAdmin = role >= ROLE.ADMIN
  const config = getQyConfigSnapshot()

  const visible = qyEntryPages(
    config.features,
    isAdmin,
    qyEntrySwitches(config)
  ).sort(
    (a, b) =>
      QY_PAGE_URL_ORDER.indexOf(a.url) - QY_PAGE_URL_ORDER.indexOf(b.url)
  )

  const groups: NavGroup[] = []
  const personal = visible.filter((page) => !isQyAdminPage(page.url))
  if (personal.length > 0) {
    groups.push({
      id: 'qy-personal',
      title: t('qy_nav_group_personal'),
      items: personal.map((page) => toNavLink(page, t)),
    })
  }

  const admin = visible.filter((page) => isQyAdminPage(page.url))
  if (admin.length > 0) {
    groups.push({
      id: 'qy-admin',
      title: t('qy_nav_group_admin'),
      items: admin.map((page) => toNavLink(page, t)),
    })
  }

  return groups
}

/**
 * @deprecated 直接从 `lib/page-order.ts` 导入。这里的再导出只为了不动
 * 既有测试的 import 路径；序号本身已经与导航结构解耦。
 */
export { QY_PAGE_URL_ORDER }
