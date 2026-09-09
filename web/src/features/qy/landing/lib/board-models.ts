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
import { QY_LANDING_FAMILIES } from '../constants'

/** `/api/pricing` 里本页真正用得上的那一个字段。 */
type BoardCandidate = { model_name?: string }

/**
 * 挑出信号板上要显示的模型名。
 *
 * 首屏那块板子上写的是**本站真的在卖的模型**，不是示意图 —— 所以它读
 * `/api/pricing`（公开端点，定价页已经在用同一个 queryKey，两页共享缓存）。
 *
 * 排序不是原样截断，而是「三支主推族优先，族内保持目录顺序，再拿其余的补满」：
 * 页面正文写的是「Claude · Gemini · GPT 全系官方保真」，而目录顺序是后台
 * 建渠道的顺序 —— 直接截前七条，板子上很可能一个 Claude 都没有，图文当场对不上。
 *
 * 目录取不到 / 未开放游客预览时返回空数组，由调用方落到兜底行；这里**不**编造
 * 模型名，站点没上架的东西不该出现在首页上。
 */
export function pickBoardModels(
  models: readonly BoardCandidate[] | undefined,
  limit: number
): string[] {
  if (!models || limit <= 0) return []

  const seen = new Set<string>()
  const names: string[] = []
  for (const model of models) {
    const name = model.model_name?.trim()
    if (!name || seen.has(name)) continue
    seen.add(name)
    names.push(name)
  }

  const rank = (name: string) => {
    const lower = name.toLowerCase()
    const index = QY_LANDING_FAMILIES.findIndex((f) => lower.includes(f.id))
    return index === -1 ? QY_LANDING_FAMILIES.length : index
  }

  return names
    .map((name, index) => ({ name, rank: rank(name), index }))
    .sort((a, b) => a.rank - b.rank || a.index - b.index)
    .slice(0, limit)
    .map((entry) => entry.name)
}
