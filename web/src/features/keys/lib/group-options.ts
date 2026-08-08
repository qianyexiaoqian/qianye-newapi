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
 * 用户建令牌时那个分组下拉的候选项。
 *
 * ── 备注（`desc`）从哪来 ──
 *
 * 数据源是 `GET /api/user/self/groups`，它对每个可选的模型分组返回
 * `{ ratio, desc }`。`desc` 是**服务端按优先级解析之后的最终结果**：
 *
 *   按格备注（用户分组 × 模型分组，`qy_group_grants.note`）
 *     > 模型分组的分组备注（`qy_model_groups.note`）
 *     > 历史 `options.UserUsableGroups` 里那句原文
 *
 * 项目方原话：「用户分组分配模型分组的时候若不填写则显示分组备注（默认），
 * 若填写以用户分组的模型分组备注优先显示」。
 *
 * ── 优先级为什么**必须**在服务端解析，前端不能自己拼 ──
 *
 * 第一层的键是 (这个用户此刻的用户分组, 模型分组)。用户侧根本没有、也不该有
 * 一份「谁属于哪一档」的映射：把它下发到前端，等于把全站的分档结构暴露给每一个
 * 登录用户；而不下发，前端就只能拿到第二、三层，于是运营在按格备注里写的那句
 * 话在用户界面上永远不出现，而管理端显示得好好的。
 *
 * 所以这一层只做一件事：把服务端给的那一份原样铺成下拉项，**不在这里做任何
 * 回退推断**。唯一的兜底是 `desc` 为空时用分组名占位 —— 那是排版兜底，
 * 不是语义兜底。
 */

/** `/api/user/self/groups` 的一项。`ratio` 在 `auto` 上是「自动」这样的字符串。 */
export type ApiKeyGroupInfo = {
  desc: string
  ratio: number | string
}

export type ApiKeyGroupOptionData = {
  value: string
  label: string
  desc: string
  ratio: number | string
}

/**
 * 服务端 map → 下拉项。
 *
 * `desc` 为空时回落成分组名：下拉项的第二行是排版的一部分，空着会让相邻两项
 * 高度不一，而这一列本来就在窄屏上截断。回落**不改变语义** —— 名字与备注为空
 * 说的是同一件事（这个分组没有配备注）。
 *
 * 键的顺序按名字升序：`Object.entries` 给的是插入顺序，而那是 Go map 序列化的
 * 顺序，两次请求可能不同 —— 下拉里的选项在刷新之后换位置，用户会点错。
 */
export function buildApiKeyGroupOptions(
  data: Readonly<Record<string, ApiKeyGroupInfo>> | undefined
): ApiKeyGroupOptionData[] {
  const entries = Object.entries(data ?? {})
  entries.sort(([a], [b]) => a.localeCompare(b))
  const options: ApiKeyGroupOptionData[] = []
  for (const [name, info] of entries) {
    options.push({
      value: name,
      label: name,
      desc: info.desc === '' ? name : info.desc,
      ratio: info.ratio,
    })
  }
  return options
}
