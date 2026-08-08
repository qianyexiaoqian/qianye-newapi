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
 * 「模型分组」那**一张**表的行模型。
 *
 * ── 与用户分组页同一个病，同一个治法 ──
 *
 * 这一页此前也是两张表：上面一张「分组倍率」（主语是 `options.GroupRatio`）、
 * 下面一张「模型分组登记」（主语是 `qy_model_groups` 里这个名字本身）。同一批
 * 名字在同一屏上出现两次、各带一半的列，而运营要回答的问题只有一个：
 * 「这个池子按几倍收、用户看不看得见、备注写什么」。
 *
 * 项目方点名的四列就是这个问题的四个字段：
 * **分组名称 / 兜底倍率 / 用户可选 / 分组备注**。
 *
 * ── 「用户可选」与「分组备注」为什么是同一行上的两列，而不是一列 ──
 *
 * 上游 `options.UserUsableGroups` 是一张 `模型分组 → 说明文案` 的 map，它同时
 * 承载了两件事：**键**表示「用户能不能选到它」，**值**是给用户看的一句话。
 * 一列表达不了两件事，而把它们混在一格里正是"填了说明才算可选"这种误解的来源。
 * 所以键 → 开关列，值 → 见下面 `note` 那一段的口径说明。
 */

import type { QyMgRow, QyMgSource } from '../types'

/** 合并后的一行 —— 界面上「模型分组」表的唯一行模型。 */
export type QyMgMergedRow = {
  /**
   * 行身份（React key 与 `updateRow` 的定位依据）。
   *
   * 服务端来的行用**名字**：它在这张表上不可改（见 {@link QyMgMergedRow.isNew}），
   * 所以它稳定。本地新加的行不能用名字 —— 那一行的名字正在被敲，每一个字符都
   * 换一次 key，React 会把输入框整个卸载重挂，表现是**每敲一个字就失焦**。
   */
  id: string
  name: string
  /**
   * 兜底倍率原文。
   *
   *  - `null` = **不在** `options.GroupRatio` 里。上游 `GetGroupRatio` 此刻已经
   *    fail-open 静默返回 1，而那个 1 不是任何人配出来的 —— 界面必须给一个
   *    「补上倍率」的显式动作，不能显示成一个看起来已经填好的 `1`。
   *  - `'0'` = 显式免费，原样写回。
   *  - 中间态（`'0.'`、`''`）原样留着，否则每敲一个小数点都会被归一成 0。
   */
  ratio: string | null
  /** 在 `options.UserUsableGroups` 里有键 = 用户可选。 */
  selectable: boolean
  /**
   * 分组备注（`qy_model_groups.note`）——「用户可选」时用户看到的那一句的**默认值**。
   *
   * ── 备注只有一个来源，这是本轮拍定的口径 ──
   *
   * 站上此前有两份文案：这里的 `note`，以及 `UserUsableGroups` 的 value
   * （上游本来就把它当用户侧说明用，本站里有真实内容）。两份并存时无论谁覆盖谁，
   * 界面上都要多解释一条覆盖规则 —— 而"一份文案两个来源"正是这次要消掉的复杂度。
   *
   * 取舍的判据是**能不能表达项目方要的那条优先级链**：
   * 「按格备注（用户分组 × 模型分组）> 模型分组备注」。`UserUsableGroups` 的 value
   * 是 per-模型分组 的，它结构上表达不了第一层，留着它只会多一条永远排第三的路。
   *
   * 所以：**`note` 是唯一的编辑面**，`UserUsableGroups` 的 value 退化成纯历史
   * 兜底（后端在 `note` 为空时才用它），界面上不再提供第二个输入框。
   * 迁移不靠脚本：`usableDescription` 非空而 `note` 为空的行上给一个「采用为
   * 分组备注」的一键动作，运营点一次就把那句话搬进唯一的那个来源。
   */
  note: string
  /** `UserUsableGroups` 里那段**原文**（未经 `note` 覆盖）。没有则为空串。 */
  usableDescription: string
  /**
   * 这个名字**是从哪来的**。空数组 = 登记表拉不到（扩展未启用）。
   *
   * 三种来源对应三种完全不同的处置，见 {@link QyMgSource}。
   */
  sources: readonly QyMgSource[]
  /** 真的能路由（abilities 里有行）。`null` = 登记表拉不到，不知道。 */
  hasRoute: boolean | null
  /** 该分组下启用的渠道数。`null` = 不知道。 */
  channelCount: number | null
  /** 同一个名字同时是用户分组与模型分组。 */
  legacyDual: boolean
  /** 在 `options.AutoGroups` 里的位次，从 1 起；0 = 不在。 */
  autoPosition: number
  /** 在登记表里有行 —— 只有这样的行才能走那套带影响面的联动删除。 */
  registered: boolean
  /**
   * 本地新加的、还没保存过的一行。**只有它的名字可以改。**
   *
   * ── 为什么已存在的行名字是只读的 ──
   *
   * 合并之前，上面那张倍率表把名字做成了输入框。在那里把 `pool` 改成 `pool2`
   * 的实际效果是：`options.GroupRatio` 里多一个 `pool2`、少一个 `pool`，而
   * `abilities`（物化路由表）、`channels.group`、`tokens.group`、两张授权表、
   * 套餐解锁、auto 顺序里的 `pool` 一个都没动 —— 站上立刻多出一个没有渠道的
   * 新分组，和一批引用着已消失名字的旧数据，全程零提示。
   *
   * 改名要横跨路由与计费两条链路重写六处，射程之外（见 `../index.tsx` 的说明）。
   * 所以这里不提供改名，只提供新建与删除；已存在的名字渲染成文本。
   */
  isNew: boolean
}

export type QyMgBuildRowsInput = {
  /** 登记表返回的行。拉不到时传空数组。 */
  registry: readonly QyMgRow[]
  /** 已解析的 `options.GroupRatio`。 */
  groupRatios: Readonly<Record<string, number>>
  /** 已解析的 `options.UserUsableGroups`（键 = 可选，值 = 历史说明文案）。 */
  usableGroups: Readonly<Record<string, string>>
  /** `options.AutoGroups` 的顺序。 */
  autoGroups: readonly string[]
}

/**
 * 三个来源 → 一张表。
 *
 * ── 为什么并集里必须有 `UserUsableGroups` 的键 ──
 *
 * 只取 `GroupRatio` ∪ 登记表的话，「在全局可选清单里、却没有兜底倍率、也没
 * 登记过」这一档在界面上根本不存在 —— 而它是资金泄漏集合里最容易造出来的一种：
 * 用户选得到它，每一次请求按凭空的 1.0 计费。
 *
 * 排序按名字升序（`localeCompare`）。
 */
export function qyMgBuildRows(input: QyMgBuildRowsInput): QyMgMergedRow[] {
  const registryByName = new Map(input.registry.map((row) => [row.name, row]))
  const names = new Set<string>([
    ...registryByName.keys(),
    ...Object.keys(input.groupRatios),
    ...Object.keys(input.usableGroups),
  ])

  const rows: QyMgMergedRow[] = []
  for (const name of names) {
    const registered = registryByName.get(name)
    const hasRatio = Object.hasOwn(input.groupRatios, name)
    const autoIndex = input.autoGroups.indexOf(name)
    rows.push({
      id: name,
      name,
      // 倍率取 **option 侧**而不是登记表里的 `base_ratio`：这一页正在编辑的就是
      // option，登记表那一份是它的一个快照。两者不一致时以正在被编辑的为准，
      // 否则运营改完一个数、下一次回读又被快照盖回去。
      ratio: hasRatio ? String(input.groupRatios[name]) : null,
      selectable: Object.hasOwn(input.usableGroups, name),
      note: registered?.note ?? '',
      usableDescription: input.usableGroups[name] ?? '',
      sources: registered?.sources ?? [],
      hasRoute: registered?.has_route ?? null,
      channelCount: registered?.channel_count ?? null,
      legacyDual: registered?.legacy_dual ?? false,
      autoPosition: autoIndex < 0 ? 0 : autoIndex + 1,
      registered: registered != null,
      isNew: false,
    })
  }
  return rows.sort((a, b) => a.name.localeCompare(b.name))
}

/**
 * 行 → `options.GroupRatio` 的 JSON。
 *
 * 三条规则，每一条都有一个具体的错误方向：
 *
 *  1. `ratio === null` 的行**不写**。它表示「这个名字只在别处出现」，
 *     替它补一个 1 等于替运营做了一次定价决定。
 *  2. 解析不出数值的行落回 `1`，**包括空输入框**。上游 `normalizeRatio` 是
 *     `Number.isFinite(Number(v)) ? Number(v) : 1`，而 `Number('') === 0` ——
 *     照抄的话，清空一个倍率输入框会静默把那个模型分组改成免费。想要免费必须
 *     显式敲一个 `0`，那一条仍然逐位保持上游语义。
 *  3. 名字两侧去空白、空名丢弃。
 */
export function qyMgSerializeRatios(rows: readonly QyMgMergedRow[]): string {
  const out: Record<string, number> = {}
  for (const row of rows) {
    const name = row.name.trim()
    if (name === '') continue
    if (row.ratio === null) continue
    const raw = row.ratio.trim()
    const parsed = raw === '' ? Number.NaN : Number(raw)
    out[name] = Number.isFinite(parsed) ? parsed : 1
  }
  return JSON.stringify(out, null, 2)
}

/**
 * 行 → `options.UserUsableGroups` 的 JSON。
 *
 * ── value 写什么 ──
 *
 * 写**这一行现在的 `usableDescription`**（历史原文），一个字都不动。
 *
 * 不写 `note`：那会让备注有第二个写入者。这一页上勾一次「用户可选」的开关就把
 * 备注复制一份进 options，此后运营在备注框里改字，用户看到的仍是复制品，
 * 而界面上两处都显示着新值 —— 这正是本轮要消掉的那种"数字骗人"。
 *
 * 也不写空串：那等于用一次无关的开关操作把历史文案清空，而在后端 `note` 还没
 * 填的那些行上，被清掉的恰好是用户此刻真正看到的那句话。
 */
export function qyMgSerializeUsableGroups(
  rows: readonly QyMgMergedRow[]
): string {
  const out: Record<string, string> = {}
  for (const row of rows) {
    const name = row.name.trim()
    if (name === '' || !row.selectable) continue
    out[name] = row.usableDescription
  }
  return JSON.stringify(out, null, 2)
}

/**
 * 倍率填了、但解析不出数值的行（空串、`abc`、`1e999`）。非空即禁用保存。
 * 见 {@link qyMgSerializeRatios} 第 2 条：不拦的话，一个空输入框会变成一次
 * 静默的免费定价。
 */
export function qyMgInvalidRatioNames(
  rows: readonly QyMgMergedRow[]
): string[] {
  return rows
    .filter((row) => {
      if (row.ratio === null) return false
      const raw = row.ratio.trim()
      return raw === '' || !Number.isFinite(Number(raw))
    })
    .map((row) => row.name.trim())
}

/**
 * 「用户可选、却没有兜底倍率」的模型分组 —— 这一批**正在按 1.0 静默扣费**。
 *
 * 判据刻意收窄到 `selectable`：一个既不可选、也没有倍率的名字（纯登记残留）
 * 不在任何计费路径上，把它一起报出来会让这条黄条变成常驻噪音，而噪音里的
 * 真警报没有人看。
 */
export function qyMgSilentlyBilledNames(
  rows: readonly QyMgMergedRow[]
): string[] {
  return rows
    .filter((row) => row.ratio === null && row.selectable)
    .map((row) => row.name.trim())
    .filter((name) => name !== '')
}

/** 兜底倍率恰好是 0 的模型分组 —— 白送，必须显式确认过才合理。 */
export function qyMgFreeNames(rows: readonly QyMgMergedRow[]): string[] {
  return rows
    .filter((row) => {
      if (row.ratio === null) return false
      const raw = row.ratio.trim()
      // 空输入框**不算**「白送」：它会同时触发红条「填不出数值」与黄条
      // 「完全免费」，两条对同一行给出互相矛盾的结论，而写回值其实是 1。
      if (raw === '') return false
      return Number(raw) === 0
    })
    .map((row) => row.name.trim())
    .filter((name) => name !== '')
}
