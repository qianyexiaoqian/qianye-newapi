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
 *      点进「佣金审核」，侧栏会换成设置抽屉，把人甩出当前上下文。
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
  commission: true,
  withdraw: true,
  availability: true,
  violation: true,
}

/** 上游抽屉的最小复刻：一个分组、两个折叠项。 */
function baseGroups(): NavGroup[] {
  return [
    {
      id: 'system-administration',
      title: 'System Administration',
      items: [
        { title: 'Site & Branding', items: [{ title: 'Site', url: '/x' }] },
        { title: 'Operations', items: [{ title: 'Ops', url: '/y' }] },
      ],
    },
  ]
}

function qyGroupItems(groups: NavGroup[]): string[] | null {
  const qy = (groups[0]?.items ?? []).find(
    (item) => item.title === 'qy_nav_group_settings'
  ) as NavCollapsible | undefined
  return qy == null ? null : qy.items.map((sub) => String(sub.url))
}

describe('并进系统设置抽屉的菜单项', () => {
  test('管理员看到的那一组 = QY_SETTINGS_PAGES，顺序一致', () => {
    const merged = mergeQySystemSettingsNavGroups(
      baseGroups(),
      ALL_ON,
      ROLE.ADMIN,
      t
    )
    assert.deepEqual(
      qyGroupItems(merged),
      QY_SETTINGS_PAGES.map((page) => page.url)
    )
    // 上游那两组必须原样保留在前面，qy 这一组追加在最后。
    assert.deepEqual(
      merged[0]?.items.map((item) => item.title),
      ['Site & Branding', 'Operations', 'qy_nav_group_settings']
    )
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
        ROLE.ADMIN,
        t
      )
    )
    assert.ok(items != null)
    assert.ok(!items.includes('/qy/admin/transfer-config'))
    assert.ok(!items.includes('/qy/admin/transfer-group-rules'))
    assert.ok(items.includes('/qy/admin/violation-rules'), '误伤了无关的页面')
  })

  test('功能全关的管理员：整组不生成', () => {
    const merged = mergeQySystemSettingsNavGroups(
      baseGroups(),
      {
        transfer: false,
        commission: false,
        withdraw: false,
        availability: false,
        violation: false,
      },
      ROLE.ADMIN,
      t
    )
    // 分组定价与新用户默认分组没有 feature 开关，所以这里仍应剩下它们两个；
    // 断言写成"还剩谁"而不是"空了"，免得把无开关的页面一起判没。
    assert.deepEqual(qyGroupItems(merged), [
      '/qy/admin/group-pricing',
      '/qy/admin/user-group',
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
      '/qy/admin/commission-records',
      '/qy/admin/withdrawals',
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

  test('前缀不许误伤：commission 不能顺带吃掉 commission-records', () => {
    // 单独钉是因为这是最容易写错的一处：少了结尾的 `(\/|$)`，
    // 上一条里的 `/qy/admin/commission-records` 会静默匹配。
    assert.ok(QY_SYSTEM_SETTINGS_PATH_PATTERN.test('/qy/admin/commission'))
    assert.ok(
      !QY_SYSTEM_SETTINGS_PATH_PATTERN.test('/qy/admin/commission-records')
    )
  })
})
