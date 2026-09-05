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
import { useMutation } from '@tanstack/react-query'
import { ImageUp, Loader2, X } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'

import { qyErrorMessage } from '../../../lib/api'
import { QyMallCover } from '../../mall/components/mall-cover'
import type { QyMallProductKind } from '../../mall/types'
import { discardQyMallCover, uploadQyMallCover } from '../api'

/** 后端 `errCoverType`：只接受这三种。 */
const ACCEPT_MIME = 'image/jpeg,image/png,image/webp'

export type QyMallCoverValue = {
  /** 要随商品保存的引用；空 = 无封面。 */
  cover_ref: string
  /** 已保存商品的现有封面地址（后端解析好的），只用于预览。 */
  cover_url: string
}

/**
 * 商品封面这一格：上传按钮 + 预览（形状照 `admin-lottery/components/lottery-cover-field.tsx`）。
 *
 * 与抽奖封面的差别只有一处：商品**只有上传**这一种来源（契约 §5 的
 * `cover_ref`，没有外链字段），所以没有地址输入框，也就没有"两种来源互斥"
 * 那一段逻辑。
 *
 * 预览用刚上传的那个 File（`createObjectURL`）而不是回服务器取：还没绑到商品
 * 上的上传只属于上传者，公开取图端点不会回它。换图或清掉时把**本组件这一轮**
 * 传上去的 ref 退还（待用上传有配额）；编辑既有商品时传进来的 `cover_ref`
 * 已经绑在商品上，退它只会得到「图片不存在」。
 */
export function QyMallCoverField(props: {
  value: QyMallCoverValue
  kind: QyMallProductKind
  onChange: (next: QyMallCoverValue) => void
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const fileInput = useRef<HTMLInputElement>(null)
  const uploadedRefs = useRef<Set<string>>(new Set())

  const [localPreview, setLocalPreview] = useState<string | null>(null)
  useEffect(() => {
    return () => {
      if (localPreview != null) URL.revokeObjectURL(localPreview)
    }
  }, [localPreview])

  const releaseIfOurs = (ref: string) => {
    if (ref === '' || !uploadedRefs.current.has(ref)) return
    uploadedRefs.current.delete(ref)
    void discardQyMallCover(ref).catch(() => {
      // 退不掉不影响本次保存：那张图会在宽限期之后被回收任务收走。
    })
  }

  const upload = useMutation({
    mutationFn: (file: File) => uploadQyMallCover(file),
    onSuccess: (data, file) => {
      releaseIfOurs(props.value.cover_ref)
      uploadedRefs.current.add(data.ref)
      setLocalPreview((prev) => {
        if (prev != null) URL.revokeObjectURL(prev)
        return URL.createObjectURL(file)
      })
      props.onChange({ cover_ref: data.ref, cover_url: '' })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const clear = () => {
    releaseIfOurs(props.value.cover_ref)
    setLocalPreview((prev) => {
      if (prev != null) URL.revokeObjectURL(prev)
      return null
    })
    props.onChange({ cover_ref: '', cover_url: '' })
  }

  const hasCover = props.value.cover_ref !== ''

  return (
    <div className='space-y-2'>
      <Label>{t('qy_mladm_cover')}</Label>

      {localPreview != null ? (
        <div className='bg-muted aspect-[16/6] w-full overflow-hidden'>
          <img
            src={localPreview}
            alt={t('qy_ml_cover_alt')}
            className='size-full object-cover'
          />
        </div>
      ) : (
        <QyMallCover
          product={{
            cover_url: hasCover ? props.value.cover_url : '',
            kind: props.kind,
          }}
        />
      )}

      <div className='flex flex-wrap items-center gap-2'>
        <input
          ref={fileInput}
          type='file'
          accept={ACCEPT_MIME}
          className='hidden'
          onChange={(event) => {
            const file = event.target.files?.[0]
            if (file != null) upload.mutate(file)
            // 清空 value，否则连续两次选**同一个文件**不会触发 change。
            event.target.value = ''
          }}
        />
        <Button
          type='button'
          size='sm'
          variant='outline'
          disabled={props.disabled || upload.isPending}
          onClick={() => fileInput.current?.click()}
        >
          {upload.isPending ? (
            <Loader2 className='size-4 animate-spin' aria-hidden='true' />
          ) : (
            <ImageUp className='size-4' aria-hidden='true' />
          )}
          {t('qy_mladm_cover_upload')}
        </Button>
        {hasCover && (
          <Button
            type='button'
            size='sm'
            variant='ghost'
            disabled={props.disabled}
            onClick={clear}
          >
            <X className='size-4' aria-hidden='true' />
            {t('qy_mladm_cover_clear')}
          </Button>
        )}
      </div>

      <p className='text-muted-foreground text-xs'>
        {t('qy_mladm_cover_hint')}
      </p>
    </div>
  )
}
