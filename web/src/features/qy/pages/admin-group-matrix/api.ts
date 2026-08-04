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
import { queryOptions } from '@tanstack/react-query'

import { qyGet, qyPost, qyPut } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type {
  QyGmCellChange,
  QyGmMatrixResponse,
  QyGmOrphansResponse,
  QyGmPreviewResponse,
  QyGmSaveRequest,
  QyGmSaveResponse,
  QyGmScopeRequest,
} from './types'

/**
 * 用户分组 × 模型分组 矩阵的管理端取数。
 *
 * 路径集中在这一个文件，与 `qianye/modules/groupmatrix` 的
 * `RegisterAdminRoutes`（路 B）以及 `preview.go` / `orphans.go`（路 C）
 * 一一对应。
 */

const QY_GM_BASE = '/admin/group-matrix'

/**
 * 矩阵本体。
 *
 * `staleTime: 0`：这一页每个数字都决定「谁能用哪批渠道、按什么价」，
 * 而两个管理员同屏编辑是设计里明确预期的场景。缓存住一份旧矩阵的直接后果是
 * 运营基于过期状态构造草稿，保存时吃一个 409 却看不出哪里对不上。
 */
export function qyGmMatrixQuery() {
  return queryOptions({
    queryKey: qyKeys.adminGroupMatrixData(),
    queryFn: () => qyGet<QyGmMatrixResponse>(QY_GM_BASE),
    staleTime: 0,
  })
}

/**
 * 保存。**返回的是服务端强制回读的真实状态**，不是请求的回声。
 *
 * 倍率落上游 `options`、清单落扩展库，两库不原子。部分失败时前端必须立刻显示
 * 「倍率已生效、清单未生效」这种半成状态，而乐观本地渲染画出来的是一个从未
 * 存在过的成功画面 —— 运营会据此以为改完了然后走人。
 */
export function qyGmSaveMatrix(body: QyGmSaveRequest) {
  return qyPut<QyGmSaveResponse>(QY_GM_BASE, body)
}

/**
 * 接管开关 / 模式 / auto 注入。
 *
 * 用户分组名进 URL 段：它是运营自由输入的字符串（站里有 `浅夜の自己人`
 * 这类名字），必须 `encodeURIComponent`，否则含 `/` 或 `#` 的名字会把请求
 * 打到另一条路由上并静默改错一行。
 */
export function qyGmSaveScope(userGroup: string, body: QyGmScopeRequest) {
  return qyPut<QyGmSaveResponse>(
    `${QY_GM_BASE}/scope/${encodeURIComponent(userGroup)}`,
    body
  )
}

/**
 * 影响面预览。**纯只读**，请求体是尚未落库的草稿。
 *
 * 走 POST 而不是 GET 是因为草稿是一个可能上百项的动作列表，塞进 query string
 * 会撞长度上限；且这里刻意不缓存 —— 预览的价值全在「此刻」，一份十秒前的
 * 影响面报告会让运营基于已经变了的令牌分布做出切 enforce 的决定。
 */
export function qyGmPreview(cells: QyGmCellChange[]) {
  return qyPost<QyGmPreviewResponse>(`${QY_GM_BASE}/preview`, { cells })
}

/**
 * 切 `enforce` 专用的影响面预览：**只评估那一个用户分组**。
 *
 * 必须与服务端 `previewDigest(userGroup)` 用同一个取值范围，否则两侧算出的
 * `impact_hash` 永远不可能相等 —— 只要站里有两个及以上被接管的用户分组，
 * 通用预览（不带 `user_groups`，服务端铺开全部 scope 行）与切换时的单分组重算
 * 就必然对不上，`enforce` 会被 409「影响面已经变化」永久锁死，而运营去查审计、
 * 查令牌都查不到原因。灰度的推荐顺序恰好是「先全部接管成 shadow，再逐个切
 * enforce」，也就是说这条路径在最常见的用法下不可达。
 *
 * `cells` 恒为空：切 enforce 用的是**已落库**的清单，草稿必须先保存。
 */
export function qyGmPreviewForEnforce(userGroup: string) {
  return qyPost<QyGmPreviewResponse>(`${QY_GM_BASE}/preview`, {
    user_groups: [userGroup],
    cells: [],
  })
}

/**
 * L0 孤儿基线。**常驻**，与任何草稿无关。
 *
 * `staleTime` 给 60 秒：这是一份历史欠账的盘点，秒级新鲜度没有意义，
 * 而它要跑好几条全表聚合，跟着矩阵一起 refetch 只是白烧数据库。
 */
export function qyGmOrphansQuery() {
  return queryOptions({
    queryKey: qyKeys.adminGroupMatrixOrphans(),
    queryFn: () => qyGet<QyGmOrphansResponse>(`${QY_GM_BASE}/orphans`),
    staleTime: 60_000,
  })
}

/**
 * 单条修复：把一个孤儿令牌的分组置空。
 *
 * 置空之后使用分组恒等于属主的用户分组（上游 `middleware/auth.go` 的
 * `if tokenGroup != ""` 会整段跳过检查），令牌立刻恢复可用，且不改变该用户
 * 的可用范围。这是唯一一个既安全又不替用户猜意图的修复动作 ——
 * 「改成某个指定分组」需要替他做选择，**不提供**；批量同理。
 */
export function qyGmRepairToken(tokenId: number) {
  return qyPost<{ token_id: number }>(`${QY_GM_BASE}/repair-token`, {
    token_id: tokenId,
  })
}
