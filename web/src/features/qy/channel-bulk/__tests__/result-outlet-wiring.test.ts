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

import { useQyChannelBatchResultStore } from '../result-store'

/**
 * 批次报告的挂载位置锁。
 *
 * # 这条锁挡的是什么
 *
 * 上游 `BulkActionsToolbar` 在选中数归零时直接 `return null`
 * （src/components/data-table/toolbar/bulk-actions.tsx），而批次收尾要做的第一件事
 * 就是清空选中态。报告弹窗一旦渲染在工具条的 children 里，这两件事就互斥：
 * 清空选中的那一刻整棵子树被卸载，报告连挂载的机会都没有，屏幕上只剩一句
 * "N 个渠道未处理成功，请查看明细"，而那份明细没有任何入口 —— 那正是这个模块
 * 存在的唯一理由。
 *
 * 这个失败**没有任何运行时信号**：不报错、不白屏、类型也全对，只是那一屏
 * 永远不出现。所以判据只能落在接线的形状上。
 */
const here = dirname(fileURLToPath(import.meta.url))
const bulkDir = join(here, '..')
const upstreamToolbar = join(
  here,
  '../../../channels/components/data-table-bulk-actions.tsx'
)
const upstreamTable = join(
  here,
  '../../../channels/components/channels-table.tsx'
)

const read = (path: string) => readFileSync(path, 'utf8')

describe('批次报告必须挂在工具条之外', () => {
  test('QyChannelBulkActions 自己不渲染报告弹窗', () => {
    const source = read(join(bulkDir, 'index.tsx'))
    const actionsBody = source.slice(
      source.indexOf('export function QyChannelBulkActions')
    )
    assert.equal(
      actionsBody.includes('<QyChannelBatchResultDialog'),
      false,
      '报告弹窗回到了 QyChannelBulkActions 里 —— 它随工具条一起卸载，永远不会出现'
    )
  })

  test('Outlet 挂在页面级，且全页只有这一处', () => {
    const table = read(upstreamTable)
    const outlet = table.indexOf('<QyChannelBulkResultOutlet')
    const bulkSlot = table.indexOf('bulkActions=')
    assert.ok(outlet > 0, 'Outlet 没有被渲染，失败明细将永远打不开')
    assert.ok(
      outlet > bulkSlot,
      'Outlet 落回了 bulkActions 插槽里 —— 「批量操作」开关关着时那个插槽是 null，' +
        '而清零的两个直达入口正是在开关关着时也能提交的'
    )
    assert.equal(
      read(upstreamToolbar).includes('<QyChannelBulkResultOutlet'),
      false,
      '工具条里又挂了一个 Outlet：两个实例会同时渲染同一份报告'
    )
  })

  test('finish() 先开报告、再走收尾回调', () => {
    // 三个入口共用的收尾。注释里出现的同名字样不算数：这条锁看的是
    // 真正会执行的那几行。
    const finish = read(join(bulkDir, 'batch-finish.ts'))
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
    const opened = finish.indexOf('openReport(')
    const done = finish.indexOf('onDone()')
    assert.ok(opened > 0 && done > 0)
    assert.ok(
      opened < done,
      'onDone() 走在 openReport 前面 —— 它会清空选中态（卸载工具条）或关掉' +
        '直达入口的弹窗，之后的写入落在一个已经不存在的实例上'
    )
  })
})

describe('报告 store 的开合', () => {
  test('open 之后拿得到报告，close 之后回到 null', () => {
    const store = useQyChannelBatchResultStore
    store.getState().close()
    assert.equal(store.getState().report, null)

    store.getState().open({
      title: '批量删除渠道',
      result: {
        total: 3,
        succeeded: 2,
        skipped: 0,
        failed: 1,
        items: [
          {
            id: 7,
            name: 'alpha',
            outcome: 'failed',
            code: 'qy_chops_item_db_error',
          },
        ],
      },
    })
    assert.equal(store.getState().report?.result.failed, 1)
    assert.equal(store.getState().report?.title, '批量删除渠道')

    store.getState().close()
    assert.equal(store.getState().report, null)
  })
})
