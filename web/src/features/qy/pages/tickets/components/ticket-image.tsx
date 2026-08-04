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
import { Loader2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { qyErrorMessage } from '../../../lib/api'
import { fetchQyTicketImageBlob } from '../api'

/**
 * 工单里的一张图片。
 *
 * **不能用 `<img src="/api/qy/ticket/images/xxx">`**：那条接口要 Bearer 头，
 * 浏览器给 `img` 发的请求不带它，结果是一个永远加载失败的破图。所以走 axios
 * 取 Blob 再 `createObjectURL`（与提现凭证同一条口径）。
 *
 * 刻意**不放进 react-query 缓存**：缓存会把图片按 key 留在全局内存里，而
 * `blob:` URL 的撤销时机必须与这个组件的生命周期绑死。放在 effect 里，
 * cleanup 就是唯一的撤销点，弹窗一关就释放。
 */
export function QyTicketImage(props: {
  imageRef: string
  scope?: 'admin' | 'user'
  className?: string
}) {
  const { t } = useTranslation()
  const [url, setUrl] = useState<string | null>(null)
  // 存错误对象而不是翻译好的字符串：存字符串就得把 `t` 写进 effect 依赖，
  // 于是切一次语言就会为每一张图重新发一次请求。
  const [error, setError] = useState<unknown>(null)
  const [loading, setLoading] = useState(true)

  const scope = props.scope ?? 'user'

  useEffect(() => {
    let objectUrl: string | null = null
    let cancelled = false

    setUrl(null)
    setError(null)
    setLoading(true)

    const load = async () => {
      try {
        const blob = await fetchQyTicketImageBlob(props.imageRef, scope)
        // 已经卸载：不要再造 URL，否则它没有任何撤销点。
        if (cancelled) return
        objectUrl = URL.createObjectURL(blob)
        setUrl(objectUrl)
      } catch (err) {
        if (!cancelled) setError(err)
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void load()

    return () => {
      cancelled = true
      if (objectUrl != null) URL.revokeObjectURL(objectUrl)
    }
  }, [props.imageRef, scope])

  if (loading) {
    return (
      <span className='text-muted-foreground flex items-center gap-1.5 text-xs'>
        <Loader2 className='size-3 animate-spin' aria-hidden='true' />
        {t('qy_tk_image_loading')}
      </span>
    )
  }
  if (error != null) {
    // 保留期到期之后图片就没了，这不是故障 —— 后端用 qy_tk_image_purged
    // 明确作答，这里如实转述，不要说成"加载失败请重试"。
    return (
      <span className='text-muted-foreground text-xs'>
        {qyErrorMessage(error, t)}
      </span>
    )
  }
  if (url == null) return null

  return (
    // blob: URL 只进 img.src，不做链接、不新开标签页 —— 工单里的图可能是用户
    // 上传的身份材料，多一个"在新标签页打开"就多一个会留在历史记录里的地址。
    <img
      src={url}
      alt={t('qy_tk_image_alt')}
      className={
        props.className ??
        'bg-muted max-h-56 w-auto max-w-full rounded border object-contain'
      }
    />
  )
}
