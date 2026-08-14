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
  'TokenDefaultGroups',
] as const

export type GroupOptionKey = (typeof GROUP_OPTION_KEYS)[number]

/**
 * A「用户分组」页负责的键。
 *
 * `TopupGroupRatio` 的主语是用户分组（一个人属于哪一档 → 充值按几折），
 * 它出现在原来那张模型分组表上是全页最误导人的一处错位。
 * `DefaultUseAutoGroup` 是站级开关，挂在用户分组页头 —— 它描述「新令牌的
 * 初始分组行为」，与模型分组的倍率无关。
 *
 * ── 「负责」不等于「经 `updateOption` 写」 ──
 *
 * 本轮起 `TopupGroupRatio` 由 `PUT /user-groups/:name` **按单个键**写回：后端把
 * `topup_ratio` 与 `clear_topup_ratio` 分成两个字段，正是为了区分"这次没打算改"
 * 与"删掉这个键、回落上游兜底"，而后者会改变收款金额。在这一页再拼一份整表
 * JSON 经 `updateOption` 写回去，就会把另一个管理员刚改的另一档静默覆盖成本页
 * 打开那一刻的旧值 —— 与 `GroupGroupRatio` 走矩阵自己的 PUT 是同一条理由。
 *
 * 这份清单管的是**编辑器唯一性**（谁能改），不是走哪个端点。所以它仍然列在
 * 这里，而归属守卫仍然成立。
 */
export const USER_GROUP_PAGE_KEYS = [
  'TopupGroupRatio',
  'DefaultUseAutoGroup',
] as const

/**
 * B「用户分组 × 模型分组」这一对的可写键。
 *
 * `GroupGroupRatio` 走矩阵自己的 PUT（两库两阶段写入），不经 `updateOption`；
 * 列在这里是因为**编辑器唯一性**这条约束管的是「谁能改」，不是「走哪个端点」。
 * 它现在的唯一编辑面是「用户分组」表行内的配置弹窗。
 *
 * ── `UserUsableGroups` 为什么从这一栏搬去了模型分组页 ──
 *
 * 它此前挂在这里，理由是「未设定范围的用户分组回落到它，所以要与矩阵同页」。
 * 那个理由建立在一个已经被换掉的文案上：现在「没设可用清单」的解释是
 * **「按模型分组自己的『用户可选』开关来」**，而那个开关就是这份 map 的键 ——
 * 它的主语是**一批渠道**，不是 (用户分组, 模型分组) 这一对。
 *
 * 搬过去之后它在模型分组表上是一个可见的开关列，而不是另一页上一张需要解释
 * 「它和上面那张矩阵是什么关系」的独立表格。项目方点名的四列里就有它。
 */
export const GROUP_MATRIX_PAGE_KEYS = ['GroupGroupRatio'] as const

/**
 * D「令牌默认分组」页负责的键。
 *
 * `TokenDefaultGroups` 是「用户分组 → 令牌创建界面预选哪个模型分组」的映射，
 * 主语是**用户分组**，因此按上面那条归属判据它本该并进 A 页。刻意单独成页，
 * 理由是 A 页的表行来自扩展的分组矩阵接口（`qyGmMatrixQuery`）——
 * `group_matrix` 关掉时那张表整个是空的，编辑器会跟着一起消失，而这项配置
 * 与可用范围毫无关系、必须在扩展未启用时照样能配。
 *
 * 它的候选清单走上游的 `/api/user-group/options` 与 `/api/model-group/options`，
 * 两个端点都不依赖扩展。
 *
 * 归属守卫因此从三份清单变成四份，「两两不交 + 并集完备」两条断言一字未改。
 */
export const TOKEN_DEFAULT_PAGE_KEYS = ['TokenDefaultGroups'] as const

/**
 * B 栏里**经 `updateOption` 写回**的那一部分 —— 现在是空的。
 *
 * `GroupGroupRatio` 走矩阵自己的两阶段 PUT，绝不能经普通的 `updateOption`：
 * 那会绕开预览闸门、绕开 `base_ratio_hash` 冲突检测、也绕开部分失败横幅，
 * 而那正是这套两库写入最坏的失败方式。
 *
 * 清单本身保留：守卫测试按它断言「B 栏经 updateOption 写回的键是它归属键的
 * 真子集，且绝不含 GroupGroupRatio」。删掉它，下一个想给交叉倍率加一条
 * 「顺手也写一下 options」的近路就没有任何东西拦得住。
 */
export const GROUP_MATRIX_PAGE_OPTION_KEYS = [] as const

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
  'UserUsableGroups',
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

// ─────────────────────────── 校验 ───────────────────────────

/**
 * 重名（去空白后）。非空即禁用保存：写回时后一个键会静默吃掉前一个。
 *
 * 只有「新加的、还没保存过的行」能改名字，所以现实里撞名的方式只有一种：
 * 新加一行、把它的名字敲成一个已经存在的分组。不拦的话保存之后那个已存在的
 * 分组的兜底倍率会被新行的值覆盖 —— 一次改价，而运营以为自己在新建。
 */
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
