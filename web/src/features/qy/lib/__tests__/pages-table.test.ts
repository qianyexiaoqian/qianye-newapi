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

import {
  QY_NAV_CLUSTERS,
  QY_NAV_GROUPS,
  QY_PAGES,
  isQyAdminPage,
} from '@/features/qy/lib/pages'
import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

/**
 * `lib/pages.ts` 是页面清单的唯一登记处（标题 / 功能开关 / 侧栏落点 / 图标 /
 * 日文副标 / 英文副标六件事合成一行）。合并之前这些信息散在三个文件里，本项目
 * 反复踩"同一概念的第 N 份拷贝各自漂移"，所以这里把结构不变式全部钉死。
 */

// __tests__ → lib → qy → features → src
const srcDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..'
)
const sidebarDataSource = readFileSync(
  join(srcDir, 'hooks', 'use-sidebar-data.ts'),
  'utf8'
)

const enKeys = en as Record<string, string>
const zhKeys = zh as Record<string, string>

/**
 * 目前收在折叠项里的 6 个页面。
 *
 * 折叠项的二级条目走上游 `SidebarMenuSubItem`，那里没有接 `useQyNavEnLabel`，
 * 所以 Steins Gate 下它们只显示中文（与上游 system-settings 的二级菜单一致）。
 * 这条清单把"当前谁没有英文副标"写死：把某一页挪出/挪进折叠项时会红，
 * 提醒同时复核 `nav-group.tsx` 要不要补那 ~15 行。
 */
const CLUSTERED_URLS = [
  '/qy/transfer',
  '/qy/transfer-logs',
  '/qy/withdraw',
  '/qy/withdrawals',
  '/qy/admin/transfer-config',
  '/qy/admin/transfer-group-rules',
]

describe('qy page table structure', () => {
  test('每一页恰好有一个落点：group 或 cluster', () => {
    for (const page of QY_PAGES) {
      const hasGroup = page.group != null
      const hasCluster = page.cluster != null
      assert.ok(
        hasGroup !== hasCluster,
        `${page.url} 必须且只能声明 group 或 cluster 之一（当前 group=${page.group}, cluster=${page.cluster}）`
      )
    }
  })

  test('cluster 引用的折叠项都存在，且没有空折叠项', () => {
    const declared = new Set(QY_NAV_CLUSTERS.map((cluster) => cluster.id))
    const used = new Set<string>()
    for (const page of QY_PAGES) {
      if (page.cluster == null) continue
      assert.ok(declared.has(page.cluster), `${page.url} 引用了未定义的折叠项`)
      used.add(page.cluster)
    }
    for (const cluster of QY_NAV_CLUSTERS) {
      assert.ok(used.has(cluster.id), `折叠项 ${cluster.id} 没有任何子项`)
    }
  })

  test('一级项都有图标，折叠子项都没有', () => {
    for (const page of QY_PAGES) {
      if (page.cluster == null) {
        // 上游每个一级侧栏项都有 lucide 图标；qy 项漏图标会整行左对齐错位。
        assert.ok(page.icon != null, `${page.url} 是一级项，必须有图标`)
      } else {
        assert.equal(
          page.icon,
          undefined,
          `${page.url} 是折叠子项，上游二级菜单不带图标`
        )
      }
    }
  })

  test('新增分组的成员在角色维度上是齐的', () => {
    for (const group of QY_NAV_GROUPS) {
      const members = QY_PAGES.filter((page) => {
        if (page.group === group.id) return true
        const cluster = QY_NAV_CLUSTERS.find((c) => c.id === page.cluster)
        return cluster?.group === group.id
      })
      assert.ok(members.length > 0, `分组 ${group.id} 没有任何页面`)
      const admin = members.filter((page) => isQyAdminPage(page.url))
      assert.ok(
        admin.length === 0 || admin.length === members.length,
        `分组 ${group.id} 混了 ${admin.length} 个管理页和 ${members.length - admin.length} 个用户页；分组的可见性跟着成员走，混编会让普通用户看到一个半空的「${group.titleKey}」分组，标题与实际内容对不上`
      )
    }
  })

  test('新增分组的规模不超过上游 admin 组（7 行）', () => {
    for (const group of QY_NAV_GROUPS) {
      const rows =
        QY_PAGES.filter((page) => page.group === group.id).length +
        QY_NAV_CLUSTERS.filter((cluster) => cluster.group === group.id).length
      assert.ok(
        rows <= 7,
        `分组 ${group.id} 有 ${rows} 行，又变成一长条平铺了 —— 请拆分组或收进折叠项`
      )
    }
  })

  test('所有 after 锚点都是上游根侧栏里真实存在的 url', () => {
    const anchors = [
      ...QY_PAGES.map((page) => page.after),
      ...QY_NAV_CLUSTERS.map((cluster) => cluster.after),
    ].filter((anchor): anchor is string => anchor != null)

    assert.ok(anchors.length > 0, '锚点全丢了：qy 项会整体堆到各组末尾')
    for (const anchor of anchors) {
      assert.ok(
        sidebarDataSource.includes(`url: '${anchor}',`),
        `锚点 ${anchor} 在 hooks/use-sidebar-data.ts 里已经不存在（上游改名？）—— 合并会 fail-open 追加到组尾，位置不再是设计意图`
      )
    }
  })
})

describe('qy page table i18n', () => {
  test('每个登记的 key 在 en 与 zh 里都存在', () => {
    const keys = [
      ...QY_PAGES.flatMap((page) => [page.titleKey, page.jpKey, page.enKey]),
      ...QY_NAV_GROUPS.map((group) => group.titleKey),
      ...QY_NAV_CLUSTERS.map((cluster) => cluster.titleKey),
    ]
    for (const key of keys) {
      assert.ok(enKeys[key] != null, `en.json 缺少 ${key}`)
      assert.ok(zhKeys[key] != null, `zh.json 缺少 ${key}`)
    }
  })

  test('en 与 zh 的键数相等', () => {
    assert.equal(
      Object.keys(enKeys).length,
      Object.keys(zhKeys).length,
      'qy 的 en/zh 必须逐键对齐'
    )
  })

  test('英文副标只在一级项上渲染，折叠子项当前没有', () => {
    const clustered = QY_PAGES.filter((page) => page.cluster != null).map(
      (page) => page.url
    )
    assert.deepEqual(
      [...clustered].sort(),
      [...CLUSTERED_URLS].sort(),
      '折叠结构变了：请同时复核 nav-group.tsx 是否需要给 SidebarMenuSubItem 接上 useQyNavEnLabel'
    )
  })
})
