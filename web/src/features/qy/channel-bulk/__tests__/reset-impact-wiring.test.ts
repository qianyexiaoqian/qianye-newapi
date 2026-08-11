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

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

/**
 * 「批量清空已用额度」这条不可逆动作的接线锁。
 *
 * # 为什么判据落在源码形状上
 *
 * 这一整条链路上的每一处失败都**没有任何运行时信号**：类型全对、typecheck
 * exit 0、接口照常回 200。少传一个 `used_quota`，确认框上那笔金额就变成
 * `$0.00`；把多选控件从列定义里摘掉，工具条永远不出现；把确认框换成普通
 * 弹窗，不可逆动作就只剩一次点击。三者的共同表现都是"看起来一切正常"，
 * 只有真的去点那一屏才发现，而那时数据已经清掉了。
 *
 * 渲染级测试在这里帮不上忙：要触发这一屏得先把 react-table + 分页 URL 状态 +
 * react-query + i18n 全套装起来，而那样的测试真正断言的是脚手架，不是接线。
 */
const here = dirname(fileURLToPath(import.meta.url))
const bulkDir = join(here, '..')
const channelsDir = join(here, '../../../channels')

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

describe('渠道列表的多选控件', () => {
  test('列定义里有全选表头 + 逐行勾选框', () => {
    const source = codeOf(
      read(join(channelsDir, 'components/channels-columns.tsx'))
    )
    assert.ok(
      source.includes("id: 'select'"),
      '选择列没了 —— 没有它就无从多选，批量操作工具条永远不会出现'
    )
    assert.ok(
      source.includes('toggleAllPageRowsSelected'),
      '表头的全选/反选没了：全选是本页勾满、再点一次全部取消，这一对动作由它实现'
    )
    assert.ok(source.includes('row.toggleSelected'), '逐行勾选没了')
  })

  test('翻页 / 改筛选之后清空选中态', () => {
    const source = codeOf(
      read(join(channelsDir, 'components/channels-table.tsx'))
    )
    const effects = source
      .split('useEffect(')
      .filter((chunk) => chunk.includes('resetRowSelection()'))
    const crossPage = effects.find(
      (chunk) =>
        chunk.includes('pagination.pageIndex') &&
        chunk.includes('columnFilters') &&
        chunk.includes('globalFilter')
    )
    assert.ok(
      crossPage,
      '跨页选中态没有被清：这张表是 manualPagination，' +
        '选中态按 row id 保留而 data 里只有当前页 —— ' +
        '第 1 页勾的 3 个翻页后既不显示也不会被执行，翻回去又变成勾着的。' +
        '不可逆动作的确认框会因此复述一个与实际执行范围不同的条数与金额'
    )
  })
})

describe('清空已用额度的二次确认', () => {
  // 确认框、影响面复述、提交都在 reset-usage.tsx —— 工具条、列表头、行内
  // 三个入口共用这一份。多一份拷贝就是多一处会各自漂移的不可逆闸门。
  const bulk = codeOf(read(join(bulkDir, 'reset-usage.tsx')))

  test('选中的渠道带着 used_quota 传进来', () => {
    const wiring = codeOf(
      read(join(channelsDir, 'components/data-table-bulk-actions.tsx'))
    )
    assert.ok(
      /used_quota:\s*\(row\.original as Channel\)\.used_quota/.test(wiring),
      '选中集不再携带 used_quota —— 确认框上的合计金额会静默变成 0，' +
        '而它是管理员按下不可逆按钮之前唯一能看到的量级'
    )
    assert.ok(
      codeOf(read(join(bulkDir, 'channel-name-list.tsx'))).includes(
        'used_quota: number'
      ),
      '批量动作的入参不再要求 used_quota，接线断了也不会有类型错误'
    )
  })

  test('确认框同屏显示渠道数与合计已用额度', () => {
    assert.ok(
      bulk.includes('<ResetImpact'),
      '影响面复述块没有渲染：只有条数的确认框回答不了"这一次要抹掉多少钱"'
    )
    const impact = bulk.slice(bulk.indexOf('function ResetImpact('))
    assert.match(
      impact,
      /qy_chops_reset_channel_count/,
      '渠道数没有出现在确认框里'
    )
    assert.match(
      impact,
      /qy_chops_reset_total_used/,
      '合计已用额度没有出现在确认框里'
    )
    assert.match(
      impact,
      /<QyAmountText/,
      '金额没有走 QyAmountText —— 直接摊 quota 整数等于让管理员自己心算汇率，' +
        '而列表页、钱包页、日志页显示的都是换算后的站内余额'
    )
  })

  test('确认框仍然是不可逆档（醒目警示 + 强制勾选）', () => {
    const resetDialog = bulk.slice(bulk.indexOf('<QyConfirmDialog'))
    const dialogEnd = resetDialog.indexOf('/>')
    assert.ok(dialogEnd > 0)
    assert.ok(
      resetDialog.slice(0, dialogEnd).includes('irreversible'),
      '重置退回成普通确认框：清零没有补算路径，必须保留强制勾选那道闸门'
    )
  })

  test('成功提示说的是后端真正抹掉的金额，不是确认框里的估算', () => {
    assert.ok(
      bulk.includes('result.cleared_used_quota'),
      '成功提示没有用后端回来的 cleared_used_quota —— ' +
        '确认框里的合计算自可能已过期几分钟的列表缓存，把它当结果复述等于说假话'
    )
  })
})

describe('新增文案两份语言包都要有', () => {
  const enKeys = en as Record<string, string>
  const zhKeys = zh as Record<string, string>

  test('确认框与提示用到的键都在', () => {
    // i18next 找不到键时直接把键名渲染出来，页面上会出现一行 qy_chops_…，
    // 而它只在真的打开那一屏时才看得见。
    for (const key of [
      'qy_chops_reset_selected_count',
      'qy_chops_reset_channel_count',
      'qy_chops_reset_total_used',
      'qy_chops_reset_already_zero',
      'qy_chops_reset_used_quota_not_selected',
      'qy_chops_toast_reset_ok',
    ]) {
      assert.ok(zhKeys[key], `zh 缺 ${key}`)
      assert.ok(enKeys[key], `en 缺 ${key}`)
    }
  })

  test('成功提示的两个插值占位符都在', () => {
    for (const pack of [zhKeys, enKeys]) {
      const text = pack['qy_chops_toast_reset_ok']
      assert.ok(text.includes('{{count}}'), `缺 count 占位符: ${text}`)
      assert.ok(text.includes('{{amount}}'), `缺 amount 占位符: ${text}`)
    }
  })
})
