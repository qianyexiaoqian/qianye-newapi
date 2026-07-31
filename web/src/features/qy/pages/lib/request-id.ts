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
import { nanoid } from 'nanoid'
import { useCallback, useRef } from 'react'

/**
 * 资金类请求的幂等键。
 *
 * 用 nanoid 而不是 `crypto.randomUUID()`：后端 `buildIdemKey`
 * （`qianye/modules/transfer/validate.go`）只接受 `[A-Za-z0-9_-]`，而 nanoid 的
 * 默认字母表恰好就是这一组；UUID 的连字符虽然也在白名单里，但 36 字符更接近
 * `idem_key varchar(96)` 的截断边界。
 */
export function newQyRequestId(): string {
  return nanoid()
}

/**
 * 幂等键的生命周期管理。
 *
 * **键必须在"打开确认弹窗"那一刻生成并缓存住，重试沿用同一个**（裁定 C10）。
 * 若在点击提交时才生成，一次网络超时后的重试会带上新的 key，后端唯一索引认不出
 * 这是同一笔，用户会被扣两次钱。
 *
 * 用 ref 而不是 state：这个值只在提交时被读取，不参与渲染。放进 state 会让每次
 * 换键都多一次无意义的重渲染，还会引入"setState 尚未生效就被读到"的时序坑。
 */
export function useQyRequestId(): {
  /** 读取当前键。同一笔业务的每次重试都会拿到同一个值。 */
  peek: () => string
  /** 换一个新键并返回。仅在开启新一笔（打开弹窗）或上一笔成功后调用。 */
  renew: () => string
} {
  const current = useRef<string>(newQyRequestId())

  const peek = useCallback(() => current.current, [])
  const renew = useCallback(() => {
    current.current = newQyRequestId()
    return current.current
  }, [])

  return { peek, renew }
}
