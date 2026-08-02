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
import {
  ArrowRightLeft,
  Banknote,
  ClipboardList,
  Gauge,
  Gavel,
  HandCoins,
  HeartPulse,
  KeyRound,
  Megaphone,
  Palette,
  Percent,
  ReceiptText,
  Repeat,
  ScrollText,
  ShieldAlert,
  SlidersHorizontal,
  Tags,
  TriangleAlert,
  UserPlus,
  UsersRound,
} from 'lucide-react'
import type { ElementType } from 'react'

import type { QyFeatures } from './types'

/**
 * qy 扩展所有页面的**单一登记表**。
 *
 * ── 为什么合并 ──
 * 同一份页面清单此前散在三个文件里：`nav.ts`（标题 + 功能开关）、
 * `lib/page-meta.ts`（日文副标）、`hooks/use-qy-nav-en-label.ts`（英文副标），
 * 本次重排还要再引入第四份（lucide 图标 —— 上游每个侧栏项都有图标，qy 项
 * 一个都没有，混进上游分组后会左对齐错位）。四份拷贝各自漂移是本项目反复
 * 出现的缺陷形状，所以这里一次性收敛成一张表，三个消费方改成从这里派生。
 *
 * 新增一个页面 = 本表加一行 + `lib/page-order.ts` 末尾追加 url，别处零改动。
 */

// ───────────────────────────── 分组 ─────────────────────────────

/**
 * 页面在侧边栏里的落点。
 *
 * `general` / `personal` / `admin` 是**上游已有的分组 id**
 * （见 `hooks/use-sidebar-data.ts`）：qy 的页面按语义并进去，而不是自己
 * 平铺成一长条 —— 「模型可用率」是只读数据面板、和上游的仪表盘同类；
 * 「支付密码」是账户安全设置、属于 Profile 一带；「分组定价」是模型侧的
 * 管理项、紧跟 Models。
 *
 * `qy-*` 是新增分组，只在语义上确实无处可挂时才建，且每组行数受控
 * （最大 6 行，不超过上游 admin 组的 7 行）。
 */
export type QyNavGroupId =
  | 'general'
  | 'personal'
  | 'admin'
  | 'qy-growth'
  | 'qy-settlement'
  | 'qy-risk'

/** 折叠项（上游 `NavCollapsible`）的标识。 */
export type QyClusterId = 'transfer' | 'withdraw' | 'transfer-settings'

/** 新增分组的定义。上游分组不在此列 —— 它们由上游自己声明。 */
export type QyNavGroupDef = {
  id: Extract<QyNavGroupId, `qy-${string}`>
  titleKey: string
  /** 插到哪个上游分组之后。同一锚点的多个新组按本表声明顺序排列。 */
  afterGroup: 'general' | 'personal' | 'admin'
}

/**
 * ⚠️ 新增分组**没有** `requiredRole` / `adminOnly` 之类的字段，这是刻意的。
 *
 * 上游 `hooks/use-sidebar-view.ts` 的角色过滤只裁项、不裁"裁空之后的组"，
 * 所以任何靠 `NavItem.requiredRole` 隐藏的管理分组，都会给普通用户留下一个
 * 只剩标题的空「结算」分组 —— 标题本身就是信息泄漏。
 *
 * 这里的规则只有一条、且只有一处实现：**成员一个都不可见的分组直接不生成**
 * （`nav.ts` 的 `mergeQyNavGroups`）。成员可见性由 `isQyPageVisible` 统一判定
 * （角色 × 功能开关），分组只是跟着走。多一个"分组也自己判一次角色"的字段
 * 就是同一概念的第二份拷贝，迟早与成员判定漂移。
 */

export const QY_NAV_GROUPS: readonly QyNavGroupDef[] = [
  {
    id: 'qy-growth',
    titleKey: 'qy_nav_group_growth',
    afterGroup: 'personal',
  },
  {
    id: 'qy-settlement',
    titleKey: 'qy_nav_group_settlement',
    afterGroup: 'admin',
  },
  {
    id: 'qy-risk',
    titleKey: 'qy_nav_group_risk',
    afterGroup: 'admin',
  },
]

/** 折叠项定义。折叠项自身的可见性由"是否还有可见子项"决定。 */
export type QyClusterDef = {
  id: QyClusterId
  titleKey: string
  icon: ElementType
  group: QyNavGroupId
  /** 同 {@link QyPageDef.after}。 */
  after?: string
}

export const QY_NAV_CLUSTERS: readonly QyClusterDef[] = [
  {
    id: 'transfer',
    titleKey: 'qy_nav_cluster_transfer',
    icon: ArrowRightLeft,
    group: 'personal',
  },
  {
    id: 'withdraw',
    titleKey: 'qy_nav_cluster_withdraw',
    icon: Banknote,
    group: 'personal',
  },
  {
    id: 'transfer-settings',
    titleKey: 'qy_nav_cluster_transfer_settings',
    icon: SlidersHorizontal,
    group: 'qy-settlement',
  },
]

// ───────────────────────────── 页面 ─────────────────────────────

export type QyPageDef = {
  url: string
  /** 侧栏与工作区索引页共用的标题 i18n key。 */
  titleKey: string
  /** 该页依赖的功能开关；`undefined` 表示只要扩展开着就显示。 */
  feature?: keyof QyFeatures
  /**
   * 侧栏落点。与 {@link QyPageDef.cluster} 二选一，恰好一个
   * （`__tests__/pages-table.test.ts` 断言）。
   */
  group?: QyNavGroupId
  /** 归入某个折叠项。此时分组由折叠项的 `group` 决定。 */
  cluster?: QyClusterId
  /**
   * 插到该 url 之后（仅对上游分组有意义）。缺省 = 追加到组尾。
   * 锚点在上游改名/消失时不会吞掉本项，合并函数会兜底追加到组尾。
   */
  after?: string
  /** lucide 图标。折叠项的子项不需要（上游二级菜单本来就不带图标）。 */
  icon?: ElementType
  /** Steins Gate 区段头的日文副标 key（见 `lib/page-meta.ts`）。 */
  jpKey: string
  /** Steins Gate 侧栏项的英文副标 key（见 `hooks/use-qy-nav-en-label.ts`）。 */
  enKey: string
}

/**
 * 页面表。**顺序 = 同一分组内的插入顺序**（折叠项的位置由它第一个子项的
 * 位置决定），与 `lib/page-order.ts` 的编号顺序无关，两者已彻底解耦。
 *
 * `/qy/admin/*` 前缀即"仅管理员可见"，不另设字段 —— 多一个字段就多一处
 * 可能与 url 对不上的地方。
 */
export const QY_PAGES: readonly QyPageDef[] = [
  // ── 上游 general：全体可见的只读数据面板，和仪表盘同类 ──
  {
    url: '/qy/availability',
    titleKey: 'qy_nav_availability',
    feature: 'availability',
    group: 'general',
    after: '/dashboard/models',
    icon: Gauge,
    jpKey: 'qy_sg_jp_availability',
    enKey: 'qy_sg_nav_en_availability',
  },

  // ── 上游 personal：账户安全 + 两个资金动作折叠 ──
  {
    // 支付密码是账户安全设置而不是资金动作，所以紧跟 Profile 而不是进折叠。
    url: '/qy/pay-password',
    titleKey: 'qy_nav_pay_password',
    feature: 'transfer',
    group: 'personal',
    after: '/profile',
    icon: KeyRound,
    jpKey: 'qy_sg_jp_pay_password',
    enKey: 'qy_sg_nav_en_pay_password',
  },
  {
    url: '/qy/transfer',
    titleKey: 'qy_nav_transfer_send',
    feature: 'transfer',
    cluster: 'transfer',
    jpKey: 'qy_sg_jp_transfer',
    enKey: 'qy_sg_nav_en_transfer',
  },
  {
    url: '/qy/transfer-logs',
    titleKey: 'qy_nav_transfer_logs',
    feature: 'transfer',
    cluster: 'transfer',
    jpKey: 'qy_sg_jp_transfer_logs',
    enKey: 'qy_sg_nav_en_transfer_logs',
  },
  {
    url: '/qy/withdraw',
    titleKey: 'qy_nav_withdraw',
    feature: 'withdraw',
    cluster: 'withdraw',
    jpKey: 'qy_sg_jp_withdraw',
    enKey: 'qy_sg_nav_en_withdraw',
  },
  {
    url: '/qy/withdrawals',
    titleKey: 'qy_nav_withdrawals',
    feature: 'withdraw',
    cluster: 'withdraw',
    jpKey: 'qy_sg_jp_withdrawals',
    enKey: 'qy_sg_nav_en_withdrawals',
  },

  // ── 新组「推广」：面向所有用户的自助页面 ──
  {
    url: '/qy/affiliate',
    titleKey: 'qy_nav_affiliate',
    feature: 'commission',
    group: 'qy-growth',
    icon: Megaphone,
    jpKey: 'qy_sg_jp_affiliate',
    enKey: 'qy_sg_nav_en_affiliate',
  },
  {
    url: '/qy/invitees',
    titleKey: 'qy_nav_invitees',
    feature: 'commission',
    group: 'qy-growth',
    icon: UserPlus,
    jpKey: 'qy_sg_jp_invitees',
    enKey: 'qy_sg_nav_en_invitees',
  },
  {
    url: '/qy/violations',
    titleKey: 'qy_nav_my_violations',
    feature: 'violation',
    group: 'qy-growth',
    icon: ShieldAlert,
    jpKey: 'qy_sg_jp_violations',
    enKey: 'qy_sg_nav_en_violations',
  },

  // ── 上游 admin：各自紧跟语义最近的上游管理项 ──
  {
    url: '/qy/admin/group-pricing',
    titleKey: 'qy_nav_a_group_pricing',
    group: 'admin',
    after: '/models/metadata',
    icon: Tags,
    jpKey: 'qy_sg_jp_a_group_pricing',
    enKey: 'qy_sg_nav_en_a_group_pricing',
  },
  {
    url: '/qy/admin/user-group',
    titleKey: 'qy_nav_a_user_group',
    group: 'admin',
    after: '/users',
    icon: UsersRound,
    jpKey: 'qy_sg_jp_a_user_group',
    enKey: 'qy_sg_nav_en_a_user_group',
  },
  {
    url: '/qy/admin/health',
    titleKey: 'qy_nav_a_health',
    group: 'admin',
    after: '/system-info',
    icon: HeartPulse,
    jpKey: 'qy_sg_jp_a_health',
    enKey: 'qy_sg_nav_en_a_health',
  },
  {
    url: '/qy/admin/site-theme',
    titleKey: 'qy_nav_a_site_theme',
    group: 'admin',
    after: '/system-settings/site',
    icon: Palette,
    jpKey: 'qy_sg_jp_a_site_theme',
    enKey: 'qy_sg_nav_en_a_site_theme',
  },

  // ── 新组「结算」：钱怎么流动，管理员视角 ──
  {
    url: '/qy/admin/commission',
    titleKey: 'qy_nav_a_commission',
    feature: 'commission',
    group: 'qy-settlement',
    icon: Percent,
    jpKey: 'qy_sg_jp_a_commission',
    enKey: 'qy_sg_nav_en_a_commission',
  },
  {
    url: '/qy/admin/commission-records',
    titleKey: 'qy_nav_a_commission_records',
    feature: 'commission',
    group: 'qy-settlement',
    icon: ReceiptText,
    jpKey: 'qy_sg_jp_a_commission_records',
    enKey: 'qy_sg_nav_en_a_commission_records',
  },
  {
    url: '/qy/admin/withdrawals',
    titleKey: 'qy_nav_a_withdrawals',
    feature: 'withdraw',
    group: 'qy-settlement',
    icon: HandCoins,
    jpKey: 'qy_sg_jp_a_withdrawals',
    enKey: 'qy_sg_nav_en_a_withdrawals',
  },
  {
    url: '/qy/admin/transfer-records',
    titleKey: 'qy_nav_a_transfer_records',
    feature: 'transfer',
    group: 'qy-settlement',
    icon: Repeat,
    jpKey: 'qy_sg_jp_a_transfer_records',
    enKey: 'qy_sg_nav_en_a_transfer_records',
  },
  {
    url: '/qy/admin/fund-orders',
    titleKey: 'qy_nav_a_fund_orders',
    group: 'qy-settlement',
    icon: ScrollText,
    jpKey: 'qy_sg_jp_a_fund_orders',
    enKey: 'qy_sg_nav_en_a_fund_orders',
  },
  {
    url: '/qy/admin/transfer-config',
    titleKey: 'qy_nav_a_transfer_config',
    feature: 'transfer',
    cluster: 'transfer-settings',
    jpKey: 'qy_sg_jp_a_transfer_config',
    enKey: 'qy_sg_nav_en_a_transfer_config',
  },
  {
    url: '/qy/admin/transfer-group-rules',
    titleKey: 'qy_nav_a_transfer_group_rules',
    feature: 'transfer',
    cluster: 'transfer-settings',
    jpKey: 'qy_sg_jp_a_transfer_group_rules',
    enKey: 'qy_sg_nav_en_a_transfer_group_rules',
  },

  // ── 新组「风控与审计」──
  {
    url: '/qy/admin/violation-rules',
    titleKey: 'qy_nav_a_violation_rules',
    feature: 'violation',
    group: 'qy-risk',
    icon: Gavel,
    jpKey: 'qy_sg_jp_a_violation_rules',
    enKey: 'qy_sg_nav_en_a_violation_rules',
  },
  {
    url: '/qy/admin/violations',
    titleKey: 'qy_nav_a_violations',
    feature: 'violation',
    group: 'qy-risk',
    icon: TriangleAlert,
    jpKey: 'qy_sg_jp_a_violations',
    enKey: 'qy_sg_nav_en_a_violations',
  },
  {
    url: '/qy/admin/audit-logs',
    titleKey: 'qy_nav_a_audit_logs',
    group: 'qy-risk',
    icon: ClipboardList,
    jpKey: 'qy_sg_jp_a_audit_logs',
    enKey: 'qy_sg_nav_en_a_audit_logs',
  },
]

/**
 * 两个工作区索引页（`/qy`、`/qy/admin`）。
 *
 * 它们不是功能页而是分组入口，所以不占 `LAB MEMO` 编号（`qyPageMeta` 给 `00`），
 * 也不出现在侧边栏里 —— 重排之后 23 个页面各自挂在语义相关的上游分组下，
 * 侧栏不再需要"先进工作区再选功能"这一跳。它们仍是有效路由，Steins Gate
 * 主题下渲染「機能一覧」总览（`home/index.tsx`）。
 */
export const QY_INDEX_PAGES: readonly { url: string; jpKey: string }[] = [
  { url: '/qy', jpKey: 'qy_sg_jp_workspace' },
  { url: '/qy/admin', jpKey: 'qy_sg_jp_admin_workspace' },
]

/** `/qy/admin/*` 前缀即管理页。 */
export function isQyAdminPage(url: string): boolean {
  return url.startsWith('/qy/admin/')
}

/**
 * 该页在当前功能开关与角色下是否应该出现在导航里。
 *
 * 只判定"能不能看见入口"；真正的访问控制在路由 guard 与后端，二者独立。
 */
export function isQyPageVisible(
  page: QyPageDef,
  features: QyFeatures,
  isAdmin: boolean
): boolean {
  if (isQyAdminPage(page.url) && !isAdmin) return false
  return page.feature == null || features[page.feature]
}
