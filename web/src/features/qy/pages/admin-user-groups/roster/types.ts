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
/**
 * 用户分组**登记表**(`qy_user_groups`)的新建 / 改名 / 删除。
 *
 * 与 `../types.ts`(矩阵页的 `QyGm*`)的分工:那一份描述「这一档人能选哪些模型
 * 分组、按什么倍率」,这一份描述「站上有哪几档人」。两者的行轴看起来一样,
 * 但来源不同 —— 矩阵页的行轴是 `users.group ∪ scopes ∪ GroupRatio 键`(观测出来的),
 * 登记表是运营维护出来的,而**一个刚建出来的分组还没有人**,只存在于后者。
 */

/** 残留的四种处置,与后端 `groupns.Residue*` 常量同值。 */
export type QyUgrDisposition = 'block' | 'clean' | 'keep' | 'rewrite'

/** 一处以用户分组名为键的残留。 */
export type QyUgrResidue = {
  module: string
  table: string
  label: string
  rows: number
  disposition: QyUgrDisposition
  detail?: string
}

/** 迁移之后某个模型分组上的变化。 */
export type QyUgrUsableChange = {
  model_group: string
  kind: 'added' | 'removed' | 'repriced'
  /**
   * 十进制字符串,不是 number。
   *
   * 它是**报价**:走一遍 JSON number 往返会把 0.1 印成 0.10000000000000001,
   * 而运营会照着这个数字判断"这次迁移是涨价还是降价"。
   */
  from_ratio?: string
  to_ratio?: string
}

/** 迁移前后的可用清单与倍率差。 */
export type QyUgrMigrationDiff = {
  from: string
  to: string
  changes: QyUgrUsableChange[]
  unchanged: number
  /** 迁过去之后一个模型分组都用不了 —— 这批人的全部令牌当场停摆。 */
  loses_everything: boolean
}

/** 迁移目标下拉的一项。 */
export type QyUgrTarget = {
  name: string
  display_name: string
  users: number
  /** 这一档能选的模型分组数。0 = 迁进去等于让这批人的令牌全部停摆。 */
  usable_groups: number
  enabled: boolean
}

/** 删除确认弹窗的全部内容。 */
export type QyUgrImpact = {
  name: string
  users: number
  tokens: number
  empty_group_tokens: number
  subscriptions: number
  /** upgrade_group / downgrade_group 指向它的套餐。非空即不可删。 */
  blocking_plans: string[]
  residues: QyUgrResidue[]
  /** `residues` 里处置为 block 且真的有行的那些。 */
  blocking: QyUgrResidue[]
  targets: QyUgrTarget[]
  diff?: QyUgrMigrationDiff
  /**
   * 「现在能不能删」是**服务端的结论**,前端不自己推。
   *
   * 两边各推一遍必然漂移,而漂移的方向永远是"按钮亮着、点下去 400"。
   */
  deletable: boolean
  block_reason?: string
}

/** 跨库改写的半成状态。只在部分失败时出现。 */
export type QyUgrPartial = {
  stage: 'cleanup' | 'migrate' | 'prepare'
  message: string
}

/** 一次改名 / 删除的结果。 */
export type QyUgrRewriteResult = {
  from: string
  to: string
  rename: boolean
  users: number
  subscriptions: number
  plans: number
  /** 提交之后仍然挂在源分组上的在册用户数。正常恒为 0。 */
  stragglers: number
  /** 缓存失效失败的用户数。非 0 = 这批人在 TTL 内仍按旧分组计价。 */
  cache_misses: number
  residues: QyUgrResidue[]
  partial?: QyUgrPartial
}

/** 登记表里的一行(GET /group-namespace/user-groups)。 */
export type QyUgrRow = {
  name: string
  display_name: string
  note: string
  enabled: boolean
  sort_order: number
  default_mode: 'deny' | 'inherit' | 'pin'
  default_model_group: string
  users: number
  empty_group_tokens: number
  legacy_dual: boolean
  default_has_route: boolean
}

export type QyUgrListResponse = { items: QyUgrRow[] }

/** 新建的返回值。`warnings` 是"建好了,但它现在还不能用"。 */
export type QyUgrCreateResult = QyUgrRow & { warnings: string[] }

export type QyUgrCreateRequest = {
  name: string
  display_name?: string
  note?: string
  sort_order?: number
}

export type QyUgrUpdateRequest = {
  display_name?: string
  note?: string
  enabled?: boolean
  sort_order?: number
}

export type QyUgrRenameRequest = {
  new_name: string
  /** 运营在界面上看到的人数。对不上时后端返回 409。 */
  expect_users: number
}

export type QyUgrDeleteRequest = {
  migrate_to: string
  expect_users: number
  /** 「目标分组一个模型分组都用不了」这道闸门的显式覆盖。它进审计。 */
  ack_loses_everything: boolean
}
