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
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import type { TFunction } from 'i18next'

import type { NavGroup, NavItem } from '@/components/layout/types'
import {
  QY_INDEX_PAGES,
  QY_PAGES,
  QY_SETTINGS_GROUP,
  QY_SETTINGS_PAGES,
  QY_TAB_GROUPS,
  isQyPageHosted,
} from '@/features/qy/lib/pages'
import type { QyFeatures } from '@/features/qy/lib/types'
import { mergeQyNavGroups } from '@/features/qy/nav'
import { mergeQySystemSettingsNavGroups } from '@/features/qy/system-settings'
import { ROLE } from '@/lib/roles'

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
}

/** 上游根导航的最小复刻，锚点与 nav-merge.test.ts 同源。 */
function baseGroups(): NavGroup[] {
  return [
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
        { title: 'Channels', url: '/channels' },
        { title: 'Users', url: '/users' },
        { title: 'System Settings', url: '/system-settings/site' },
      ],
    },
  ]
}

/**
 * 某个角色**真正**能在侧栏上点到的全部 url。
 *
 * 判据必须来自 `mergeQyNavGroups` 的输出（含折叠项的子项），而不是来自页面表：
 * 拿页面表当判据就是在用"我登记了"证明"我看得见"，那正是本轮这个缺陷的形状。
 */
function sidebarUrls(role: number): Set<string> {
  const urls = new Set<string>()
  const walk = (items: readonly NavItem[]) => {
    for (const item of items) {
      if (typeof item.url === 'string') urls.add(item.url)
      const children = (item as { items?: readonly NavItem[] }).items
      if (children != null) walk(children)
    }
  }
  for (const group of mergeQyNavGroups(baseGroups(), ALL_ON, role, t)) {
    walk(group.items)
  }
  return urls
}

/** 系统设置抽屉里那一组的全部 url（抽屉只对 role=100 打得开）。 */
function settingsDrawerUrls(role: number): Set<string> {
  const urls = new Set<string>()
  const walk = (items: readonly NavItem[]) => {
    for (const item of items) {
      if (typeof item.url === 'string') urls.add(item.url)
      const children = (item as { items?: readonly NavItem[] }).items
      if (children != null) walk(children)
    }
  }
  for (const group of mergeQySystemSettingsNavGroups(
    [{ id: 'settings', title: 'Settings', items: [] }],
    ALL_ON,
    role,
    t
  )) {
    walk(group.items)
  }
  return urls
}

/**
 * 「有路由就必须有入口」的机器判据。
 *
 * ── 为什么这条守卫值一整个文件 ──
 * 这个仓库第四次栽在同一件事上：页面写完、路由建好、后端接口通了，
 * 而 `lib/pages.ts` 里那一行**忘了加**。表现是站内一个入口都没有，只有手敲
 * URL 才到得了 —— 双色球、可用范围开关、渠道额度清零，以及本轮的佣金余额
 * 与 AFF 关系。项目方三次反馈「怎么没看见」，三次都是这个形状。
 *
 * 已有的 `pages-table.test.ts` 只守了**反方向**（登记的 url 必须有路由文件，
 * 防手敲地址 404）。反方向全绿恰恰是这次事故的现场：两页的路由在、组件在、
 * 页面表里零行，于是没有任何东西会红。
 *
 * ── 判据是"文件系统里的路由" vs "页面表里的登记" ──
 * 扫 `src/routes/_authenticated/qy/` 的实际文件，而不是 `routeTree.gen.ts`：
 * 后者是构建产物，拿它当判据会让这条守卫依赖"某次构建有没有跑过"。
 *
 * "有入口"的定义刻意宽于"侧栏上有一行"：
 *   · 根侧栏一级项（`group` 是普通分组）；
 *   · 上游系统设置抽屉里的一项（`group === QY_SETTINGS_GROUP`）；
 *   · 某个选择夹里的一张标签（`isQyPageHosted`）。
 * 三者都是"用户点得到"，都算数。不算数的只有一种：**页面表里根本没有这一行**。
 */

// __tests__ → lib → qy → features → src
const srcDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..'
)
const qyRoutesDir = join(srcDir, 'routes', '_authenticated', 'qy')

/**
 * 豁免清单。**每一条都必须写清"那它的入口在哪"**，否则它就是下一次事故。
 *
 * 这里只收 url —— 布局路由（`route.tsx`）与动态段路由（`$actNo`）由扫描器
 * 按形状排除，两个工作区索引页由 `QY_INDEX_PAGES` 认领，都不进本表：
 * 它们各有一条**普适**规则，逐个列举只会让表越来越长而信息量为零。
 * 本表只收"确实没有任何登记、但入口另在别处"的那一种。
 */
const EXEMPT: { url: string; why: string }[] = [
  {
    url: '/qy/admin/group-matrix',
    why: '日常配置已搬进「系统设置 → 计费与支付 → 用户分组」的编辑弹窗；本页降级成高级/诊断视图（整列批量、跨档对比、孤儿令牌基线），入口是用户分组表下方那条「高级」链接（features/qy/pages/admin-user-groups/.../user-groups-section.tsx）。刻意不进页面表：两个平级入口读写同一组数据 = 两份互不知情的草稿',
  },
]

/** 路由文件路径 → 页面 url；不是"一个页面"的返回 null。 */
function routeUrl(relativePath: string): string | null {
  const posix = relativePath.replaceAll('\\', '/')
  // 布局路由：只提供守卫与 <Outlet/>，没有自己的可访问内容。
  if (posix === 'route.tsx' || posix.endsWith('/route.tsx')) return null
  // 动态段：详情页，入口是它所属列表页上的那一行。列表页自己会被本守卫检查，
  // 所以详情页不可能变成孤儿。
  if (posix.includes('$')) return null
  // `qy/index.tsx` 自己是 `/qy`；其余是 `<目录>/index.tsx`。两种都要认，
  // 只写后者会让工作区索引页整个从扫描结果里消失，豁免清单随之开始骗人。
  if (posix === 'index.tsx') return '/qy'
  if (!posix.endsWith('/index.tsx')) return null
  return `/qy/${posix.slice(0, -'/index.tsx'.length)}`
}

/** 递归收集 `src/routes/_authenticated/qy/` 下的所有页面 url。 */
function scanRouteUrls(): string[] {
  const urls: string[] = []
  const walk = (dir: string, prefix: string) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const rel = prefix === '' ? entry.name : `${prefix}/${entry.name}`
      if (entry.isDirectory()) {
        walk(join(dir, entry.name), rel)
        continue
      }
      if (!entry.name.endsWith('.tsx')) continue
      const url = routeUrl(rel)
      if (url != null) urls.push(url)
    }
  }
  walk(qyRoutesDir, '')
  return urls.sort()
}

const routeUrls = scanRouteUrls()
const registered = new Set(QY_PAGES.map((page) => page.url))
const indexUrls = new Set(QY_INDEX_PAGES.map((page) => page.url))
const exemptUrls = new Set(EXEMPT.map((item) => item.url))

describe('qy 路由 ↔ 导航入口', () => {
  /** 扫描器本身失效时（目录改名、正则写错）必须变红，而不是静默全绿。 */
  test('扫描器确实扫到了路由', () => {
    assert.ok(
      routeUrls.length >= 20,
      `只扫到 ${routeUrls.length} 条路由，扫描器多半坏了 —— 下面所有断言都会变成空转`
    )
    for (const url of ['/qy/affiliate', '/qy/admin/commission-records']) {
      assert.ok(routeUrls.includes(url), `扫描器漏了 ${url}`)
    }
    // 形状排除必须真的生效，否则布局路由与详情页会以孤儿身份刷屏。
    assert.ok(!routeUrls.includes('/qy/admin/lottery/$actNo'))
    assert.ok(!routeUrls.some((url) => url.endsWith('/route')))
  })

  /**
   * 本仓第四次事故的直接判据。
   *
   * 变红的含义只有一个：某个页面写完了、路由建好了，而 `lib/pages.ts` 里
   * 那一行忘了加，于是站内一个入口都没有。
   */
  test('每条路由都在页面表里有登记（否则站内一个入口都没有）', () => {
    const orphans = routeUrls.filter(
      (url) =>
        !registered.has(url) && !indexUrls.has(url) && !exemptUrls.has(url)
    )
    assert.deepEqual(
      orphans,
      [],
      `以下页面有路由但没有任何入口，只能手敲 URL 才到得了 —— 请在 lib/pages.ts 里登记，或写进本文件的 EXEMPT 并说明入口在哪：\n${orphans.join('\n')}`
    )
  })

  /** 豁免清单不许烂掉：指向已删路由的豁免会把一条真的孤儿悄悄放行。 */
  test('豁免清单每一条都仍然存在，且都写了理由', () => {
    for (const item of EXEMPT) {
      assert.ok(
        routeUrls.includes(item.url),
        `豁免了 ${item.url}，但它已经没有路由文件了 —— 删掉这一条，否则它会一直挡着同名新页面`
      )
      assert.ok(
        item.why.trim().length >= 20,
        `${item.url} 的豁免理由太短，说不清"那它的入口在哪"`
      )
      assert.ok(
        !registered.has(item.url),
        `${item.url} 既登记进了页面表又被豁免；豁免是"页面表之外另有入口"的说法，两者同时成立时清单就开始骗人了`
      )
    }
    // 豁免清单只增不减是它自己烂掉的方式：补完佣金两页之后，全站只剩分组矩阵
    // 这一条真正的例外。多一条就必须在 review 里说清楚它为什么进不了页面表。
    assert.equal(
      EXEMPT.length,
      1,
      '豁免清单变长了：新页面的默认答案是"登记进 lib/pages.ts"，豁免是例外'
    )
    // 索引页走的是 QY_INDEX_PAGES 而不是豁免 —— 两条路都能让它们不变红，
    // 但只有前者会在它们被删掉时同步变红。
    for (const url of ['/qy', '/qy/admin']) {
      assert.ok(indexUrls.has(url), `${url} 从 QY_INDEX_PAGES 里消失了`)
      assert.ok(!exemptUrls.has(url), `${url} 不该靠豁免过关`)
    }
  })

  /**
   * 登记 ≠ 有入口：`group` 缺失的一行会被 `qyEntryPages` 静默滤掉。
   *
   * `pages-table.test.ts` 已从"每页恰好一个落点"的角度守过一次；这里从
   * **路由**这一侧再走一遍，因为本守卫的承诺是"路由能到达"，而不是"表结构自洽"。
   */
  test('登记过的路由都真的落到了某一处入口', () => {
    // 宿主页的入口必须来自**真实的侧栏输出**，不能来自 QY_TAB_GROUPS 自己。
    //
    // 这条断言曾经写成 `hostUrls = new Set(QY_TAB_GROUPS.map(g => g.host))`，
    // 而 host 又是 `QY_TAB_GROUPS.find(...)` 的返回值 —— 两者同源，
    // `hostUrls.has(host.host)` 恒为 true，一个字节都没守到。
    // 它本来要守的是"/wallet 被上游 sidebar_modules 关掉时，挂在它下面的
    // 三张标签会同时失去全部入口"，而那种情况下旧写法照样全绿。
    const hostUrls = sidebarUrls(ROLE.SUPER_ADMIN)
    const unreachable = routeUrls.flatMap((url) => {
      const page = QY_PAGES.find((item) => item.url === url)
      if (page == null) return []
      // 三种合法落点：根侧栏一级项 / 系统设置抽屉 / 别人选择夹里的一张标签。
      if (isQyPageHosted(url)) {
        const host = QY_TAB_GROUPS.find((group) => group.pages.includes(url))
        // 宿主页自己必须还在侧栏上，否则整组标签一起失联。
        return host != null && hostUrls.has(host.host) ? [] : [url]
      }
      if (page.group === QY_SETTINGS_GROUP) return []
      return page.group != null ? [] : [url]
    })
    assert.deepEqual(
      unreachable,
      [],
      `以下页面登记在 QY_PAGES 里，却既没有分组、也不在任何选择夹里 —— 表里有行、界面上没有入口：\n${unreachable.join('\n')}`
    )
  })

  /**
   * 抽屉里那一组页面，**每一个够得着抽屉的角色都必须真的点得到**。
   *
   * ── 本仓第五次"写了但找不到"，也是本条断言存在的全部理由 ──
   * 上一版守卫对 `QY_SETTINGS_GROUP` 无条件放行（`return []`），把"标了这个组"
   * 当成"有入口"。而事实是两处角色判据曾经不一致：
   *   · `system-settings.ts` 只要求 role >= ADMIN(10) 就生成这些菜单项；
   *   · 抽屉本体的路由要求 role === SUPER_ADMIN(100)，否则 redirect('/403')。
   * 于是 role=10 的管理员看得见「系统设置」、点进去 403，那 8 个页面
   * （违规规则/违规类型/AI 内容审核/佣金设置/划转设置/划转分组规则/抽奖设置/
   * API 地址）在界面上完全不存在 —— 而后端 requireQyAdmin 只要求 role>=10，
   * 页面本身就是给他们用的。守卫全绿，项目方又一次"怎么没看见"。
   *
   * 判据取自**真实的导航输出**（mergeQyNavGroups / mergeQySystemSettingsNavGroups），
   * 不是页面表：拿页面表当判据就是用"我登记了"证明"我看得见"。
   */
  test('设置抽屉里的页面对每一档管理员都真的点得到', () => {
    const settingsUrls = QY_SETTINGS_PAGES.map((page) => page.url)
    assert.ok(
      settingsUrls.length > 0,
      'QY_SETTINGS_PAGES 空了，下面两条断言会变成空转'
    )

    // role=100：入口在系统设置抽屉里，根侧栏上**不该**再有一份（两个入口 =
    // 两份互不知情的入口，也会让 pages-table 的"每页恰好一个落点"失去意义）。
    const drawer = settingsDrawerUrls(ROLE.SUPER_ADMIN)
    const rootForSuper = sidebarUrls(ROLE.SUPER_ADMIN)
    for (const url of settingsUrls) {
      assert.ok(drawer.has(url), `role=100 在系统设置抽屉里找不到 ${url}`)
      assert.ok(
        !rootForSuper.has(url),
        `${url} 同时出现在抽屉与根侧栏 —— 同一组页面有了两个入口`
      )
    }

    // role=10：抽屉打不开（点进去 403），所以这些页面必须出现在根侧栏上。
    assert.equal(
      settingsDrawerUrls(ROLE.ADMIN).size,
      0,
      '抽屉对 role=10 仍然生成菜单项，而它的路由会把这一档直接 redirect 到 /403'
    )
    const rootForAdmin = sidebarUrls(ROLE.ADMIN)
    const missing = settingsUrls.filter((url) => !rootForAdmin.has(url))
    assert.deepEqual(
      missing,
      [],
      `以下页面对 role=10 的管理员没有任何入口 —— 抽屉要 role=100 才打得开，而根侧栏上也没有：${missing.join(', ')}`
    )
  })

  /**
   * 普通用户（以及角色读不出来的那一档）不该因为上面那条兜底看见任何管理页。
   *
   * `undefined` 这一格不是凑数：`role` 从 auth store 里读，未登录或字段缺失时
   * 就是 undefined，而 `undefined >= 100`、`undefined < 10` 同时为 false ——
   * 兜底判据写成两个反向早退时，这一档会直接掉进兜底，把 8 个管理页铺进
   * 未登录用户的侧栏。本条第一次跑就是这样红的。
   */
  test('兜底入口不会把管理页泄漏给普通用户', () => {
    const settingsUrls = QY_SETTINGS_PAGES.map((page) => page.url)
    for (const role of [
      ROLE.GUEST,
      ROLE.USER,
      undefined as unknown as number,
    ]) {
      const visible = sidebarUrls(role)
      for (const url of settingsUrls) {
        assert.ok(
          !visible.has(url),
          `role=${String(role)} 的侧栏上出现了管理页 ${url}`
        )
      }
      assert.equal(
        settingsDrawerUrls(role).size,
        0,
        `role=${String(role)} 在系统设置抽屉里拿到了 qy 管理项`
      )
    }
  })

  /**
   * 佣金管理的两个入口，源码级钉死。
   *
   * 上面那条通用守卫已经能拦住"再删一次"，这一条另外钉的是**落点**：
   * 项目方要的是从侧栏点得到，进了设置抽屉或被收进某张标签都不算数
   * （抽屉在 `/system-settings` 下要求 role=100，普通管理员够不着）。
   */
  test('佣金余额与 AFF 关系是「结算」组的一级项', () => {
    for (const url of [
      '/qy/admin/commission-records/balances',
      '/qy/admin/commission-records/relations',
    ]) {
      const page = QY_PAGES.find((item) => item.url === url)
      assert.ok(page != null, `${url} 又从页面表里消失了`)
      assert.equal(
        page.group,
        'qy-settlement',
        `${url} 不在「结算」组的一级项上，侧栏里点不到`
      )
      assert.ok(page.icon != null, `${url} 缺图标，整行会与上游项左对齐错位`)
      assert.equal(isQyPageHosted(url), false, `${url} 被收进了别人的选择夹`)
    }
  })

  /**
   * 佣金审核页上那两个按钮不许跟着入口一起删。
   *
   * 侧栏入口与页内跳转解决的是两件事：侧栏回答"从零开始去哪找"，页内按钮
   * 回答"我正在看这一笔，另外两张表怎么开"。两者都在，运营才不用记路径。
   */
  test('佣金审核页仍然直连另外两张表', () => {
    const source = readFileSync(
      join(
        srcDir,
        'features',
        'qy',
        'pages',
        'admin-commission-records',
        'index.tsx'
      ),
      'utf8'
    )
    for (const url of [
      '/qy/admin/commission-records/balances',
      '/qy/admin/commission-records/relations',
    ]) {
      assert.ok(source.includes(url), `佣金审核页丢了指向 ${url} 的按钮`)
    }
  })
})
