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
import { dirname, join, relative } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

/**
 * 需求 4 的两条接线，两条都是"改错了不会有任何信号"的形状。
 *
 * ── 1. 可用范围与交叉倍率**只能有一个编辑器实现** ──
 *
 * 项目方要的是"行内弹窗直接配"，于是同一份数据现在有两个外壳（整页矩阵 +
 * 弹窗）。两个外壳本身没问题，两份**状态机**才是问题：那道"含撤销或改价的草稿，
 * 看过影响面之前不许保存"的闸门若只写在其中一份里，运营就能在弹窗里一键改掉
 * 一整档的价而完全没有影响面可看，而页面上同样的动作是被拦住的。
 * 判据取"谁调用了保存端点"—— 保存是闸门的唯一出口。
 *
 * ── 2. 「用户分组」表的操作列不许再跳走 ──
 *
 * 原话：「配置可用模型分组，这里直接弹窗选择配置，不要再跳转到用户分组可用的
 * 模型分组配置。」跳走的代价不只是多一次点击：那一页是另一个 section，本
 * section 会被卸载，运营正在编辑的充值折扣草稿全部丢失。
 */

// __tests__ → admin-user-groups → pages → qy → features
const featuresDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..'
)

function collectSources(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name !== '__tests__' && entry.name !== 'node_modules') {
        collectSources(full, out)
      }
      continue
    }
    if (/\.tsx?$/.test(entry.name)) out.push(full)
  }
  return out
}

const sources = collectSources(featuresDir)

describe('分组范围编辑器只有一份实现', () => {
  test('只有状态机 hook 调用矩阵保存端点', () => {
    const callers = sources
      .filter((file) => {
        const text = readFileSync(file, 'utf8')
        // 端点自己的定义（`export function qyGmSaveMatrix(`）不算调用方。
        return /(?:^|[^\w.])qyGmSaveMatrix\(/m.test(
          text.replaceAll('export function qyGmSaveMatrix(', '')
        )
      })
      .map((file) => relative(featuresDir, file).replaceAll('\\', '/'))

    assert.deepEqual(
      callers,
      ['qy/pages/admin-group-matrix/lib/use-editor.ts'],
      `第二处保存调用 = 第二份状态机 = 两个外壳的预览闸门可能松紧不一：${callers.join(', ')}`
    )
  })
})

describe('用户分组表：弹窗配置，不再跳转（需求 4）', () => {
  const section = readFileSync(
    join(featuresDir, 'system-settings', 'groups', 'user-groups-section.tsx'),
    'utf8'
  )

  test('操作列打开的是弹窗', () => {
    assert.ok(
      section.includes('QyUgScopeDialog'),
      '「配置可用模型分组」必须就地开弹窗'
    )
  })

  test('不再存在通往 group-matrix 那一页的行内跳转', () => {
    assert.ok(
      !section.includes("section: 'group-matrix'"),
      '跳走会卸载本 section，运营正在编辑的充值折扣草稿随之丢失'
    )
  })

  test('【可用模型分组】列列的是名字，不是个数', () => {
    // 项目方原话：「直接把模型分组名称显示上去，如：免费の渠道、浅夜の梦专属
    // 号池」。退回成个数不会有任何测试变红，只会让那一列重新变得无法回答
    // 「这一档人到底能用到哪几个池子」。
    assert.ok(
      section.includes('qyUgGrantedModelGroups'),
      '名单必须与矩阵页 / 弹窗同源，不能在这里另算一份'
    )
  })
})

describe('范围表单不许被矩阵保存冲掉（需求 4 的弹窗形态）', () => {
  const form = readFileSync(
    join(
      featuresDir,
      'qy',
      'pages',
      'admin-group-matrix',
      'components',
      'scope-sheet.tsx'
    ),
    'utf8'
  )

  /*
    配置弹窗把范围表单与矩阵差异条放在同一屏，且那里的 `open` 是硬编码
    字面量 `true`。矩阵保存成功后 `applyServerState` 会 setQueryData 写入一个
    全新的响应对象 ⇒ `userGroups.find(...)` 是新的对象身份。重置 effect 的依赖
    里放这个对象，就等于「按一次矩阵保存 = 把运营刚填好的 enforce 与备注静默
    清回服务端旧值」，而屏幕上只有一句绿色的「已保存」。

    判据取依赖数组本身：它是这条路径唯一的开关，而症状（设置消失）不会有
    任何报错、任何测试、任何日志。
  */
  test('重置只由「打开」与「换了一个分组」触发，不由服务端对象身份触发', () => {
    const deps = form.match(/\}, \[props\.open[^\]]*\]\)/)
    assert.ok(deps, '没有找到范围表单的重置 effect 依赖数组')
    assert.equal(
      deps[0].includes('userGroupName'),
      true,
      '依赖必须是分组**名字**：换了一个人才重置'
    )
    assert.equal(
      /userGroup(?!Name)/.test(deps[0]),
      false,
      '依赖里放 userGroup 这个对象 = 每次矩阵保存都把范围表单清回旧值'
    )
  })
})
