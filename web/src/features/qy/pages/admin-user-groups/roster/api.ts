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

import { qyDelete, qyGet, qyPost, qyPut } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import type {
  QyUgrCreateRequest,
  QyUgrCreateResult,
  QyUgrDeleteRequest,
  QyUgrImpact,
  QyUgrListResponse,
  QyUgrMigrateRequest,
  QyUgrMigrateResult,
  QyUgrRenameRequest,
  QyUgrRewriteResult,
  QyUgrUpdateRequest,
  QyUgrUpdateResult,
} from './types'

const QY_UGR_BASE = '/admin/group-namespace/user-groups'

/**
 * 分组名进 URL 段时**必须** `encodeURIComponent`。
 *
 * 站里有「浅夜の梦专属号池」这类名字,而分组名是运营自由输入的字符串:
 * 含 `/` 或 `#` 的名字不编码会把请求打到另一条路由上 —— 在删除这条路径上,
 * 那意味着删掉的是另一个分组,而返回值看起来一切正常。
 */
function path(name: string, suffix = '') {
  return `${QY_UGR_BASE}/${encodeURIComponent(name)}${suffix}`
}

/**
 * 登记表。
 *
 * `staleTime: 0`:每一行都带着在册人数与空分组令牌数,而它们正是删除闸门的
 * 判据(后端还会用请求体里回传的人数做一次一致性复核,对不上直接 409)。
 * 缓存一份旧清单的后果是运营对着一个过期的数字按下删除,然后拿到一个
 * 他看不懂的冲突提示。
 */
export function qyUgrListQuery() {
  return queryOptions({
    queryKey: qyKeys.adminUserGroupRoster(),
    queryFn: () => qyGet<QyUgrListResponse>(QY_UGR_BASE),
    staleTime: 0,
  })
}

/**
 * 删除影响面。**与删除端点走同一段服务端探测**,不是前端拼出来的估算。
 *
 * `target` 非空时后端顺带算上迁移前后的可用清单与倍率差 —— 这一份必须与
 * 删除时真正生效的判据同源(`service.GetUserUsableGroups` +
 * `ratio_setting.InspectGroupRatio`),否则会出现"预览说不变、迁完之后全 403"。
 */
export function qyUgrImpactQuery(name: string | null, target: string) {
  return queryOptions({
    queryKey: qyKeys.adminUserGroupImpact(name ?? '', target),
    queryFn: () =>
      qyGet<QyUgrImpact>(
        `${path(name ?? '', '/impact')}${
          target === '' ? '' : `?target=${encodeURIComponent(target)}`
        }`
      ),
    enabled: name != null && name !== '',
    staleTime: 0,
    /*
      queryKey 含迁移目标，所以**每换一次目标就是一把新键**。没有这一行的话
      新键无缓存 ⇒ data 变 undefined ⇒ 弹窗正文（四个数字、迁移下拉、
      迁移后差异、连带清理清单）整块塌成一行「Loading...」，弹窗高度塌掉再
      弹回、滚动位置归零。而在两个候选目标之间比较「失去 / 获得 / 改价」
      三堆，正是这个弹窗存在的唯一理由 —— 每切一次就清一次屏，等于比不了。

      保留上一份的代价是"屏幕上这一份可能是上一个目标的"，因此调用方必须同时
      渲染一个正在刷新的标记，并在刷新期间禁用删除键。数据正确性另有兜底：
      提交时的 expect_users 会在人数漂移时 409。
    */
    placeholderData: (previous: QyUgrImpact | undefined) => previous,
  })
}

export function qyUgrCreate(body: QyUgrCreateRequest) {
  return qyPost<QyUgrCreateResult>(QY_UGR_BASE, body)
}

/**
 * 改展示属性 + 充值倍率。
 *
 * 没有登记行时后端**自动补一行**再改（`backfilled`）：管理端那张表的清单是
 * 「观测值 ∪ 登记表」，表上一定会出现只在观测里的名字，对着它按编辑却收到
 * 「还没有登记」，运营唯一能做的事是去别处找一个叫「回填」的按钮 ——
 * 而那个按钮解决的是一个他不需要知道的内部区分。
 */
export function qyUgrUpdate(name: string, body: QyUgrUpdateRequest) {
  return qyPut<QyUgrUpdateResult>(path(name), body)
}

/**
 * 改名。返回值里的 `partial` 非空表示两库只写成了一半。
 *
 * 调用方**必须**把它原样渲染出来并留在屏幕上:报成功然后走人是这套跨库设计
 * 最坏的失败方式 —— 线上停在中间态,而没有任何人知道。
 */
export function qyUgrRename(name: string, body: QyUgrRenameRequest) {
  return qyPost<QyUgrRewriteResult>(path(name, '/rename'), body)
}

/** 删除并迁移。`partial` 的处理同 {@link qyUgrRename}。 */
export function qyUgrDelete(name: string, body: QyUgrDeleteRequest) {
  return qyDelete<QyUgrRewriteResult>(path(name), body)
}

/**
 * 一键迁移 —— 把这一档人整体挪到另一档，**源分组留着**。
 *
 * ── 为什么它不是删除的一个选项 ──
 *
 * 迁移此前只作为删除的副产品存在，而删除对 `default` 是硬拒的（它是
 * `users.group` 这一列的数据库默认值）。于是运营真正想做的那件事（「把这 707 个
 * 人挪走」）在界面上无路可走：唯一的入口被一道与他的目的毫无关系的闸门挡着。
 *
 * 服务端走的是与删除**同一份**迁移实现，只是不再有"清掉源分组配置"那一步。
 */
export function qyUgrMigrate(name: string, body: QyUgrMigrateRequest) {
  return qyPost<QyUgrMigrateResult>(path(name, '/migrate'), body)
}
