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
 * 一次批量上传的条数上限。
 *
 * 与 `qianye/config/qianye.example.yaml` 的 `mall.code_upload_max` 默认值同值。
 * 真正说了算的是后端（它读 YAML，运维可以改小），这里只是让运营在粘贴完
 * 就看到"超了"，而不是把 2000 行发上去再吃一个 400。管理端没有把这个值下发
 * 到前端的接口 —— 后端若把它加进某个配置端点，这里应改成读那个值。
 */
export const QY_MALL_CODE_UPLOAD_MAX = 500

/** 与后端一条码的长度上限同值（`≤128`，按 rune）。 */
export const QY_MALL_CODE_MAX_RUNES = 128

/**
 * 把粘贴进来的文本切成一行一条码。
 *
 * 只做两件事：按换行切、去掉首尾空白与空行。**不去重、不改大小写**：码是
 * 第三方发的凭据，`ABC` 与 `abc` 可能真的是两枚；重复行由后端逐条判成
 * rejected 报回来，运营看得到是第几行 —— 这里静默合并掉的话，他数出来的
 * 条数会与库里多出来的对不上。
 */
export function qyMallParseCodes(text: string): string[] {
  return text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line !== '')
}
