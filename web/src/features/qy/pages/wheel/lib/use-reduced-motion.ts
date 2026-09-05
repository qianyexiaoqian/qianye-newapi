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

For commercial licensing, please contact support@quantumnous.com
*/
import { useEffect, useState } from 'react'

const QUERY = '(prefers-reduced-motion: reduce)'

function readReducedMotion(): boolean {
  try {
    return globalThis.matchMedia?.(QUERY)?.matches === true
  } catch {
    return false
  }
}

/**
 * 用户是否要求缩减动效。design-14 §4：`prefers-reduced-motion: reduce` 下停掉位移
 * 与过渡；转盘在这一档下不旋转、直接显示结果。
 *
 * 读不到 `matchMedia`（非浏览器环境）时按"不缩减"处理 —— 那只影响一段 2 秒的
 * 过渡，不影响结果。
 */
export function useQyReducedMotion(): boolean {
  const [reduced, setReduced] = useState(readReducedMotion)

  useEffect(() => {
    let media: MediaQueryList | undefined
    try {
      media = globalThis.matchMedia?.(QUERY)
    } catch {
      media = undefined
    }
    if (media == null || typeof media.addEventListener !== 'function') return
    const onChange = () => setReduced(media.matches)
    media.addEventListener('change', onChange)
    return () => media.removeEventListener('change', onChange)
  }, [])

  return reduced
}
