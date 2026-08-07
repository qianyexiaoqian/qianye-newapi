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
 * 模型分组登记表的管理端契约。
 *
 * 与后端 `qianye/modules/groupns` 的 `ModelGroupRow`（列表）、
 * `ModelGroupImpact`（影响面）、`ModelGroupDeleteResult`（删除结果）
 * 一一对应，字段名逐字相同。
 */

/**
 * 一个模型分组名的**来源**。
 *
 * 项目方原话:「模型分组当前预设有点混乱」。混乱的具体形状是回填出来的名单里
 * 混着 `default` / `group_1` 这类历史残留，而界面上没有任何一处说得清
 * 「这一行是从哪来的、它现在还能不能路由」。三种来源对应三种完全不同的处置：
 *
 *  - `group_ratio`    有兜底倍率。只有它没有路由 = 用户选得到、一发请求必然 503。
 *  - `abilities`      真的能路由。只有它没有倍率 = 走到它的请求正按 1.0 静默计费。
 *  - `registry_only`  两者都没有，一个纯粹的名字 —— 最安全的删除对象。
 */
export type QyMgSource = 'abilities' | 'group_ratio' | 'registry_only'

/** 列表里的一行。 */
export type QyMgRow = {
  name: string
  display_name: string
  note: string
  enabled: boolean
  sort_order: number

  base_ratio: number
  ratio_missing: boolean
  has_route: boolean
  channel_count: number
  legacy_dual: boolean

  sources: QyMgSource[]
  in_usable_groups: boolean
  /** 全局「用户可选分组」里那段**原文**（未经备注覆盖）。 */
  usable_description: string
  /** 在 `options.AutoGroups` 里的位次，从 1 起；0 表示不在。 */
  auto_position: number
}

export type QyMgListResponse = { items: QyMgRow[] }

/** 各模块声明的一处残留。处置见后端 `qianye/modules/groupns/residue.go`。 */
export type QyMgResidue = {
  module: string
  table: string
  label: string
  rows: number
  disposition: 'block' | 'clean' | 'keep' | 'rewrite'
  detail?: string
}

/** 删除前必须摆在运营眼前的全部东西。 */
export type QyMgImpact = {
  name: string
  registered: boolean
  sources: QyMgSource[]

  in_group_ratio: boolean
  base_ratio: number
  in_usable_groups: boolean
  usable_description: string
  note: string
  auto_position: number

  has_route: boolean
  ability_rows: number
  enabled_channels: number

  tokens: number
  token_owners: number

  cross_ratio_user_groups: string[]
  pinned_by_user_groups: string[]
  pinned_empty_group_tokens: number

  residues: QyMgResidue[]

  /** **不可覆盖**的拒绝理由。非空即删不了。 */
  blockers: string[]
  needs_force_has_route: boolean
  needs_force_orphan_tokens: boolean
}

export type QyMgDeleteRequest = {
  force_has_route?: boolean
  force_orphan_tokens?: boolean
}

export type QyMgDeleteResult = {
  name: string
  impact: QyMgImpact
  removed_from: string[]
  /** 非空 = 扩展库那一半已提交、options 那一半没写成。必须原样显示。 */
  partial?: { stage: string; message: string }
}

export type QyMgUpdateRequest = {
  display_name?: string
  note?: string
  enabled?: boolean
  sort_order?: number
}
