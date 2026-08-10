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

import type { NavGroup, NavItem } from '@/components/layout/types'
import { USER_STATUS } from '@/features/users/constants'
import { useAuthStore, type AuthUser } from '@/stores/auth-store'

import { QY_PAGES } from './pages'
import type { QyFeatures } from './types'

/**
 * 「被限制的账号」在前端的**唯一**判定与**唯一**白名单。
 *
 * ── 语义 ──
 * 「禁用」不再是一刀切封号，而是**受限账号**：能登录、能提工单申诉，但不能动
 * 任何资源与钱。这里只负责"降级展示"——把用户到不了的入口从界面上拿掉，
 * 免得他到处点、到处撞 401/403。
 *
 * ⚠️ **这不是安全边界。** 真正的边界是后端鉴权层的白名单（会话链
 * `middleware/auth.go` 与令牌链两条各判一次）。本文件里的任何判定都可以被
 * 改 localStorage 绕过，绕过之后用户看到的是一堆 403 —— 那正是后端在挡。
 * 反过来也成立：后端放开一条路由不等于前端要给它一个入口。
 *
 * ── 状态字段的来源（与后端已经约定好，不是猜的）──
 * 字段是上游既有的 `status`，来自 `GET /api/user/self` 的 `data.status`，
 * 以及登录 / `POST /api/user/auth/refresh` 返回的 AuthBundle 里同一个 `user`
 * 对象 —— 三者共用后端同一个 DTO 构造函数 `buildSelfUserData`
 * （`controller/user.go`，其中 `"status": user.Status`），所以三条路径下发的
 * 一定是同一个值。前端侧它已经落在 {@link AuthUser.status} 上，
 * `applyAuthBundle` 写进 store，本文件只是读。
 *
 * 取值对齐后端 `common/constants.go`：`1 = enabled`、`2 = disabled`。
 * 前端的常量表在 `features/users/constants.ts` 的 {@link USER_STATUS}，
 * 这里直接引用而不是再写一个 2 —— 同一个魔数的第二份拷贝迟早漂移。
 */

/**
 * 该用户是否处于受限状态。
 *
 * ── 判据为什么是「有值且不等于 enabled」而不是「等于 disabled」──
 * 后端将来若引入第三态（设计路产出里提到的「受限」），`=== DISABLED` 会静默
 * 把它当成正常账号放行，而那正是这个仓反复栽的"漏一处就是一条静默死路"。
 * 反过来写成 `!== ENABLED` 也不行：`status` 在类型上是可选的，冷启动或后端
 * 某天忘记下发时它是 `undefined`，那样**所有人**都会被判成受限、整站只剩工单。
 * 两个方向的失败代价不对称，所以判据是「字段确实来了，且不是 enabled」：
 * 字段缺失一律按正常账号处理（展示层 fail-open，安全由后端兜底）。
 */
export function isQyRestrictedUser(
  user: Pick<AuthUser, 'status'> | null | undefined
): boolean {
  const status = user?.status
  if (typeof status !== 'number') return false
  return status !== USER_STATUS.ENABLED
}

/**
 * 组件里读受限状态。订阅的是 `auth.user.status` 这一个字段，
 * 所以别的用户字段变化不会引起重渲染。
 */
export function useQyIsRestricted(): boolean {
  return useAuthStore((state) => isQyRestrictedUser(state.auth.user))
}

/**
 * 受限账号仍然可以到达的前端路由。**白名单，清单之外一律落到受限说明页。**
 *
 * 与后端鉴权白名单是两份清单，这是刻意的：后端那份管"接口能不能调"，
 * 这份管"界面上有没有入口 / 直接输 URL 会看到什么"。后端那份比这份宽
 * （登录、登出、会话自检、站点基础信息都在它里面，但它们不是页面）。
 *
 * 两页各自的理由：
 *   · `/qy/tickets`    —— 项目方要求的申诉通道本身；
 *   · `/qy/violations` —— 「你为什么被限制」。少了它，申诉入口就是一个让人
 *      对着空白框猜的东西，最后变成工单洪水。后端白名单里的
 *      `/api/qy/violation/my-summary`、`/my-records`、`POST /appeals`
 *      正好是这一页在用的三个端点。
 *
 * 用 url 数组而不是把标题/图标再抄一遍：标题与图标从 {@link QY_PAGES}
 * 那张单一登记表里取（见 {@link qyRestrictedNavGroups}），
 * `__tests__/account-status.test.ts` 双向断言清单里的每个 url 都在表里。
 */
export const QY_RESTRICTED_URLS: readonly string[] = [
  '/qy/tickets',
  '/qy/violations',
]

/**
 * 这个路径对受限账号是否可达。
 *
 * 前缀匹配到**路径段**为止：`/qy/tickets` 与 `/qy/tickets/` 与
 * `/qy/tickets/T-123` 命中，`/qy/tickets-admin`、`/qy/admin/tickets` 不命中。
 * 写成裸 `startsWith(url)` 会让任何以白名单项开头的新路由自动获得豁免 ——
 * 那就从白名单退化成了黑名单。
 */
export function isQyRestrictedPathAllowed(pathname: string): boolean {
  const path = pathname.length > 1 ? pathname.replace(/\/+$/, '') : pathname
  return QY_RESTRICTED_URLS.some(
    (url) => path === url || path.startsWith(`${url}/`)
  )
}

/** 受限账号的侧栏分组 id。上游角色过滤只认 `admin`，这个 id 不会被它裁掉。 */
export const QY_RESTRICTED_NAV_GROUP_ID = 'qy-restricted'

/**
 * 受限账号的整棵根导航。
 *
 * **由白名单构造，而不是把正常导航过滤一遍** —— 这是本次最关键的一条设计约束。
 * 过滤式（黑名单）的失败方式是：今后任何新增的侧栏项默认对受限账号可见，
 * 而且没有人会注意到。构造式则相反：新增页面默认不可见，要让它可见必须显式
 * 往 {@link QY_RESTRICTED_URLS} 里加一行。
 *
 * 页面被功能开关关掉（`features.ticket === false`）时这一行不生成；两行都不
 * 生成时返回空数组，侧栏整个空掉 —— 那是诚实的：此时站点确实没给受限账号
 * 留任何自助通道，受限说明页会改口让用户去找站点管理员。
 */
export function qyRestrictedNavGroups(
  features: QyFeatures,
  t: TFunction
): NavGroup[] {
  const items: NavItem[] = []
  for (const url of QY_RESTRICTED_URLS) {
    const page = QY_PAGES.find((candidate) => candidate.url === url)
    // 登记表里查不到就不生成这一行：宁可少一个入口，也不要一行点进去 404 的死链。
    if (page == null) continue
    if (page.feature != null && !features[page.feature]) continue
    items.push({ title: t(page.titleKey), url: page.url, icon: page.icon })
  }

  if (items.length === 0) return []
  return [
    {
      id: QY_RESTRICTED_NAV_GROUP_ID,
      title: t('qy_restricted_nav_group'),
      items,
    },
  ]
}
