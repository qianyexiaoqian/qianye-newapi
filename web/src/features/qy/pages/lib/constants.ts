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
 * 资金类页面的共享常量。
 *
 * 分页大小与后端各模块 `paginate()` / `pageParams()` 的默认值一致
 * （`qianye/modules` 下的 `http.go`、`handler.go`、`api_user.go`）。
 * 后端上限是 100，前端不提供切换：这些列表都是流水，
 * 一次拉 100 行只会让移动端卡片视图变成无限滚动条。
 */
export const QY_PAGE_SIZE = 20

/**
 * 备注 / 说明的默认字符上限。
 *
 * 真实上限来自 `/api/qy/config` 的 `withdraw_options.remark_max_runes`
 * 与 `/withdraw/config`；这里只是接口未返回时的兜底，与后端 `checkRunes`
 * 的 `max <= 0 → 200` 分支保持一致。
 */
export const QY_REMARK_MAX_RUNES = 200

/** 划转备注上限。后端 `maxRemarkRunes` 是写死的 200，不随配置变化。 */
export const QY_TRANSFER_REMARK_MAX_RUNES = 200

/**
 * 按字符数（而非 UTF-16 码元）统计长度。
 *
 * `'𝄞'.length === 2`、`'中'.length === 1`，而后端 `utf8.RuneCountInString`
 * 把两者都算 1。直接用 `.length` 会让用户在输入 emoji 时看到"还剩 198 字"
 * 却被后端拒绝。展开成码点数组是与后端口径唯一一致的算法。
 */
export function qyRuneLength(value: string): number {
  return [...value].length
}
