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
import { Blocks } from 'lucide-react'

import type {
  NavCollapsible,
  NavGroup,
  NavItem,
} from '@/components/layout/types'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { getQyConfigSnapshot } from './lib/config-query'
import {
  QY_SETTINGS_PAGES,
  QY_SETTINGS_SECTION_PATHS,
  type QyPageDef,
  isQyPageVisible,
} from './lib/pages'
import type { QyFeatures } from './lib/types'

/**
 * 把 qy 的配置类管理页并进上游「系统设置」抽屉（需求 8）。
 *
 * 项目方原话：「对于管理员的一些菜单，你可以加入到系统设置里面的菜单去。」
 *
 * ── 上一轮勘察提出的约束现在仍然成立，这里正面解掉 ──
 * 上游的系统设置是一个 drill-in 视图：`useSidebarView` 拿当前 pathname 去匹配
 * `SidebarView.pathPattern`，匹配上就整块换掉侧栏。qy 的页面 url 是
 * `/qy/admin/*`，原 pattern（`^/system-settings(/|$)`）认不出来，于是"从设置
 * 抽屉里点一个 qy 页面 → 侧栏立刻掉回根导航"，等于点一下就被踢出去。
 *
 * 解法是把 pattern 扩宽到**恰好那几页**（{@link QY_SYSTEM_SETTINGS_PATH_PATTERN}），
 * 而不是整个 `/qy/admin/`：审核 / 流水 / 审计那些页仍在根侧栏上，从根侧栏点
 * 进去时侧栏不该换成设置抽屉。两条清单同源（`QY_SETTINGS_PAGES`），
 * 所以"菜单里有、pattern 里没有"这种半接上的状态构造不出来。
 *
 * ── 为什么默认是一个独立的折叠项，而不是散进上游那 7 组 ──
 * 散进去意味着上游那些组里混进**另一个路由**：上游成员都是同一页面内的
 * `?section=` / 路径锚点，点了停在原页，qy 的点了整页跳走，同一组里两种行为。
 * 单独一组还能让"这是扩展带来的"这件事一眼可见。
 *
 * ── 那为什么仍然留了例外 ──
 * 项目方 2026-09-05：「API 地址这个页面菜单，从扩展设置移动到模型与路由下面。」
 * 归属比行为一致更重要时，页面在表里写一行 `settingsSection`
 * （{@link QY_SETTINGS_SECTION_PATHS}），本函数就把它挂到那个折叠项末尾。
 * 认的是上游那一组子项的 url 前缀，不是翻译后的标题 —— 标题随语言变，url 不变。
 * 认不出来（上游改了路径 / 删了那一组）时**落回「扩展设置」**：入口错位是小事，
 * 入口消失是本仓反复出现的那件大事。
 */

/**
 * 纯函数版：往上游抽屉的第一组末尾追加一个「扩展设置」折叠项，
 * 并把写了 `settingsSection` 的页面挂进上游对应的折叠项（如 API 地址 → 模型与路由）。
 *
 * 追加而不是插到某个位置：上游那 7 组的顺序是它自己的编排，qy 挤进中间
 * 只会在上游调整顺序时错位。一个都不可见时**整组不生成** —— 留一个只剩标题
 * 的折叠项，标题本身就是信息泄漏（与 `nav.ts` 里对空分组的处理同一条规则）。
 * 全部页面都挂进了上游折叠项时同理不生成这一项。
 *
 * 成员可见性与根侧栏共用 `isQyPageVisible`（角色 × 功能开关）；这里额外要求
 * 管理员，因为整组页面都是 `/qy/admin/*`。
 */
export function mergeQySystemSettingsNavGroups(
  baseGroups: NavGroup[],
  features: QyFeatures,
  role: number,
  t: TFunction
): NavGroup[] {
  // 判据必须是 SUPER_ADMIN，不是 ADMIN：抽屉本体的路由
  // （`routes/_authenticated/system-settings/route.tsx`）要求 role === 100，
  // 否则 redirect('/403')。这两处曾经不一致，后果是 role=10 的管理员在这里
  // 生成了 8 个菜单项，却永远打不开承载它们的那扇门 —— 那 8 个页面在界面上
  // 等于不存在（本仓第五次"写了但找不到"）。role<100 的那一档由
  // `nav.ts` 的 withQySettingsFallback 在根侧栏上补一个同名折叠项接住。
  // 正向判据:`role` 可能是 undefined(未登录 / 字段缺失),而 `undefined < 100`
  // 是 false —— 反向写法会让这一档穿过去。取反之后同样的输入得到"不生成"。
  if (!(role >= ROLE.SUPER_ADMIN)) return baseGroups
  const isAdmin = true

  const visible = QY_SETTINGS_PAGES.filter((page) =>
    isQyPageVisible(page, features, isAdmin)
  )
  if (visible.length === 0) return baseGroups

  const target = baseGroups[0]
  if (target == null) return baseGroups

  // 子项不带图标：上游那 7 组的子项也都不带，混着给会让缩进看起来是坏的。
  const navItem = (page: QyPageDef) => ({
    title: t(page.titleKey),
    url: page.url,
  })

  // 先把写了 `settingsSection` 的那几页挂到上游对应的折叠项末尾。
  const attached = new Set<string>()
  const items = target.items.map((item): NavItem => {
    if (item.items == null) return item
    const children = item.items
    const mine = visible.filter((page) => {
      if (page.settingsSection == null) return false
      const basePath = QY_SETTINGS_SECTION_PATHS[page.settingsSection]
      return children.some((child) => String(child.url).startsWith(basePath))
    })
    if (mine.length === 0) return item
    for (const page of mine) attached.add(page.url)
    return { ...item, items: [...children, ...mine.map(navItem)] }
  })

  // 剩下的（以及没能挂上去的那些 —— fail-open）仍旧收在「扩展设置」里。
  const own = visible.filter((page) => !attached.has(page.url))
  if (own.length === 0) return [{ ...target, items }, ...baseGroups.slice(1)]

  const qyItem: NavCollapsible = {
    title: t('qy_nav_group_settings'),
    icon: Blocks,
    items: own.map(navItem),
  }
  return [{ ...target, items: [...items, qyItem] }, ...baseGroups.slice(1)]
}

/**
 * `system-settings.config.ts` 的调用入口：读当前配置与角色，再走上面的纯函数。
 *
 * ── 为什么读快照而不是用 hook ──
 * 上游 `SidebarView.getNavGroups` 的签名是 `(t) => NavGroup[]`，纯函数，
 * 没有 hook 位置。响应式由调用方保证：`useSidebarView` 里 `useSidebarData()`
 * （内部订阅 `useQyConfig()` 这个 useQuery）与 `useAuthStore` 都是无条件调用的，
 * 配置或角色一变整个 hook 重跑，这里的快照跟着是新的。
 * `getQyWorkspaceNavGroups` 用的是同一套理由与同一对数据源。
 */
export function withQySystemSettingsNavGroups(
  baseGroups: NavGroup[],
  t: TFunction
): NavGroup[] {
  const config = getQyConfigSnapshot()
  if (!config.enabled) return baseGroups
  const role = useAuthStore.getState().auth.user?.role ?? ROLE.GUEST
  return mergeQySystemSettingsNavGroups(baseGroups, config.features, role, t)
}

/**
 * 上游「计费与支付」那一组里，由扩展提供的 section 的 url。
 *
 * 与 `features/system-settings/billing/section-registry.tsx` 里那一项的 `id`
 * 对应。写在 qy 这一侧而不是上游那一侧：上游文件只登记「有这么一个 section」，
 * 「什么时候该看得见」是扩展自己的事。
 *
 * 拆页之后这个集合是**空的**，而且必须是空的。
 *
 * ── 为什么连矩阵那一页也不能摘 ──
 *
 * 「用户分组」与「模型分组」两页读写的全是上游 `options`（充值折扣 / 兜底倍率 /
 * auto 顺序），扩展关掉时它们当然要照常可见。矩阵那一页此前被列在这里，但拆页
 * 之后它同时是全局「用户可选分组」清单（上游 option `UserUsableGroups`）的
 * **唯一编辑器** —— 摘掉入口就等于扩展一关，一个纯上游配置项从界面上彻底消失，
 * 而 C 页那一列是只读、JSON 抽屉里那个字段也是只读。运营想让用户在令牌里选到
 * 一个新建的模型分组，就只能靠猜出深链接。
 *
 * 页面本体在扩展关掉时已经有降级契约（后端 guard 回 404 → 中性空态），入口留着
 * 才能明确告诉运营「矩阵没开，但可选清单在这儿改」，而不是变成一个静默消失的菜单。
 */
const QY_BILLING_SECTION_URLS = new Set<string>([])

/**
 * 扩展关掉时，把扩展贡献的计费 section 从抽屉里摘掉。
 *
 * ── 为什么不在上游的 section 表里做条件登记 ──
 * 那张表同时供给三件事：抽屉菜单、`$section` 路由白名单、以及 section 内容。
 * 条件登记会让白名单跟着消失，于是一条已经发出去的深链接（或浏览器历史里的
 * 那一条）在扩展关掉之后不是显示「功能未启用」，而是被静默重定向回「额度设置」
 * —— 管理员会以为自己记错了地址。所以 section 恒在，只有**入口**跟着开关走。
 *
 * 判据只用「扩展是否启用」，**不看 `features.group_matrix`**：那个 YAML 开关
 * 关掉时后端 guard 回 404、页面按既有降级契约显示中性空态，入口留着才能明确
 * 告诉运营「没开」，而不是变成一个静默消失的菜单。这与 `lib/pages.ts` 里那几页
 * 不挂该开关是同一条决定。
 */
export function withQyBillingSectionNavItems<T extends { url: string }>(
  items: T[]
): T[] {
  if (getQyConfigSnapshot().enabled) return items
  return items.filter((item) => !QY_BILLING_SECTION_URLS.has(item.url))
}

/**
 * 系统设置 drill-in 视图的路径匹配。
 *
 * 上游原本是 `/^\/system-settings(\/|$)/`。这里把 qy 那几个配置页并进同一个
 * pattern —— 它们现在是这个抽屉的成员，进去之后侧栏当然要保持是抽屉。
 *
 * `(\/|$)` 是必须的：没有它，`/qy/admin/stardust-config` 这类前缀会顺带匹配
 * 任何以它开头的新路由，而流水页是留在根侧栏上的，从根侧栏点进去侧栏却换成
 * 设置抽屉，等于把人甩出当前上下文。
 */
export const QY_SYSTEM_SETTINGS_PATH_PATTERN = new RegExp(
  `^\\/(system-settings|qy\\/admin\\/(${QY_SETTINGS_PAGES.map((page) =>
    page.url.replace('/qy/admin/', '')
  ).join('|')}))(\\/|$)`
)
