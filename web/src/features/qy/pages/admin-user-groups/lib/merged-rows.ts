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
 * 「用户分组」那**一张**表的显示层规则。
 *
 * ── 合并本身**不在这里**，在后端 ──
 *
 * 项目方原话：「用户分组这一页，这两个可以合并成一个。明明一个很简单的问题
 * 为什么这么搞这么复杂？」界面上此前并排着「用户分组」（从 `users.group` 观测
 * 出来的）与「用户分组登记」（运营在 `qy_user_groups` 里登记出来的）两张表，
 * 名字几乎一样、内容互相重叠。
 *
 * 本轮后端把三个来源（观测 ∪ 登记 ∪ 只存在于 options 键里的死配置）合成一张
 * `userGroupRow` 下发，连「可用模型分组」那一列的名字清单都算好了。
 * **前端一层都不再重推**：那一列在"设了范围"与"没设范围"下取值方式完全不同
 * （前者读清单，后者读上游 `GetUserUsableGroups` 的实际结果），而后者的判据
 * 只有后端有。重推一遍的漂移方向是「表里列了 3 个、点开弹窗只有 2 个」，
 * 而运营对着矛盾的数字最可能的动作是重配一遍 —— 重配的动作恰好是撤销与改价。
 *
 * 所以这个文件只剩两件纯显示 / 纯输入的事，它们确实属于前端。
 */

/**
 * 表格里那一列显示几个名字、折进去几个。
 *
 * 项目方原话：「【可用模型分组】直接把模型分组名称显示上去，如：免费の渠道、
 * 浅夜の梦专属号池」—— 所以列的是名字，不是个数。但站里模型分组有十几个，
 * 全列出来会把一行撑成一堵墙，所以超出的折成 `+N`。
 *
 * 折叠**只折显示**：整格的 `title` 与配置弹窗里都是完整名单。
 */
export function qyUgSplitUsable(
  usable: readonly string[],
  limit: number
): { shown: string[]; overflow: number } {
  if (usable.length <= limit) return { shown: [...usable], overflow: 0 }
  return { shown: usable.slice(0, limit), overflow: usable.length - limit }
}

/**
 * 充值倍率输入框的原文 → 提交给 `PUT /user-groups/:name` 的那两个字段。
 *
 * ── 三态，每一条错了的方向都是收款金额 ──
 *
 *  - `clear`   输入框是空的 = **删掉这个键**，回落上游兜底（`1` + 一条 SysError）。
 *              它不是 0：写 0 意味着这一档人充值恒为 0 元。
 *  - `set`     敲进去的数（**含 0**）。0 是「这一档充值免费」，是一个合法且被
 *              项目方点名要能表达的配置。
 *  - `invalid` 解析不出数值 = 整条拒绝，**绝不提交**。提交一个 NaN 会让整份
 *              `TopupGroupRatio` 序列化成 `null`，全站充值折扣一起失效。
 *
 * 后端刻意用两个字段（`topup_ratio` 与 `clear_topup_ratio`）而不是一个可空值：
 * Go 里 `"topup_ratio": null` 与字段缺席都得到 nil 指针，两者不可区分，而
 * 「这次没打算改」与「回落到兜底」的后果不同。这个函数是那条契约的前端一侧。
 */
export type QyUgTopupInput =
  | { kind: 'clear' }
  | { kind: 'invalid' }
  | { kind: 'set'; value: number }

export function qyUgParseTopupInput(raw: string): QyUgTopupInput {
  const text = raw.trim()
  if (text === '') return { kind: 'clear' }
  // 只认十进制字面量。`1e3` / `0x10` / `Infinity` 都是合法的 JS 数字字面量，
  // 但它们出现在充值倍率里几乎必然是误输入，静默接受会把一个离谱的乘数写进收款。
  if (!/^\d+(\.\d+)?$/.test(text)) return { kind: 'invalid' }
  const value = Number(text)
  if (!Number.isFinite(value)) return { kind: 'invalid' }
  return { kind: 'set', value }
}

/**
 * 服务端下发的充值倍率 → 输入框里的原文。
 *
 * `null`（没配过）→ 空串，**不是 `"1"`**：预填那个 1 之后运营随手保存一遍，
 * 一档"没配过"就固化成一条显式记录，此后上游兜底再改也影响不到它，
 * 而没有任何人做过这个决定。倍率格上是同一条规则、同一个理由。
 */
export function qyUgTopupInputOf(ratio: number | null): string {
  return ratio == null ? '' : String(ratio)
}
