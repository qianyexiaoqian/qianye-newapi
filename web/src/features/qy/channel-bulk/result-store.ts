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
import { create } from 'zustand'

import type { QyChannelBatchResult } from './api'

/**
 * 批次报告的状态，**刻意放在组件树之外**。
 *
 * # 为什么不能是 QyChannelBulkActions 里的一个 useState
 *
 * 那些按钮渲染在上游 `BulkActionsToolbar` 的 children 里，而那个工具条在选中数
 * 归零时直接 `return null`（src/components/data-table/toolbar/bulk-actions.tsx）。
 * 批次跑完要做两件事：清空选中态、摊开失败明细。只要报告的状态住在工具条内部，
 * 这两件事就互斥 —— 清空选中的那一刻整棵子树被卸载，随后的 setState 落在一个
 * 已经不存在的组件上被丢弃，屏幕上只剩一句"3 个渠道未处理成功，请查看明细"，
 * 而那份明细没有任何入口。
 *
 * 「批次跑完不清空选中态」也不成立：删除成功的行会从表里消失，`not_found` 的那些
 * 同样消失，于是"全部失败但都是 not_found"这一档照样把选中数归零。
 *
 * 所以报告的归属只有一个正确答案：它不属于工具条，它属于这一页。状态放到组件树
 * 外面之后，弹窗由 `QyChannelBulkResultOutlet` 渲染在工具条**之外**（上游自己的
 * 打标签/删除弹窗也放在 `</BulkActionsToolbar>` 之后，正是为了躲开同一次卸载）。
 *
 * # 为什么是全局单例而不是 context
 *
 * 一个页面同时只可能有一个批次报告在屏幕上。用 context 就要在上游文件里加一层
 * Provider 包裹整个返回值，那是更大的一处上游改动，换来的只是一份用不到的隔离。
 */
type QyChannelBatchReport = {
  result: QyChannelBatchResult
  title: string
}

type QyChannelBatchResultState = {
  report: QyChannelBatchReport | null
  open: (report: QyChannelBatchReport) => void
  close: () => void
}

export const useQyChannelBatchResultStore = create<QyChannelBatchResultState>(
  (set) => ({
    report: null,
    open: (report) => set({ report }),
    close: () => set({ report: null }),
  })
)
