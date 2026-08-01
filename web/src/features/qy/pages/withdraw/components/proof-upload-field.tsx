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
import { CheckCircle2, ImageUp, Loader2, RotateCcw, X } from 'lucide-react'
import { useEffect, useRef, useState, type ChangeEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'

import { qyErrorMessage } from '../../../lib/api'
import { qyUploadWithdrawProof } from '../api'
import { QY_PROOF_NONE, type QyProofSelection } from '../lib/proof'
import type { QyWithdrawFiatConfig } from '../types'

type ProofUploadFieldProps = {
  fiat: QyWithdrawFiatConfig
  disabled: boolean
  selection: QyProofSelection
  onSelectionChange: (next: QyProofSelection) => void
}

/**
 * 提现凭证上传控件（两步式的第一步）。
 *
 * 选中文件的那一刻就上传并换回一个 ref，申请提交时只带 ref。因此这里的
 * 每一次"换一张图"都是一次独立的上传：上一张会变成孤儿，由后端 24 小时窗口的
 * 清理任务收走（`pruneProofs` 的 A 条），前端不需要、也不应该去删它 ——
 * 用户完全可能在换图之后又后悔换回来。
 *
 * 大小与类型都在**选文件那一刻**先拦一道。这不是安全边界（服务端按魔数判定才是），
 * 而是为了不让用户把 9 MiB 上行跑完之后才收到一个 413。
 */
export function ProofUploadField(props: ProofUploadFieldProps) {
  const { t } = useTranslation()
  const inputRef = useRef<HTMLInputElement>(null)

  const [file, setFile] = useState<File | null>(null)
  const [preview, setPreview] = useState<string | null>(null)
  /** 本地预检失败的 i18n key。与上传请求的失败分开存，两者不会同时成立。 */
  const [localErrorKey, setLocalErrorKey] = useState<string | null>(null)

  const maxBytes = props.fiat.proof_max_bytes
  const accept = props.fiat.proof_accept
  // 只用于文案。真正的判定用字节数，除下来的 MB 只是给人看的。
  const maxMb = Math.round((maxBytes / (1024 * 1024)) * 10) / 10

  /**
   * 预览 URL 的完整生命周期都在这一个 effect 里：换图时 cleanup 先撤销上一张，
   * 组件卸载时同样走 cleanup。**图片是 PII，撤销不是可选项** —— 不撤销的话
   * blob: URL 会一直挂在 document 上，直到整个页面被销毁。
   */
  useEffect(() => {
    if (file == null) {
      setPreview(null)
      return
    }
    const url = URL.createObjectURL(file)
    setPreview(url)
    return () => URL.revokeObjectURL(url)
  }, [file])

  const uploadMutation = useMutation({
    mutationFn: qyUploadWithdrawProof,
    onMutate: () => props.onSelectionChange({ ref: null, uploading: true }),
    onSuccess: (data) =>
      props.onSelectionChange({ ref: data.ref, uploading: false }),
    onError: () => props.onSelectionChange(QY_PROOF_NONE),
  })

  const handlePick = (event: ChangeEvent<HTMLInputElement>) => {
    const picked = event.target.files?.[0] ?? null
    // 立刻清空 input：不清的话"换一张图 → 又换回原来那张"不会再触发 change，
    // 用户会以为控件卡住了。
    event.target.value = ''
    if (picked == null) return

    uploadMutation.reset()
    if (picked.size > maxBytes) {
      setFile(null)
      setLocalErrorKey('qy_wd_proof_err_too_large')
      props.onSelectionChange(QY_PROOF_NONE)
      return
    }
    // 空 type 不拦：部分系统对 webp 之类不回 MIME，而服务端按魔数判定不会看走眼。
    if (picked.type !== '' && !accept.includes(picked.type)) {
      setFile(null)
      setLocalErrorKey('qy_err_wd_proof_type')
      props.onSelectionChange(QY_PROOF_NONE)
      return
    }

    setLocalErrorKey(null)
    setFile(picked)
    uploadMutation.mutate(picked)
  }

  const clear = () => {
    setFile(null)
    setLocalErrorKey(null)
    uploadMutation.reset()
    props.onSelectionChange(QY_PROOF_NONE)
  }

  // 本地预检的文案带得出具体上限（"超过 2 MB"），后端 code 的那一句只能是通用的
  // ——所以两者不合并。两条路的上限数字都来自同一个 fiat.proof_max_bytes。
  let errorText: string | null = null
  if (localErrorKey != null) {
    errorText = t(localErrorKey, { mb: maxMb })
  } else if (uploadMutation.isError) {
    errorText = qyErrorMessage(uploadMutation.error, t)
  }

  return (
    <div className='space-y-1.5'>
      <Label>{t('qy_wd_proof_label')}</Label>
      <p className='text-muted-foreground text-xs'>
        {t('qy_wd_proof_hint', { mb: maxMb })}
      </p>

      <input
        ref={inputRef}
        type='file'
        className='hidden'
        accept={accept.join(',')}
        disabled={props.disabled}
        onChange={handlePick}
      />

      {file == null ? (
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={props.disabled}
          onClick={() => inputRef.current?.click()}
        >
          <ImageUp aria-hidden='true' />
          {t('qy_wd_proof_pick')}
        </Button>
      ) : (
        <div className='flex items-start gap-3 rounded-md border p-2.5'>
          {preview != null && (
            // blob: URL 只进 img.src。**不做成链接、不新开标签页** —— 那会把一个
            // 指向 PII 的地址塞进地址栏与浏览历史。
            <img
              src={preview}
              alt={t('qy_wd_proof_alt')}
              className='bg-muted size-16 shrink-0 rounded object-cover'
            />
          )}
          <div className='min-w-0 flex-1 space-y-1.5'>
            <p className='truncate text-sm'>{file.name}</p>
            {props.selection.uploading && (
              <p className='text-muted-foreground flex items-center gap-1.5 text-xs'>
                <Loader2 className='size-3 animate-spin' aria-hidden='true' />
                {t('qy_wd_proof_uploading')}
              </p>
            )}
            {props.selection.ref != null && (
              <p className='text-muted-foreground flex items-center gap-1.5 text-xs'>
                <CheckCircle2 className='size-3' aria-hidden='true' />
                {t('qy_wd_proof_uploaded')}
              </p>
            )}
            <div className='flex flex-wrap gap-2'>
              {uploadMutation.isError && (
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  disabled={props.disabled}
                  onClick={() => uploadMutation.mutate(file)}
                >
                  <RotateCcw aria-hidden='true' />
                  {t('qy_wd_proof_retry')}
                </Button>
              )}
              <Button
                type='button'
                variant='outline'
                size='sm'
                disabled={props.disabled || props.selection.uploading}
                onClick={() => inputRef.current?.click()}
              >
                {t('qy_wd_proof_replace')}
              </Button>
              <Button
                type='button'
                variant='ghost'
                size='sm'
                disabled={props.disabled || props.selection.uploading}
                onClick={clear}
              >
                <X aria-hidden='true' />
                {t('qy_wd_proof_remove')}
              </Button>
            </div>
          </div>
        </div>
      )}

      {errorText != null && (
        <p className='text-destructive text-sm'>{errorText}</p>
      )}
    </div>
  )
}
