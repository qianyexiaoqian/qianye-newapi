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
import { CheckCircle2, ImageUp, Loader2, X } from 'lucide-react'
import { useEffect, useRef, useState, type ChangeEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'

import { qyErrorMessage } from '../../../lib/api'
import { discardQyTicketImage, uploadQyTicketImage } from '../api'
import { qyBytesToMbLabel } from '../lib/priority'
import type { QyTicketImageItem } from '../lib/uploads'
import type { QyTicketConfig, QyTicketUpload } from '../types'

type Props = {
  config: QyTicketConfig
  disabled: boolean
  items: QyTicketImageItem[]
  onItemsChange: (next: QyTicketImageItem[]) => void
  /**
   * 上传实现。缺省走用户端接口；管理端传入 `/admin` 那一条。
   *
   * 做成 prop 而不是在组件里判"我是不是管理员"：这个控件不该知道自己长在哪个
   * 页面上，而两条上传接口的鉴权、审计动作类型都不同，猜错的方向是管理员的
   * 上传被记成用户行为。
   */
  upload?: (file: File) => Promise<QyTicketUpload>
  /**
   * 丢弃实现（把服务端那条未提交的上传行删掉）。缺省走用户端接口；
   * 管理端传入 `/admin` 那一条，理由与 `upload` 相同。
   */
  discard?: (ref: string) => Promise<void>
}

/**
 * 工单图片选择器（两步式的第一步）。
 *
 * 选中文件的那一刻就上传并换回一个 ref，提交正文时只带 ref。
 *
 * **移除一张已上传的图必须同时告诉服务端。** 只删本地那一项的话，那条未绑定的
 * 行会一直占着后端的"未提交上传数"名额（默认只有 image_max_per_message×2 = 6），
 * 而唯一的回收口径是 24 小时孤儿清理：用户"选了三张又关掉弹窗"两次之后，
 * 就再也传不了任何图，收到的提示还是"请先完成当前这条消息"—— 一个他此刻
 * 根本无法执行的下一步（他手上这条消息里一张图都没有）。丢弃失败不打断用户，
 * 那条行仍然会被孤儿清理兜底。
 *
 * 大小与类型在**选文件那一刻**先拦一道。这不是安全边界（服务端按魔数判定才是），
 * 而是为了不让用户把 9 MiB 上行跑完之后才收到一个 413。
 */
export function QyTicketImagePicker(props: Props) {
  const { t } = useTranslation()
  const inputRef = useRef<HTMLInputElement>(null)
  /** 本地预检失败的 i18n key。与上传请求的失败分开存，两者不会同时成立。 */
  const [localErrorKey, setLocalErrorKey] = useState<string | null>(null)

  const maxMb = qyBytesToMbLabel(props.config.image_max_bytes)
  const remaining = props.config.image_max_per_message - props.items.length

  const upload = async (
    item: QyTicketImageItem,
    current: QyTicketImageItem[]
  ) => {
    // 用函数式更新的等价物：每一次都从"最新的那份列表"里按 key 定位，
    // **并且立刻把结果写回 currentRef**。
    //
    // 少了那一行回写，两张图并发上传时就会丢结果：A 的 patch 先跑、调用
    // onItemsChange，但 React 还没提交、下面那个 useEffect 也就还没把 currentRef
    // 刷新；紧接着 B 的 patch 读到的仍是"两张都在 uploading"的旧快照，A 的 ref
    // 与 uploading:false 被整段覆盖回去。后果不是少一张图那么轻：A 永远停在
    // uploading:true → qyImagesSettled 恒为 false → 提交按钮永久禁用，而那一行的
    // ✕ 也带着 disabled={item.uploading}，用户唯一的出路是关掉弹窗、丢掉已经
    // 写好的正文重来。
    const patch = (next: Partial<QyTicketImageItem>) => {
      const updated = currentRef.current.map((it) =>
        it.key === item.key ? { ...it, ...next } : it
      )
      currentRef.current = updated
      props.onItemsChange(updated)
    }
    currentRef.current = current
    try {
      const data = await (props.upload ?? uploadQyTicketImage)(item.file)
      patch({ ref: data.ref, uploading: false, error: null })
    } catch (error) {
      patch({ ref: null, uploading: false, error })
    }
  }

  // 保存最新的一份列表供异步回调读取。用 ref 而不是把 items 写进依赖：
  // 上传是一次性的副作用，重新绑定回调不会让已经在飞的请求换一个目标。
  const currentRef = useRef(props.items)
  useEffect(() => {
    currentRef.current = props.items
  }, [props.items])

  const handlePick = (event: ChangeEvent<HTMLInputElement>) => {
    const picked = [...(event.target.files ?? [])]
    // 立刻清空 input：不清的话"移除一张 → 又选回同一张"不会再触发 change，
    // 用户会以为控件卡住了。
    event.target.value = ''
    if (picked.length === 0) return

    if (picked.length > remaining) {
      setLocalErrorKey('qy_tk_err_image_too_many_local')
      return
    }
    const tooLarge = picked.some(
      (file) => file.size > props.config.image_max_bytes
    )
    if (tooLarge) {
      setLocalErrorKey('qy_tk_err_image_too_large_local')
      return
    }
    // 空 type 不拦：部分系统对 webp 之类不回 MIME，而服务端按魔数判定不会看走眼。
    const badType = picked.some(
      (file) =>
        file.type !== '' && !props.config.image_accept.includes(file.type)
    )
    if (badType) {
      setLocalErrorKey('qy_err_tk_image_type')
      return
    }

    setLocalErrorKey(null)
    const added: QyTicketImageItem[] = picked.map((file, index) => ({
      key: `${Date.now()}-${index}-${file.name}`,
      file,
      ref: null,
      uploading: true,
      error: null,
    }))
    const next = [...props.items, ...added]
    props.onItemsChange(next)
    added.forEach((item) => void upload(item, next))
  }

  const remove = (key: string) => {
    setLocalErrorKey(null)
    const target = props.items.find((item) => item.key === key)
    const next = props.items.filter((item) => item.key !== key)
    currentRef.current = next
    props.onItemsChange(next)
    if (target?.ref != null) {
      // 不 await、也不报错：名额回收失败不该挡住"我不想要这张图了"这个动作，
      // 而那条行仍有 24 小时孤儿清理兜底。
      void (props.discard ?? discardQyTicketImage)(target.ref).catch(() => {})
    }
  }

  if (!props.config.image_enabled) return null

  return (
    <div className='space-y-1.5'>
      <Label>{t('qy_tk_images_label')}</Label>
      <p className='text-muted-foreground text-xs'>
        {t('qy_tk_images_hint', {
          max: props.config.image_max_per_message,
          mb: maxMb,
        })}
      </p>

      <input
        ref={inputRef}
        type='file'
        multiple
        className='hidden'
        accept={props.config.image_accept.join(',')}
        disabled={props.disabled}
        onChange={handlePick}
      />

      <Button
        type='button'
        variant='outline'
        size='sm'
        disabled={props.disabled || remaining <= 0}
        onClick={() => inputRef.current?.click()}
      >
        <ImageUp aria-hidden='true' />
        {t('qy_tk_images_pick')}
      </Button>

      {props.items.length > 0 && (
        <ul className='space-y-1.5'>
          {props.items.map((item) => (
            <li
              key={item.key}
              className='flex items-center gap-2 rounded-md border px-2.5 py-1.5'
            >
              <span className='min-w-0 flex-1 truncate text-sm'>
                {item.file.name}
              </span>
              {item.uploading && (
                <Loader2
                  className='text-muted-foreground size-3.5 animate-spin'
                  aria-hidden='true'
                />
              )}
              {item.ref != null && (
                <CheckCircle2
                  className='text-muted-foreground size-3.5'
                  aria-hidden='true'
                />
              )}
              {item.error != null && (
                <span className='text-destructive max-w-[16rem] truncate text-xs'>
                  {qyErrorMessage(item.error, t)}
                </span>
              )}
              <Button
                type='button'
                variant='ghost'
                size='icon-sm'
                aria-label={t('qy_tk_images_remove')}
                disabled={props.disabled || item.uploading}
                onClick={() => remove(item.key)}
              >
                <X aria-hidden='true' />
              </Button>
            </li>
          ))}
        </ul>
      )}

      {localErrorKey != null && (
        <p className='text-destructive text-xs'>
          {t(localErrorKey, {
            max: props.config.image_max_per_message,
            mb: maxMb,
          })}
        </p>
      )}
    </div>
  )
}
