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
 * 清零直达入口的**位置锁**。
 *
 * # 这条锁挡的是什么
 *
 * 功能早就做完了（后端带权限、写审计、实测可用，前端有确认框与逐条结果），
 * 项目方却连着三次说"没有清零功能"—— 三次都是对着列表里「已使用 / 剩余」
 * 那一列问的。真正的缺陷是入口的**位置**：它在「批量操作」开关 → 行首勾选框
 * → 底部工具条这条四步路的尽头，而那三步都在离那一列很远的地方。
 *
 * 位置这件事没有任何运行时信号：把入口挪回工具条里，类型全对、typecheck
 * exit 0、点进去功能也完全正常 —— 只是那一列上再也没有它，于是又回到
 * "做了但找不到"。所以判据落在接线的形状上：
 *
 *   1. 两个入口确实挂在「已使用 / 剩余」这一列上（表头 + 行内）；
 *   2. 它们**不依赖「批量操作」开关**（`enableSelection` / `batchMode`），
 *      也不长在批量工具条那个文件里；
 *   3. 原有的工具条入口**保留**（这次是加一条更短的路，不是搬家）；
 *   4. 三个入口共用同一个确认框与同一个后端接口，没有第二份实现、
 *      也没有第二个端点。
 *
 * 「点了之后会发生什么」由 reset-direct-entry-flow.test.tsx 真的点一遍来守。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

import { readJsxTree } from '../../__tests__/jsx-tree'

const here = dirname(fileURLToPath(import.meta.url))
const bulkDir = join(here, '..')
const channelsDir = join(here, '../../../channels')

const COLUMNS = join(channelsDir, 'components/channels-columns.tsx')
const TOOLBAR = join(channelsDir, 'components/data-table-bulk-actions.tsx')
const PAGE = join(channelsDir, 'components/channels-table.tsx')

const read = (path: string) => readFileSync(path, 'utf8')

/** 去掉注释行：注释里出现的同名字样不算数。 */
const codeOf = (source: string) =>
  source
    .split('\n')
    .filter((line) => {
      const trimmed = line.trimStart()
      return (
        !trimmed.startsWith('//') &&
        !trimmed.startsWith('*') &&
        !trimmed.startsWith('/*')
      )
    })
    .join('\n')

describe('清零入口长在「已使用 / 剩余」这一列上', () => {
  const columns = readJsxTree(COLUMNS)

  test('列表头有一个直达入口', () => {
    assert.equal(
      columns.occurrences('QyChannelResetUsageColumnAction').length,
      1,
      '表头的直达入口不见了 —— 项目方三次都在看这一列的表头与单元格'
    )
    const code = codeOf(columns.source)
    assert.match(
      code,
      /header:\s*\(\{\s*table,\s*column\s*\}\)\s*=>\s*\(\s*<BalanceColumnHeader/,
      '「已使用 / 剩余」列的表头不再渲染带入口的表头组件'
    )
  })

  test('表头组件自己渲染列名，排序与隐藏列不会被换掉', () => {
    const code = codeOf(columns.source)
    const header = code.slice(
      code.indexOf('function BalanceColumnHeader'),
      code.indexOf('export function resettableChannelsOf')
    )
    assert.ok(header.length > 0, '表头组件的结构变了，这条锁需要重新校准')
    assert.match(
      header,
      /<DataTableColumnHeader\s+column=\{column\}/,
      '表头把列名写成了纯文本 —— 排序（升序 / 降序）与「隐藏此列」都长在' +
        'DataTableColumnHeader 上，自己拼一个 <span> 会把它们一起弄丢'
    )
  })

  test('每一行的已用额度旁边有一个单渠道入口', () => {
    const occurrences = columns.occurrences('QyChannelResetUsageRowAction')
    assert.equal(occurrences.length, 1, '行内的单渠道清零入口不见了')
    assert.ok(
      occurrences[0].length > 0,
      '行内入口没有挂在任何 JSX 里 —— 它不会被渲染'
    )
  })

  test('两个入口都不依赖「批量操作」开关', () => {
    const code = codeOf(columns.source)
    // 选择列（勾选框那一列）是唯一按 enableSelection 条件渲染的东西。
    // 清零入口一旦落进这个条件块，就又回到"先开开关才够得着"。
    const gated = code.slice(
      code.indexOf('...(enableSelection'),
      code.indexOf('// Balance column')
    )
    assert.ok(gated.length > 0, '列定义的结构变了，这条锁需要重新校准')
    assert.equal(
      /QyChannelResetUsage/.test(gated),
      false,
      '清零入口被塞进了 enableSelection 的条件块 —— ' +
        '「批量操作」开关关着时它就消失了，而那正是这次要解决的问题'
    )
    // 表头组件自己也不许去读开关状态。
    const header = code.slice(
      code.indexOf('function BalanceColumnHeader'),
      code.indexOf('function BalanceCell')
    )
    assert.ok(header.length > 0)
    assert.equal(
      /enableSelection|batchMode|getSelectedRowModel|getFilteredSelectedRowModel/.test(
        header
      ),
      false,
      '表头入口去读了选中态/批量开关：它必须自带勾选，与外面那个开关无关'
    )
  })

  /*
   * 表头只在**表格视图**里存在，而这一页 `enableCardView` 且没传
   * `defaultViewMode` —— DataTablePage 的默认是卡片视图，首次进页面连一个 <th>
   * 都没有。多渠道入口因此必须在工具条上再有一份（工具条两种视图下都渲染）。
   *
   * 它在默认视图下真的显示、点开是同一个挑选面板，由
   * reset-entry-render.test.tsx 真渲染一遍来守；这里守的是**渠道页确实把它接上了**
   * —— 那个探针自己拼 preActions，接线断了它是不会红的。
   */
  test('工具条上有一份不依赖视图模式的多渠道入口', () => {
    const page = codeOf(read(PAGE))
    assert.ok(
      page.includes('QyChannelResetUsageToolbarAction'),
      '渠道页没有把工具条上的多渠道入口接上 —— 默认是卡片视图，' +
        '那里根本没有表头，表头上那个入口一个像素都不存在'
    )
    const preActions = page.slice(
      page.indexOf('preActions:'),
      page.indexOf('getRowClassName')
    )
    assert.ok(preActions.length > 0, '工具条插槽的结构变了，这条锁需要重新校准')
    assert.ok(
      preActions.includes('QyChannelResetUsageToolbarAction'),
      '多渠道入口被挪出了工具条插槽'
    )
    assert.equal(
      /defaultViewMode=/.test(page),
      false,
      '这一页开始指定默认视图了 —— reset-entry-render.test.tsx 里' +
        '"默认是卡片视图"那条前提需要重新校准'
    )
  })

  test('入口没有长在批量工具条里', () => {
    assert.equal(
      /QyChannelResetUsage(Row|Column)Action/.test(read(TOOLBAR)),
      false,
      '直达入口被挪回了批量工具条 —— 那个文件只在「批量操作」开着且有选中行时' +
        '才被渲染，等于入口又消失了'
    )
  })
})

describe('原有的批量工具条入口保留', () => {
  const toolbar = readJsxTree(join(bulkDir, 'index.tsx'))

  test('工具条上仍然有「重置统计」按钮', () => {
    assert.ok(
      codeOf(toolbar.source).includes('qy_chops_reset_action'),
      '工具条上的重置按钮被删了 —— 这次是加一条更短的路，不是搬家，' +
        '已经习惯那条路的人不该被打断'
    )
  })

  test('工具条走的是同一个确认框，不是自己那一份', () => {
    assert.equal(
      toolbar.occurrences('QyChannelResetUsageDialog').length,
      1,
      '工具条没有复用共享的确认框'
    )
    const code = codeOf(toolbar.source)
    assert.equal(
      code.includes('qyBatchResetChannelUsage'),
      false,
      '工具条又自己发了一次重置请求：确认框与提交必须只有一份实现'
    )
  })
})

describe('三个入口共用一份实现', () => {
  const resetUsage = readJsxTree(join(bulkDir, 'reset-usage.tsx'))
  const code = codeOf(resetUsage.source)

  test('直达入口渲染的都是共享的确认框', () => {
    // 两处：行内单渠道一处，多渠道的挑选面板一处（表头与工具条那两个入口
    // 复用同一个 ChannelResetUsagePickEntry，所以不是三处）。
    assert.equal(
      resetUsage.occurrences('QyChannelResetUsageDialog').length,
      2,
      '直达入口没有各自渲染同一个确认框 —— ' +
        '要么少了一个入口，要么某一个自己另开了一份'
    )
  })

  test('表头与工具条那两个入口是同一份实现', () => {
    for (const entry of [
      'QyChannelResetUsageColumnAction',
      'QyChannelResetUsageToolbarAction',
    ]) {
      const rest = code.slice(
        code.indexOf(`export function ${entry}`) + entry.length
      )
      const nextDecl = ['\nexport function ', '\nfunction ']
        .map((mark) => rest.indexOf(mark))
        .filter((index) => index >= 0)
      const body = rest.slice(
        0,
        nextDecl.length > 0 ? Math.min(...nextDecl) : rest.length
      )
      assert.ok(body.length > 0, `找不到 ${entry} 的函数体`)
      assert.match(
        body,
        /<ChannelResetUsagePickEntry/,
        `${entry} 自己另写了一份挑选面板 —— 两个入口必须是同一份实现，` +
          '否则修一处漏一处'
      )
    }
  })

  test('提交只有一个出口，且仍然是 batch-reset-usage', () => {
    const calls = code.match(/qyBatchResetChannelUsage\(/g) ?? []
    assert.equal(
      calls.length,
      1,
      `重置请求在这个文件里被发起了 ${calls.length} 次：单渠道路径必须复用` +
        '同一个批量接口（ids 里只放一个），不要另写一个端点'
    )
    assert.equal(
      /\/admin\/channels\//.test(code),
      false,
      '这里直接拼了接口路径：地址只应该在 api.ts 里出现一次'
    )
  })

  test('确认框是不可逆档（醒目警示 + 强制勾选）', () => {
    const dialog = code.slice(code.indexOf('<QyConfirmDialog'))
    assert.ok(
      dialog.slice(0, dialog.indexOf('/>')).includes('irreversible'),
      '入口变短了，不可逆闸门却被一起省掉了'
    )
  })

  test('不可逆警示说的不是"资金会立即变动"', () => {
    // 默认那句正文是资金口径的（qy 这边多数不可逆动作确实在动钱）。
    // 清 used_quota 不动任何人的余额，照抄那句会与同屏的 scope hint
    //（"上游的真实用量不会被改动"）直接打架，读起来像是要扣钱或退钱。
    assert.ok(
      code.includes('qy_chops_reset_irreversible_desc'),
      '确认框用回了资金口径的不可逆正文'
    )
  })

  test('确认框说清楚清的是「已使用」而不是「剩余」', () => {
    assert.ok(
      code.includes('qy_chops_reset_scope_hint'),
      '那一列里的两个数长得一样、含义完全不同（used_quota vs balance）。' +
        '不说清楚，用户会以为点一下两个都清'
    )
  })
})

describe('新增文案两份语言包都要有', () => {
  const enKeys = en as Record<string, string>
  const zhKeys = zh as Record<string, string>

  test('直达入口用到的键都在', () => {
    // i18next 找不到键时直接把键名渲染出来，页面上会出现一行 qy_chops_…，
    // 而它只在真的打开那一屏时才看得见。
    for (const key of [
      'qy_chops_reset_desc_one',
      'qy_chops_reset_scope_hint',
      'qy_chops_reset_one_action',
      'qy_chops_reset_pick_title',
      'qy_chops_reset_pick_desc',
      'qy_chops_reset_pick_all',
      'qy_chops_reset_pick_selected',
      'qy_chops_reset_pick_next',
      'qy_chops_reset_pick_empty',
      'qy_chops_reset_irreversible_desc',
    ]) {
      assert.ok(zhKeys[key], `zh 缺 ${key}`)
      assert.ok(enKeys[key], `en 缺 ${key}`)
    }
  })

  test('带插值的三条都保留了占位符', () => {
    for (const pack of [zhKeys, enKeys]) {
      assert.ok(pack['qy_chops_reset_desc_one'].includes('{{name}}'))
      assert.ok(pack['qy_chops_reset_pick_selected'].includes('{{count}}'))
      assert.ok(pack['qy_chops_reset_pick_next'].includes('{{count}}'))
    }
  })
})
