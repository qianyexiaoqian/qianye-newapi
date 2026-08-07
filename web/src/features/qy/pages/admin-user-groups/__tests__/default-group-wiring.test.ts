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
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

/**
 * 「两个用户分组页合并成一个」这次改动的守卫。
 *
 * 项目方原话：「当前为何要有 2 个用户分组？只保留一个新的即可，旧的这个移除掉。」
 * 被移除的那一页（`/qy/admin/user-group`）整页只有**一个**设置项 ——「新注册用户
 * 落进哪个分组」。删页容易，把那一项一起删掉更容易：它没有任何编译期依赖，
 * 端点还在、后端模块还在、审计还在写，只是前端再没有任何地方能改它。表现是
 * 站长某天发现新注册用户全都落在 `default`，而他记得自己配过别的。
 *
 * 所以这里同时钉住两个方向：旧入口真的没了，且那一项真的落在了新页面上。
 * 只钉其中一个方向都会被"删干净了但功能也没了"或"两个入口又长回来了"绕过。
 */

// __tests__ → admin-user-groups → pages → qy → features
const featuresDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..'
)
const srcDir = join(featuresDir, '..')

describe('新用户默认分组的唯一落点', () => {
  test('「用户分组」section 真的挂了那张卡片', () => {
    const section = readFileSync(
      join(featuresDir, 'system-settings', 'groups', 'user-groups-section.tsx'),
      'utf8'
    )
    assert.match(
      section,
      /import \{ QyNewUserDefaultGroupCard \} from '@\/features\/qy\/pages\/admin-user-groups\/default-group'/,
      '卡片没有被引入'
    )
    assert.match(
      section,
      /<QyNewUserDefaultGroupCard\s*\/>/,
      '引入了却没渲染：导入语句会被 lint 挑出来，但如果顺手改成 `import type` 就彻底静默了'
    )
  })

  test('卡片是这项配置的唯一编辑器', () => {
    // 判据取"谁调用了保存端点"。这项配置走自己的 PUT（自带审计），
    // 不在本页那次 `updateOption` 批量保存的键域里 —— 第二个调用点出现，
    // 就意味着又有了第二份基线与第二道保存闸门。
    const card = readFileSync(
      join(
        featuresDir,
        'qy',
        'pages',
        'admin-user-groups',
        'default-group',
        'index.tsx'
      ),
      'utf8'
    )
    assert.match(card, /qySaveUserGroupConfig/)
    assert.match(
      card,
      /qyUserGroupConfigQuery/,
      '卡片必须自己读配置，而不是等别人把值传进来 —— 传进来的值来自 `system-options`，' +
        '而这项配置根本不在那份 option 里'
    )
  })

  test('旧的整页入口一处都不剩', () => {
    for (const dead of [
      join(featuresDir, 'qy', 'pages', 'admin-user-group'),
      join(srcDir, 'routes', '_authenticated', 'qy', 'admin', 'user-group'),
    ]) {
      assert.ok(
        !existsSync(dead),
        `${dead} 还在：目录留着就迟早会有人把它接回路由，两个用户分组页原样复现`
      )
    }
    // 生成的路由表也必须跟着重生成，否则 `tsgo -b` 会去 import 一个不存在的文件。
    const tree = readFileSync(join(srcDir, 'routeTree.gen.ts'), 'utf8')
    assert.ok(
      !tree.includes('qy/admin/user-group'),
      'routeTree.gen.ts 里还留着已删路由：路由表是生成物，删文件之后必须重新生成'
    )
  })
})
