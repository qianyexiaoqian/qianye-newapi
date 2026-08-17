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
import { useEffect, useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

import { qyErrorMessage } from '../../../lib/api'
import { QyLotCover } from '../../lottery/components/lottery-cover'
import { discardQyLotCover, uploadQyLotCover } from '../api'
import type { QyLotAdminConfig } from '../types'

export type QyLotCoverValue = { cover_ref: string; cover_url: string }

/**
 * 「卡片背景图」这一格：地址输入 + 上传按钮 + 预览。
 *
 * ## 两种来源在界面上是**一格**而不是两格
 *
 * 后端对"外链与上传同时给值"是直接 400（同时给了两个，显示哪一张只能由代码里
 * 的先后顺序回答，而那是最坏的一种规则来源）。做成两格并列的表单，运营会
 * 两边都填，然后在保存时吃一个他看不懂的错。所以这里是一格：填地址会清掉
 * 已选的图，选图会清掉地址，任何时刻只有一个来源是活的。
 *
 * ## 预览为什么用刚上传的那个 File 而不是回服务器取
 *
 * 刚上传的封面还**没有落到任何活动上**，而取回接口只回已绑定的那些（未绑定的
 * 上传只属于上传者，匿名可取等于把管理员挑到一半的图公开出去）。本地
 * `createObjectURL` 既准确又免掉一次往返，代价是刷新页面后未保存的那张预览
 * 会消失 —— 那正确地反映了"它还没被保存进任何活动"。
 *
 * ## 换图时必须把上一张退掉
 *
 * 待用上传有配额（防"只传图、不保存活动"把磁盘打满）。不退的话，十次
 * "选了又换"之后管理员在 24 小时宽限期到期前再也传不了图，而提示语让他
 * "先保存活动" —— 一个他此刻无法执行的动作。
 *
 * 只退**本组件这一轮传上去的** ref（记在 `uploadedRefs` 里）：编辑既有活动时
 * 传进来的 `cover_ref` 已经绑在那场活动上，退它只会得到一个"图片不存在"的报错。
 */
export function QyLotCoverField(props: {
  value: QyLotCoverValue
  onChange: (next: QyLotCoverValue) => void
  config: QyLotAdminConfig | undefined
  /** 详情页的换图弹窗里用 `hero`，向导里用默认的 `banner`。 */
  variant?: 'banner' | 'hero'
}) {
  const { t } = useTranslation()
  const id = useId()
  const fileInput = useRef<HTMLInputElement>(null)
  const uploadedRefs = useRef<Set<string>>(new Set())

  const yaml = props.config?.yaml_readonly
  const uploadOn = yaml?.cover_enabled ?? true
  const maxBytes = yaml?.cover_max_bytes ?? 0
  const accept = (yaml?.cover_accept_mime ?? []).join(',')

  // 刚上传那张图的本地预览地址。它的撤销时机必须与本组件的生命周期绑死，
  // 所以放在 state + effect cleanup 里，而不是塞进任何全局缓存。
  const [localPreview, setLocalPreview] = useState<string | null>(null)
  useEffect(() => {
    return () => {
      if (localPreview != null) URL.revokeObjectURL(localPreview)
    }
  }, [localPreview])

  /** 把这一轮自己传上去、又不再使用的那张退还给服务端。失败只记日志。 */
  const releaseIfOurs = (ref: string) => {
    if (ref === '' || !uploadedRefs.current.has(ref)) return
    uploadedRefs.current.delete(ref)
    void discardQyLotCover(ref).catch(() => {
      // 退不掉不影响本次保存：那张图会在 24 小时宽限期之后被回收任务收走。
      // 所以这里刻意不打扰运营 —— 他此刻正在填表单。
    })
  }

  const upload = useMutation({
    mutationFn: (file: File) => uploadQyLotCover(file),
    onSuccess: (data, file) => {
      releaseIfOurs(props.value.cover_ref)
      uploadedRefs.current.add(data.ref)
      setLocalPreview((prev) => {
        if (prev != null) URL.revokeObjectURL(prev)
        return URL.createObjectURL(file)
      })
      // 选了图就清掉地址：两种来源互斥，留着那个地址只会在保存时吃一个 400。
      props.onChange({ cover_ref: data.ref, cover_url: '' })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const pick = (file: File | undefined) => {
    if (file == null) return
    // 本地先拦一道。让运营把 5 MiB 传完再看到 413 是最贵的一种拒绝方式 ——
    // 但它**不是**权威:真正的判定是服务端的 MaxBytesReader 与魔数。
    if (maxBytes > 0 && file.size > maxBytes) {
      toast.error(
        t('qy_lot_cover_too_large_local', {
          max: Math.floor(maxBytes / 1024),
        })
      )
      return
    }
    upload.mutate(file)
  }

  const clear = () => {
    releaseIfOurs(props.value.cover_ref)
    setLocalPreview((prev) => {
      if (prev != null) URL.revokeObjectURL(prev)
      return null
    })
    props.onChange({ cover_ref: '', cover_url: '' })
  }

  const hasCover =
    props.value.cover_ref !== '' || props.value.cover_url.trim() !== ''

  return (
    <div className='space-y-2'>
      <Label htmlFor={`${id}-url`}>{t('qy_lot_cover')}</Label>

      {/* 预览始终在:没配封面时 QyLotCover 画的是兜底图案,而那正是用户在大厅
          里会看到的东西 —— 让运营先看一眼再决定要不要配,比一句说明有用。 */}
      {localPreview != null ? (
        <div className='bg-muted w-full overflow-hidden aspect-[16/6]'>
          <img
            src={localPreview}
            alt={t('qy_lot_cover_alt')}
            className='size-full object-cover'
          />
        </div>
      ) : (
        <QyLotCover
          activity={{
            cover_ref: props.value.cover_ref,
            cover_url: props.value.cover_url,
          }}
          variant={props.variant}
        />
      )}

      <Input
        id={`${id}-url`}
        value={props.value.cover_url}
        placeholder={t('qy_lot_cover_url_placeholder')}
        maxLength={500}
        onChange={(event) => {
          const next = event.target.value
          // 敲地址就把已选的图退掉:两种来源互斥,而"我明明传了图"与
          // "我明明填了地址"同时成立时,保存会 400。
          if (next.trim() !== '' && props.value.cover_ref !== '') {
            releaseIfOurs(props.value.cover_ref)
            setLocalPreview((prev) => {
              if (prev != null) URL.revokeObjectURL(prev)
              return null
            })
          }
          props.onChange({
            cover_ref: next.trim() === '' ? props.value.cover_ref : '',
            cover_url: next,
          })
        }}
      />

      <div className='flex flex-wrap items-center gap-2'>
        {uploadOn && (
          <>
            <input
              ref={fileInput}
              type='file'
              accept={accept === '' ? 'image/*' : accept}
              className='hidden'
              onChange={(event) => {
                pick(event.target.files?.[0])
                // 清空 value,否则连续两次选**同一个文件**不会触发 change。
                event.target.value = ''
              }}
            />
            <Button
              type='button'
              size='sm'
              variant='outline'
              disabled={upload.isPending}
              onClick={() => fileInput.current?.click()}
            >
              {upload.isPending ? (
                <Loader2 className='size-4 animate-spin' aria-hidden='true' />
              ) : (
                <ImageUp className='size-4' aria-hidden='true' />
              )}
              {t('qy_lot_cover_upload')}
            </Button>
          </>
        )}
        {hasCover && (
          <Button type='button' size='sm' variant='ghost' onClick={clear}>
            <X className='size-4' aria-hidden='true' />
            {t('qy_lot_cover_clear')}
          </Button>
        )}
      </div>

      <p className='text-muted-foreground text-xs'>
        {uploadOn
          ? t('qy_lot_cover_hint', { max: Math.floor(maxBytes / 1024) })
          : t('qy_lot_cover_hint_link_only')}
      </p>
    </div>
  )
}
