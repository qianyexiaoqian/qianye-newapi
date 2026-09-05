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
 * qy 的配置类管理页并进上游「系统设置」抽屉（需求 8）。
 *
 * # 守什么
 *
 * 上一轮勘察指出的约束：qy 页面的 url 是 `/qy/admin/*`，而系统设置是一个
 * 按 `pathPattern` 匹配 pathname 的 drill-in 视图 —— 菜单项加进去了、
 * pattern 没跟上，就是"点一下侧栏立刻掉回根导航"。所以这里必须**两边一起断言**：
 *
 *   1. 菜单里出现（`mergeQySystemSettingsNavGroups`）；
 *   2. 那几个 url 被 `pathPattern` 认下来（`QY_SYSTEM_SETTINGS_PATH_PATTERN`）；
 *   3. 上游 `system-settings.config.ts` 真的接了这两样 —— 只测导出的常量而
 *      不测消费方，正是本仓反复出现的"变量赋了值但没人用"；
 *   4. 反方向：留在根侧栏的流水/审核页**不许**被 pattern 认下来，否则从根侧栏
 *      点进「邀请管理」，侧栏会换成设置抽屉，把人甩出当前上下文。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import type { TFunction } from 'i18next'

import type { NavCollapsible, NavGroup } from '@/components/layout/types'
import { QY_SETTINGS_PAGES } from '@/features/qy/lib/pages'
import type { QyFeatures } from '@/features/qy/lib/types'
import {
  QY_SYSTEM_SETTINGS_PATH_PATTERN,
  mergeQySystemSettingsNavGroups,
} from '@/features/qy/system-settings'
import { ROLE } from '@/lib/roles'

const t = ((key: string) => key) as unknown as TFunction

const ALL_ON: QyFeatures = {
  transfer: true,
  invite: true,
  commission: true,
  availability: true,
  lottery: true,
  violation: true,
  ticket: true,
  group_matrix: true,
  pay_password: true,
  stardust: true,
  mall: true,
}

/**
 * 上游抽屉的最小复刻：一个分组、四个折叠项。
 *
 * 「模型与路由」「安全与限制」两项的子项 url 必须是**真的**
 * `/system-settings/{models,security}/*` —— 挂进上游折叠项的判据就是这个前缀
 * （`QY_SETTINGS_SECTION_PATHS`）。写成 '/x' 这类占位符会让本文件永远走
 * fail-open 那条路，"挂到模型与路由下面"这件事一个字节都没被测到。
 */
function baseGroups(): NavGroup[] {
  return [
    {
      id: 'system-administration',
      title: 'System Administration',
      items: [
        { title: 'Site & Branding', items: [{ title: 'Site', url: '/x' }] },
        {
          title: 'Models & Routing',
          items: [{ title: 'Global', url: '/system-settings/models/global' }],
        },
        {
          title: 'Security & Limits',
          items: [
            {
              title: 'Rate Limit',
              url: '/system-settings/security/rate-limit',
            },
          ],
        },
        { title: 'Operations', items: [{ title: 'Ops', url: '/y' }] },
      ],
    },
  ]
}

function collapsibleItems(groups: NavGroup[], title: string): string[] | null {
  const found = (groups[0]?.items ?? []).find(
    (item) => item.title === title
  ) as NavCollapsible | undefined
  return found == null ? null : found.items.map((sub) => String(sub.url))
}

function qyGroupItems(groups: NavGroup[]): string[] | null {
  return collapsibleItems(groups, 'qy_nav_group_settings')
}

/** 表里声明了要挂进上游折叠项的页面，不再是「扩展设置」那一组的成员。 */
const OWN_SETTINGS_PAGES = QY_SETTINGS_PAGES.filter(
  (page) => page.settingsSection == null
)

describe('并进系统设置抽屉的菜单项', () => {
  test('超级管理员看到的那一组 = 未另行指定落点的配置页，顺序一致', () => {
    const merged = mergeQySystemSettingsNavGroups(
      baseGroups(),
      ALL_ON,
      ROLE.SUPER_ADMIN,
      t
    )
    assert.deepEqual(
      qyGroupItems(merged),
      OWN_SETTINGS_PAGES.map((page) => page.url)
    )
    // 上游那三组必须原样保留在前面，qy 这一组追加在最后。
    assert.deepEqual(
      merged[0]?.items.map((item) => item.title),
      [
        'Site & Branding',
        'Models & Routing',
        'Security & Limits',
        'Operations',
        'qy_nav_group_settings',
      ]
    )
  })

  /**
   * 项目方 2026-09-05：「API 地址这个页面菜单，从扩展设置移动到模型与路由下面。」
   *
   * 两个方向一起钉：它出现在上游「模型与路由」的末尾，**并且**不再出现在
   * 「扩展设置」里 —— 只钉前者的话，两处都挂上（同一页两个入口）也照样全绿。
   */
  test('API 地址挂在上游「模型与路由」下面，不在扩展设置里', () => {
    const merged = mergeQySystemSettingsNavGroups(
      baseGroups(),
      ALL_ON,
      ROLE.SUPER_ADMIN,
      t
    )
    assert.deepEqual(collapsibleItems(merged, 'Models & Routing'), [
      '/system-settings/models/global',
      '/qy/admin/api-address',
    ])
    assert.ok(
      !(qyGroupItems(merged) ?? []).includes('/qy/admin/api-address'),
      '同一页同时挂在两个折叠项下 —— 两个互不知情的入口'
    )
  })

  /**
   * 项目方 2026-09-05：「把这 2 个菜单，移动到安全与限制下：违规规则 / AI 内容审核。」
   *
   * 同一次拍板删掉了上游自带的敏感词过滤（docs/decisions.md D-16），所以这两页
   * 现在是「安全与限制」里唯一管"什么内容不许过"的东西。顺序与页面表一致：
   * 违规规则在前、AI 内容审核在后。
   *
   * 与上一条一样两个方向一起钉，另外把「违规类型」也钉进反方向 —— 它**没有**
   * 写 settingsSection，仍旧留在「扩展设置」里。少了这一句，把整个 violation
   * 三件套一起搬过去也照样全绿，而那不是项目方要的。
   */
  test('违规规则与 AI 内容审核挂在上游「安全与限制」下面', () => {
    const merged = mergeQySystemSettingsNavGroups(
      baseGroups(),
      ALL_ON,
      ROLE.SUPER_ADMIN,
      t
    )
    assert.deepEqual(collapsibleItems(merged, 'Security & Limits'), [
      '/system-settings/security/rate-limit',
      '/qy/admin/violation-rules',
      '/qy/admin/violation-ai-review',
    ])
    const own = qyGroupItems(merged) ?? []
    assert.ok(
      !own.includes('/qy/admin/violation-rules') &&
        !own.includes('/qy/admin/violation-ai-review'),
      '同一页同时挂在两个折叠项下 —— 两个互不知情的入口'
    )
    assert.ok(
      own.includes('/qy/admin/violation-categories'),
      '「违规类型」被一起搬走了：项目方点名的只有规则与 AI 审核两页'
    )
  })

  /**
   * fail-open：上游改了 `/system-settings/models` 的路径、或整组删掉时，
   * 页面**落回「扩展设置」**而不是从抽屉里消失。
   *
   * 这条不是假想：本仓已经五次栽在"功能在、入口没了"上，而这次的判据是一个
   * 上游随时可改的 url 前缀。
   */
  test('上游那一组认不出来时，落回扩展设置（入口不消失）', () => {
    const withoutModels: NavGroup[] = [
      {
        id: 'system-administration',
        title: 'System Administration',
        items: [{ title: 'Operations', items: [{ title: 'Ops', url: '/y' }] }],
      },
    ]
    const merged = mergeQySystemSettingsNavGroups(
      withoutModels,
      ALL_ON,
      ROLE.SUPER_ADMIN,
      t
    )
    assert.ok(
      (qyGroupItems(merged) ?? []).includes('/qy/admin/api-address'),
      'API 地址在抽屉里彻底没有入口了'
    )
  })

  /**
   * 抽屉本体的路由要求 role === SUPER_ADMIN(100)，否则 redirect('/403')。
   *
   * 这一条曾经写成 `role >= ADMIN(10)` 就生成菜单项，与那道门不一致 ——
   * 后果是 role=10 的管理员在抽屉里"有"这 8 个页面，却永远打不开承载它们的
   * 那扇门，于是这些页面在界面上等于不存在（本仓第五次"写了但找不到"）。
   * 这一档的入口改由 `nav.ts` 的 withQySettingsFallback 在根侧栏上补，
   * 由 route-entry-guard.test.ts 钉住。
   */
  test('role=10 的管理员：抽屉里整组不生成（那扇门他打不开）', () => {
    const merged = mergeQySystemSettingsNavGroups(
      baseGroups(),
      ALL_ON,
      ROLE.ADMIN,
      t
    )
    assert.equal(qyGroupItems(merged), null)
    assert.deepEqual(merged, baseGroups(), 'role<100 时应逐字返回上游原样')
  })

  test('普通用户：整组不生成（不是留一个只剩标题的折叠项）', () => {
    const merged = mergeQySystemSettingsNavGroups(
      baseGroups(),
      ALL_ON,
      ROLE.USER,
      t
    )
    assert.equal(qyGroupItems(merged), null)
    assert.deepEqual(merged, baseGroups(), '非管理员时应逐字返回上游原样')
  })

  test('功能开关关掉的页面不出现在抽屉里', () => {
    const items = qyGroupItems(
      mergeQySystemSettingsNavGroups(
        baseGroups(),
        { ...ALL_ON, transfer: false },
        ROLE.SUPER_ADMIN,
        t
      )
    )
    assert.ok(items != null)
    assert.ok(!items.includes('/qy/admin/transfer-config'))
    assert.ok(!items.includes('/qy/admin/transfer-group-rules'))
    // 控制项取「抽奖设置」而不是「违规规则」：后者已经挂到上游「安全与限制」
    // 下面（见本文件上面那两条），不再是这一组的成员，拿它当控制项等于把
    // "搬家了"误读成"被 transfer 开关误伤了"。
    assert.ok(items.includes('/qy/admin/lottery-config'), '误伤了无关的页面')
  })

  test('功能全关的管理员：整组不生成', () => {
    const merged = mergeQySystemSettingsNavGroups(
      baseGroups(),
      {
        transfer: false,
        invite: false,
        commission: false,
        availability: false,
        lottery: false,
        violation: false,
        ticket: false,
        group_matrix: false,
        pay_password: false,
        stardust: false,
        mall: false,
      },
      ROLE.SUPER_ADMIN,
      t
    )
    // 「受限账号」没有 feature 开关，所以这里仍应剩下它；断言写成"还剩谁"而不是
    // "空了"，免得把无开关的页面一起判没。
    //
    // 「新用户默认分组」曾经也在这一栏里（同样没有开关）。那一页已整体下线：
    // 它整页只有一个下拉，现在是「计费与支付 → 用户分组」section 上的一张卡片
    // （`features/qy/pages/admin-user-groups/default-group`）。
    //
    // 分组矩阵不在这一栏里，是因为它已经不是 qy 那一组折叠菜单的成员了：它整体
    // 搬进了上游抽屉的「计费与支付 → 用户分组」section。那一项的显隐由
    // `withQyBillingSectionNavItems` 判定，判据只有「扩展是否启用」——
    // `group_matrix.enabled` 关掉时入口照样留着，点进去看到后端 guard 返回 404
    // 之后的中性空态，明确告诉你没开，而不是变成一个静默消失的菜单。
    //
    // 「受限账号」同样没有开关，而且**不能有**：受限态是会话鉴权链上的一档身份
    // （`middleware/restricted_user.go`），管理员在用户管理页上禁用任何一个账号
    // 都会产生它，与 violation / ticket 两个模块开没开完全无关。挂上任何一个
    // YAML 开关，都会造出"受限状态照常发生，而解释它、配置它的那一页连入口都
    // 没有"—— 本仓反复出现的「功能在，入口没了」。
    //
    // API 地址簿同样没有开关，但它已经不在这一栏里了：项目方 2026-09-05 把它
    // 挂到了上游「模型与路由」下面，由上一条测试逐项钉住。
    assert.deepEqual(qyGroupItems(merged), ['/qy/admin/restricted-accounts'])
    assert.deepEqual(collapsibleItems(merged, 'Models & Routing'), [
      '/system-settings/models/global',
      '/qy/admin/api-address',
    ])
  })
})

describe('drill-in 视图的路径匹配', () => {
  const configSource = readFileSync(
    join(
      dirname(fileURLToPath(import.meta.url)),
      '..',
      '..',
      '..',
      'components',
      'layout',
      'config',
      'system-settings.config.ts'
    ),
    'utf8'
  )

  test('上游 system-settings.config.ts 真的接上了这两样', () => {
    // 只读源码不 import：那个模块会把 7 个 section registry（含 JSX 与业务
    // 组件）整棵拉进来，在 node:test 环境里挂不起来。这里要证的只是"接线在"。
    assert.ok(
      /pathPattern:\s*QY_SYSTEM_SETTINGS_PATH_PATTERN/.test(configSource),
      'pattern 导出了却没人用：菜单里点得到，点进去侧栏掉回根导航'
    )
    assert.ok(
      /return withQySystemSettingsNavGroups\(/.test(configSource),
      '合并函数没有被调用：抽屉里根本不会出现 qy 那一组'
    )
    assert.ok(
      !/pathPattern:\s*\/\^/.test(configSource),
      '又写回了字面量 pattern：qy 的页面会认不出来'
    )
  })

  test('上游自己的路径照旧匹配', () => {
    for (const path of ['/system-settings', '/system-settings/site']) {
      assert.ok(QY_SYSTEM_SETTINGS_PATH_PATTERN.test(path), path)
    }
  })

  test('抽屉里的每一页都匹配', () => {
    for (const page of QY_SETTINGS_PAGES) {
      assert.ok(
        QY_SYSTEM_SETTINGS_PATH_PATTERN.test(page.url),
        `${page.url} 在抽屉菜单里，但侧栏视图认不出它 —— 点一下就被踢回根导航`
      )
    }
  })

  test('留在根侧栏的流水/审核页一个都不匹配', () => {
    for (const path of [
      '/qy/admin/invite',
      '/qy/admin/daily-consume',
      '/qy/admin/settlement',
      '/qy/admin/commission-records',
      '/qy/admin/transfer-records',
      '/qy/admin/fund-orders',
      '/qy/admin/violations',
      '/qy/admin/audit-logs',
      '/qy/admin/health',
      '/qy/affiliate',
      '/wallet',
    ]) {
      assert.ok(
        !QY_SYSTEM_SETTINGS_PATH_PATTERN.test(path),
        `${path} 被当成了设置抽屉的一部分：从根侧栏点进去侧栏会整块换掉`
      )
    }
  })

  test('前缀不许误伤：lottery-config 不能顺带吃掉 lottery-config-x', () => {
    // 单独钉是因为这是最容易写错的一处：少了结尾的 `(\/|$)`，任何以抽屉页
    // url 开头的新路由都会被静默认成抽屉的一部分。
    assert.ok(QY_SYSTEM_SETTINGS_PATH_PATTERN.test('/qy/admin/lottery-config'))
    assert.ok(QY_SYSTEM_SETTINGS_PATH_PATTERN.test('/qy/admin/lottery-config/'))
    assert.ok(
      !QY_SYSTEM_SETTINGS_PATH_PATTERN.test('/qy/admin/lottery-config-x')
    )
  })
})
