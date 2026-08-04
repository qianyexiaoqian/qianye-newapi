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
import { useEffect, useState } from 'react'

/**
 * 每秒一跳的当前 unix 秒。
 *
 * 倒计时用**客户端时钟**是刻意的：它只影响"还有多久"这句话好不好看。
 * 真正说了算的是服务端的 `close_at` / `draw_at` —— 用户把系统时间调快一小时
 * 也不会因此提前封盘或提前开奖，最多是他自己看到的倒计时不准。
 *
 * 因此本 hook 的结果**绝不能**用来决定"能不能提交"：按钮亮不亮可以参考它，
 * 但提交后的判定全在服务端的活动行锁里。
 */
export function useQyNowSeconds(enabled = true): number {
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))

  useEffect(() => {
    if (!enabled) return
    const timer = setInterval(() => {
      setNow(Math.floor(Date.now() / 1000))
    }, 1000)
    return () => clearInterval(timer)
  }, [enabled])

  return now
}
