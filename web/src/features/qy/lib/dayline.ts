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
 * 结算日界的时间显示。
 *
 * ── 为什么要有这一个文件 ──
 *
 * 「结算日界 UTC+0」与「下一轮最早 {next} 开跑」原先出自两套时区系：前者由
 * `day_offset_minutes` 直接拼成 UTC±N，后者走 `formatTimestampToDate`，也就是
 * **浏览器本地时区**。而 `next_run_after` 是一个 UTC 日界瞬间，于是在
 * UTC-7 的机器上同一句话渲染成
 *
 *     结算日界 UTC+0 … 下一轮最早 2026-08-21 17:00:00 开跑
 *
 * ——日界写 UTC、时刻是本地时，两个数不在同一个时区系里，而且「最早开跑」的
 * 日期看上去比日界日期还早一天。瞬间本身是对的，坏的是口径。
 *
 * 这里把两半收进同一个地方：日界标签与时刻用**同一个偏移**渲染，并把偏移写在
 * 时刻后面。用户侧 `qy_aff_payout_eta_value_utc`（「结算日界为 UTC 零点，即北京
 * 时间 08:00」）早就是这个做法，管理端跟上。
 *
 * 刻意不引 dayjs 的 utc/timezone 插件：本仓的 `@/lib/dayjs` 只 extend 了
 * relativeTime，为一句文案拉进两个插件会让全站 bundle 与时区默认值一起改变，
 * 而这里要的只是「把一个 unix 秒按固定偏移写成墙钟」。
 */

/** 把 `day_offset_minutes` 渲染成 `UTC+8` / `UTC-3.5` / `UTC+0` 这样的标签。 */
export function qyDaylineLabel(offsetMinutes: number): string {
  const hours = offsetMinutes / 60
  return `UTC${hours >= 0 ? '+' : ''}${trimNumber(hours)}`
}

/**
 * 把一个 unix 秒按 `offsetMinutes` 这个固定偏移写成 `YYYY-MM-DD HH:mm:ss (UTC±N)`。
 *
 * 带上时区后缀是这个函数存在的全部理由：不带的话读者没法分辨它是本地时还是
 * 日界所在时区，而这两者恰恰在同一句话里并排出现。
 */
export function qyFormatAtDayline(
  unixSeconds: number,
  offsetMinutes: number
): string {
  if (!Number.isFinite(unixSeconds) || unixSeconds <= 0) return ''
  const shifted = new Date((unixSeconds + offsetMinutes * 60) * 1000)
  if (Number.isNaN(shifted.getTime())) return ''
  // toISOString 永远按 UTC 渲染，所以先把瞬间平移到目标偏移再取它的 UTC 墙钟。
  const wall = shifted.toISOString().slice(0, 19).replace('T', ' ')
  return `${wall} (${qyDaylineLabel(offsetMinutes)})`
}

/** 去掉 `8.0` 这种没有信息量的小数尾巴，但保留 `5.5` / `-3.5`。 */
function trimNumber(value: number): string {
  return Number.isInteger(value)
    ? String(value)
    : String(Number(value.toFixed(2)))
}
