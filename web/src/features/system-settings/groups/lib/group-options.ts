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
import { safeJsonParse } from '../../utils/json-parser'

/**
 * 「分组定价」那一页被拆成三块之后，7 个上游 option 的归属与序列化。
 *
 * ── 为什么是 7 项而不是题面上的 6 段 ──
 *
 * 原 `group-ratio-form.tsx` 的表单值里 `GroupGroupRatio` 不是独立表单行
 * （它在矩阵编辑器内部被读写），所以肉眼数只能数出 6 段。按 6 段做完备性
 * 检查会让「矩阵格子的倍率」落单。
 *
 * （曾经的第 8 项 `GroupSpecialUsableGroup` 已随后端一并下线：它从来没有真正
 * 生效过 —— 上游在差分算完之后无条件把用户分组自己补回去，把唯一有意义的
 * `-:自己` 恒抵消掉。理由见 `setting/ratio_setting/group_ratio.go`。）
 *
 * ── 归属判据（一句话）──
 *
 * 看这个配置的**主语**：主语是模型分组 → 模型分组页；主语是用户分组 →
 * 用户分组页；主语是 (用户分组, 模型分组) 这一对 → 可用范围页。
 *
 * ── 为什么归属要写成常量而不是只写在注释里 ──
 *
 * 「同一份数据只有一个编辑器」是这次拆页唯一的硬约束，而它在代码里没有任何
 * 天然的表现形式：谁都可以在第二个页面上再放一个输入框，typecheck 全绿、
 * 页面看起来更方便，直到两个页面各自持有一份基线、互相把对方的改动覆盖掉。
 * 三份清单摆在这里，`__tests__/group-options.test.ts` 断言它们两两不交且并集
 * 恰好是全部 8 项 —— 加错一处即红。
 */
export const GROUP_OPTION_KEYS = [
  'GroupRatio',
  'TopupGroupRatio',
  'UserUsableGroups',
  'GroupGroupRatio',
  'AutoGroups',
  'MaxTokenAutoGroups',
  'DefaultUseAutoGroup',
] as const

export type GroupOptionKey = (typeof GROUP_OPTION_KEYS)[number]

/**
 * A「用户分组」页可写的键。
 *
 * `TopupGroupRatio` 的主语是用户分组（一个人属于哪一档 → 充值按几折），
 * 它出现在原来那张模型分组表上是全页最误导人的一处错位。
 * `DefaultUseAutoGroup` 是站级开关，挂在用户分组页头 —— 它描述「新令牌的
 * 初始分组行为」，与模型分组的倍率无关。
 */
export const USER_GROUP_PAGE_KEYS = [
  'TopupGroupRatio',
  'DefaultUseAutoGroup',
] as const

/**
 * B「用户分组可用的模型分组配置」页可写的键。
 *
 * `GroupGroupRatio` 走矩阵自己的 PUT（两库两阶段写入），不经 `updateOption`；
 * 列在这里是因为**编辑器唯一性**这条约束管的是「谁能改」，不是「走哪个端点」。
 * `UserUsableGroups` 是未设定范围的用户分组回落到的那份全局清单，与矩阵必须
 * 同页 —— 把它放到模型分组页去，「未设定范围」这个状态就没有任何地方解释了。
 */
export const GROUP_MATRIX_PAGE_KEYS = [
  'UserUsableGroups',
  'GroupGroupRatio',
] as const

/**
 * B 页里**经 `updateOption` 写回**的那一部分。
 *
 * 与 {@link GROUP_MATRIX_PAGE_KEYS} 的差是 `GroupGroupRatio`：它走矩阵自己的
 * PUT，与可选清单在同一次两阶段写入里落两个库。两者混进同一个 `updateOption`
 * 调用会绕开那道预览闸门与部分失败横幅。守卫测试断言这是个真子集。
 */
export const GROUP_MATRIX_PAGE_OPTION_KEYS = ['UserUsableGroups'] as const

/**
 * C「模型分组」页可写的键。
 *
 * `AutoGroups` 与 `MaxTokenAutoGroups` 被同一对后端函数消费
 * （`GetUserAutoGroup` / `FilterUserTokenAutoGroups`），拆到两页去必然出现
 * 「只改了一个」。两者同页。
 */
export const MODEL_GROUP_PAGE_KEYS = [
  'GroupRatio',
  'AutoGroups',
  'MaxTokenAutoGroups',
] as const

/**
 * 哪儿都不可写、只读展示的键。
 *
 * 目前是空的：唯一的成员 `GroupSpecialUsableGroup` 已随后端下线。清单本身保留 ——
 * 归属守卫按「三份可写清单 + 这一份」求并集,删掉它会让下一个"只读但仍在生效"
 * 的配置项没有地方登记,而那正是这套守卫要防的形状。
 */
export const READ_ONLY_GROUP_OPTION_KEYS = [] as const

// ─────────────────────────── 解析 ───────────────────────────

export function parseGroupRatioMap(value: string): Record<string, number> {
  return safeJsonParse<Record<string, number>>(value, {
    fallback: {},
    silent: true,
  })
}

export function parseGroupDescriptionMap(
  value: string
): Record<string, string> {
  return safeJsonParse<Record<string, string>>(value, {
    fallback: {},
    silent: true,
  })
}

export function parseNestedRatioMap(
  value: string
): Record<string, Record<string, number>> {
  return safeJsonParse<Record<string, Record<string, number>>>(value, {
    fallback: {},
    silent: true,
  })
}

export function parseAutoGroups(value: string): string[] {
  const parsed = safeJsonParse<unknown>(value, { fallback: [], silent: true })
  if (!Array.isArray(parsed)) return []
  return parsed.filter((item): item is string => typeof item === 'string')
}

// ─────────────────────────── 模型分组（C 页）───────────────────────────

/**
 * 模型分组表的一行。
 *
 * `ratio` 是 `string | null`，**不是 number**：
 *
 *  - `null` = 这个名字出现在别处（全局可选清单）但**不在** `options.GroupRatio`
 *    里。上游 `GetGroupRatio` 此刻已经 fail-open 静默返回 1，而那个 1 不是任何
 *    人配出来的。把它渲染成一个正常的 `1` 会让运营看不出差别 —— 而这正是本轮
 *    要消灭的那种静默。
 *  - `'0'` = 显式免费，必须原样写回；`Number()` 往返 + `omitempty` 会让它消失，
 *    一个本该免费的分组静默变成兜底价。
 *  - 输入框里的中间态（`'0.'`、`''`）也要能原样留着，否则每敲一个小数点都会被
 *    归一成 0。
 */
export type ModelGroupRow = {
  id: string
  name: string
  ratio: string | null
}

let modelGroupRowSeq = 0

/**
 * 表格行的稳定身份。
 *
 * ── 为什么必须是单调自增，而不是从名字或数组长度派生 ──
 *
 * 行 id 是 React 的 `key`，也是 `updateRow` / 删除的定位依据。从**名字**派生
 * （`mg_new_${name}`）时：新增一行拿到 `mg_new_group_1`、改名成 `vip`、再新增一行
 * ——`group_1` 又空出来，第二行拿到同一个 id，此后给其中一行填倍率会把另一行也
 * 一起改掉（`current.map(row => row.id === id ? … : row)` 命中两行），删除同理
 * 一次删两行。从**数组长度**派生（`uu_new_${current.length}`）时：删掉任意一行后
 * 长度回落，下一次新增就与已存在的行撞 id，表现是「我明明加了三个，只出现两个」。
 *
 * 这两种形状此前在三页里各出现过一次，且都直接改的是钱（兜底倍率）或用户能选到
 * 哪些模型分组。序列号没有这些性质：它只增不减、与内容无关。
 */
export function nextRowId(prefix: string) {
  modelGroupRowSeq += 1
  return `${prefix}_${modelGroupRowSeq}`
}

/**
 * 由 `GroupRatio` ∪ `UserUsableGroups` 的键建行。
 *
 * 并集而不是只取 `GroupRatio`：只取后者的话，「在全局可选清单里、却没有兜底
 * 倍率」这一档在界面上根本不存在，而它是资金泄漏集合里最容易被造出来的一种。
 */
export function buildModelGroupRows(
  groupRatio: string,
  userUsableGroups: string
): ModelGroupRow[] {
  const ratioMap = parseGroupRatioMap(groupRatio)
  const usableMap = parseGroupDescriptionMap(userUsableGroups)
  const names = new Set([...Object.keys(ratioMap), ...Object.keys(usableMap)])
  const rows: ModelGroupRow[] = []
  for (const name of names) {
    const hasRatio = Object.hasOwn(ratioMap, name)
    rows.push({
      id: nextRowId('mg'),
      name,
      ratio: hasRatio ? String(ratioMap[name]) : null,
    })
  }
  return rows
}

/**
 * 行 → `options.GroupRatio` 的 JSON。
 *
 * 三条规则，每一条都有一个具体的错误方向：
 *
 *  1. `ratio === null` 的行**不写**。它表示「这个名字只在可选清单里」，
 *     替它补一个 1 等于替运营做了一次定价决定。
 *  2. 名字两侧去空白、空名丢弃；重名由调用方在界面上拦（保存前禁用）。
 *  3. 解析不出数值的行落回 `1`，**包括空输入框**。
 *
 * ── 第 3 条对上游的一处刻意偏离，理由是钱 ──
 *
 * 上游 `normalizeRatio` 是 `Number.isFinite(Number(v)) ? Number(v) : 1`，
 * 而 `Number('') === 0` 且 0 是有限数 —— 于是**把倍率输入框清空会静默写进
 * 一个 0 倍率**，那个模型分组从此免费，保存成功、没有任何提示。
 * 这里把空串与 `abc` 同等看待（落回 1，即 `GetGroupRatio` 找不到键时的值），
 * 并由 {@link invalidRatioRowNames} 在界面上把这些行标红、禁用保存 ——
 * 两道加起来，「清空 = 免费」这条路径既不可达也不会被静默走过去。
 * 想要免费必须显式敲一个 `0`，那一条仍然逐位保持上游语义。
 */
export function serializeModelGroupRows(
  rows: readonly ModelGroupRow[]
): string {
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
 * 倍率填了、但解析不出数值的行（空串、`abc`、`1e999`）。
 *
 * 非空即禁用保存。见 {@link serializeModelGroupRows} 第 3 条：不拦的话，
 * 一个空输入框会变成一次静默的免费定价。
 */
export function invalidRatioRowNames(rows: readonly ModelGroupRow[]): string[] {
  return rows
    .filter((row) => {
      if (row.ratio === null) return false
      const raw = row.ratio.trim()
      return raw === '' || !Number.isFinite(Number(raw))
    })
    .map((row) => row.name.trim())
}

/** 重名（去空白后）。非空即禁用保存：写回时后一个键会静默吃掉前一个。 */
export function duplicateRowNames(rows: readonly { name: string }[]): string[] {
  const counts = new Map<string, number>()
  for (const row of rows) {
    const name = row.name.trim()
    if (name === '') continue
    counts.set(name, (counts.get(name) ?? 0) + 1)
  }
  return [...counts.entries()]
    .filter(([, count]) => count > 1)
    .map(([name]) => name)
}

/**
 * 在全局可选清单里、却没有兜底倍率的模型分组。
 *
 * 这一批**正在按 1.0 静默扣费**：用户选得到它（清单里有），而
 * `GetGroupRatio` 找不到键时 fail-open 返回 1。C 页顶部必须常驻这条黄条。
 */
export function modelGroupsMissingRatio(
  rows: readonly ModelGroupRow[]
): string[] {
  return rows
    .filter((row) => row.ratio === null && row.name.trim() !== '')
    .map((row) => row.name.trim())
}

/** 兜底倍率恰好是 0 的模型分组 —— 白送，必须显式确认过才合理。 */
export function freeModelGroups(rows: readonly ModelGroupRow[]): string[] {
  return rows
    .filter((row) => {
      if (row.ratio === null) return false
      const raw = row.ratio.trim()
      // 空输入框**不算**「白送」。`Number('') === 0`，不排除的话一个正在被清空
      // 准备重填的格子会同时触发红条「填不出数值」与黄条「倍率为 0，调用完全免费」，
      // 两条对同一行给出互相矛盾的结论；而 serializeModelGroupRows 对空串实际
      // 写回的是 1，三处说法各不相同。运营据此判断「已经免费了」是错的。
      if (raw === '') return false
      return Number(raw) === 0
    })
    .map((row) => row.name.trim())
    .filter((name) => name !== '')
}

// ─────────────────────────── auto 顺序（C 页）───────────────────────────

export function moveAutoGroup(
  list: readonly string[],
  index: number,
  direction: 'down' | 'up'
): string[] {
  const next = [...list]
  const target = direction === 'up' ? index - 1 : index + 1
  if (index < 0 || index >= next.length) return next
  if (target < 0 || target >= next.length) return next
  ;[next[index], next[target]] = [next[target], next[index]]
  return next
}

export function serializeAutoGroups(list: readonly string[]): string {
  return JSON.stringify([...list], null, 2)
}

// ─────────────────────────── 用户分组（A 页）───────────────────────────

/**
 * 用户分组表的一行。
 *
 * `topupRatio` 同样是字符串三态：`''` = 未设置（键不写进 JSON，回落 1），
 * `'0'` = 显式免费充值。混成 `number | undefined` 会让 0 在 `omitempty`
 * 里消失，表现是「配了免费充值、实际按原价收」。
 */
export type UserGroupRow = {
  id: string
  name: string
  topupRatio: string
}

/**
 * 用户分组名的取值域。
 *
 * **刻意不含 `options.GroupRatio` 的键** —— 那是模型分组清单。两者在底层是
 * 同一个字符串命名空间（本轮要拆开的正是这件事），把模型分组的名字灌进用户
 * 分组页，等于在一个专门用来区分两者的界面上把它们又混回去。
 *
 * 三个来源分别对应三种「站上确实存在这一档人」的证据：
 *   · `authoritative`  服务端按 `users.group` 现算的清单（扩展未启用时为空）
 *   · `TopupGroupRatio` 的键：有人给这一档配过充值折扣
 *   · `GroupGroupRatio` 的**外层**键：有人给这一档配过交叉倍率
 */
export function buildUserGroupRows(
  authoritative: readonly string[],
  topupGroupRatio: string,
  groupGroupRatio: string
): UserGroupRow[] {
  const topupMap = parseGroupRatioMap(topupGroupRatio)
  const crossMap = parseNestedRatioMap(groupGroupRatio)
  const names = new Set([
    ...authoritative,
    ...Object.keys(topupMap),
    ...Object.keys(crossMap),
  ])
  const rows: UserGroupRow[] = []
  for (const name of names) {
    rows.push({
      id: nextRowId('ug'),
      name,
      topupRatio: Object.hasOwn(topupMap, name) ? String(topupMap[name]) : '',
    })
  }
  return rows
}

/**
 * 行 → `options.TopupGroupRatio` 的 JSON。
 *
 * 空串跳过（= 未设置），非法数值也跳过 —— 与上游
 * `serializeGroupPricingRows` 里那段 `topup !== '' && Number.isFinite(...)`
 * 逐字同口径。写进一个 `NaN` 会让整份 JSON 变成 `null`，充值折扣全站失效。
 */
export function serializeTopupRatios(rows: readonly UserGroupRow[]): string {
  const out: Record<string, number> = {}
  for (const row of rows) {
    const name = row.name.trim()
    if (name === '') continue
    const raw = row.topupRatio.trim()
    if (raw === '') continue
    const parsed = Number(raw)
    if (!Number.isFinite(parsed)) continue
    out[name] = parsed
  }
  return JSON.stringify(out, null, 2)
}

// ─────────────────────────── 可选清单（B 页）───────────────────────────

export type UsableGroupRow = {
  id: string
  name: string
  description: string
}

export function buildUsableGroupRows(
  userUsableGroups: string
): UsableGroupRow[] {
  const map = parseGroupDescriptionMap(userUsableGroups)
  return Object.entries(map).map(([name, description]) => ({
    id: nextRowId('uu'),
    name,
    description: typeof description === 'string' ? description : '',
  }))
}

export function serializeUsableGroupRows(
  rows: readonly UsableGroupRow[]
): string {
  const out: Record<string, string> = {}
  for (const row of rows) {
    const name = row.name.trim()
    if (name === '') continue
    out[name] = row.description
  }
  return JSON.stringify(out, null, 2)
}
