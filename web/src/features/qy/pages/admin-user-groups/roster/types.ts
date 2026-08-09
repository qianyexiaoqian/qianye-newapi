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

/**
 * 拒绝删除 / 改名的分支代码，与后端 `groupns.UserGroupBlock*` 常量同值。
 *
 * 后端下发的 `block_reason` 回答的是「为什么不行」；运营站在弹窗前要问的下一个
 * 问题是「那我该干什么」，而那是一条与界面强相关的指路（去哪一页、改哪个字段），
 * 只能由前端给。**按 `block_reason` 的中文子串去猜分支是不行的** —— 任何一次
 * 文案润色都会静默让指路消失，而消失之后界面看起来完全正常。
 */
export type QyUgrBlockCode =
  /** 套餐的升级/降级分组指向它。 */
  | 'blocking_plans'
  /** 某个模块声明了处置为 block 的残留（冻结值，只有人能决定新值）。 */
  | 'blocking_residues'
  /** 这一档还有人，但站上没有第二个已登记分组可迁。 */
  | 'no_migration_target'
  /** `default` 是 `users.group` 的数据库默认值，永不可删、永不可改名。 */
  | 'upstream_default'

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
  /** `block_reason` 的机器可读版本。见 {@link QyUgrBlockCode}。 */
  block_code?: QyUgrBlockCode
  /**
   * 改名能不能进行 —— 与删除**共用同一道 block 残留闸门**。
   *
   * 对一份冻结的引用来说「改个名」与「删掉」完全等价，所以后端两处走同一个
   * `renamability`。它下发到这里是为了让运营在打开弹窗时就看见这条路也走不通，
   * 而不是删不掉之后去试改名、再吃一个提交后的 400。
   */
  renamable: boolean
  rename_block_reason?: string
  rename_block_code?: QyUgrBlockCode
}

/**
 * 跨库改写的半成状态。只在部分失败时出现。
 *
 * `topup` 是编辑弹窗那条链路上唯一的半成状态：展示属性已经落进扩展库、充值倍率
 * 没写进上游 `options`。后端此前对它回 500 + 一句通用的「处理失败，请稍后重试」，
 * 于是运营合理推断「什么都没保存」，而下一次回读会显示新备注 + 旧倍率，
 * 解释只写在审计正文里 —— 而看审计的不是此刻站在弹窗前的那个人。
 */
export type QyUgrPartial = {
  stage: 'cleanup' | 'migrate' | 'prepare' | 'topup'
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
  /**
   * 充值倍率 `options.TopupGroupRatio[分组名]`。
   *
   * 给了就设（**含 0**，那是「这一档充值免费」），不给 = 不改。
   * 要**删掉这个键**（回落上游兜底 1）必须用 {@link clear_topup_ratio}，
   * 不能传 `null` —— Go 侧 `"topup_ratio": null` 与字段缺席都得到 nil 指针，
   * 两者不可区分，而「这次没打算改」与「回落兜底」的后果差一个收款金额。
   */
  topup_ratio?: number
  /** 把充值倍率这个键整个删掉。与 {@link topup_ratio} 互斥，同时给出会 400。 */
  clear_topup_ratio?: boolean
}

/**
 * `PUT /user-groups/:name` 的响应。
 *
 * 展示属性落扩展库、充值倍率落上游 `options`，两库不原子且顺序是**先展示属性
 * 后倍率**：前者失败时后者一个字节都没动（零资金影响）。反过来做的话收款金额
 * 已经变了而界面上那一行看起来什么都没保存。
 */
export type QyUgrUpdateResult = {
  /** 后端**自动补了一行登记**（这个名字此前只在 `users.group` 里）。 */
  backfilled: boolean
  /** 充值倍率这次到底改了什么（后端下发的原文，前端不二次转译）。空 = 没改。 */
  topup_change: string
  /** 非空 = 两库只写成了一半。**必须原样弹出来并留在屏幕上。** */
  partial?: QyUgrPartial
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
