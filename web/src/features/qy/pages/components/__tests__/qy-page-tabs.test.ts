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

import { QY_TAB_GROUPS } from '@/features/qy/lib/pages'

/**
 * 选择夹的**覆盖度**：清单上有几张标签，宿主页就必须给出几份正文。
 *
 * 这是本仓最常见的那种「断链」：`QY_TAB_GROUPS` 里加了一行，侧栏立刻把那一页
 * 的独立入口撤掉，而宿主页忘了给正文 —— `QyPageTabs` 里的
 * `if (props.bodies[url] == null) return []` 会**安静地**跳过它，结果是这个页面
 * 在整个前端里再也到不了，而且没有任何报错。
 *
 * 所以这里按源码扫：两个宿主组件里必须逐字出现自己那一组的每一个 url。
 * 之所以扫源码而不是渲染组件，是因为渲染需要路由 + react-query + zustand 三套
 * provider，而要守的东西其实只是"这张表和那份 bodies 对得上"。
 */

// __tests__ → components → pages → qy
const qyDir = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..')

/** 宿主 url → 提供 bodies 的组件文件。新增选择夹时这里要跟着加一行。 */
const HOST_SOURCES: Readonly<Record<string, string>> = {
  '/wallet': join(qyDir, 'pages', 'wallet-transfer', 'index.tsx'),
  '/qy/affiliate': join(qyDir, 'pages', 'affiliate', 'hub.tsx'),
  '/qy/lottery': join(qyDir, 'pages', 'lottery', 'hub.tsx'),
}

describe('选择夹的正文覆盖度', () => {
  test('每个宿主都有一个登记在案的实现文件', () => {
    assert.deepEqual(
      QY_TAB_GROUPS.map((group) => group.host).sort(),
      Object.keys(HOST_SOURCES).sort(),
      '新增/删除选择夹时，本测试的 HOST_SOURCES 没有跟着改'
    )
  })

  for (const group of QY_TAB_GROUPS) {
    test(`${group.host} 为每一张标签都提供了正文`, () => {
      const source = readFileSync(HOST_SOURCES[group.host] as string, 'utf8')
      for (const url of group.pages) {
        assert.ok(
          source.includes(`'${url}'`),
          `${group.host} 的选择夹里有 ${url}，但宿主组件没给它正文 —— 侧栏入口已经撤掉，这一页会变成整个前端都到不了`
        )
      }
    })
  }

  test('宿主组件不自己再列一遍标签顺序', () => {
    // 顺序的唯一真源是 QY_TAB_GROUPS。宿主里一旦出现 TabsTrigger，
    // 就意味着有人绕开 QyPageTabs 又写了一份清单。
    for (const path of Object.values(HOST_SOURCES)) {
      const source = readFileSync(path, 'utf8')
      assert.ok(
        !source.includes('TabsTrigger'),
        `${path} 自己渲染了 TabsTrigger：标签顺序会出现第二份拷贝`
      )
    }
  })
})
