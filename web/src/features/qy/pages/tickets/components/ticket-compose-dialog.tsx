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
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyOpsErrorMessage } from '../../ops/errors'
import { createQyTicket } from '../api'
import { QY_TICKET_PRIORITIES, getQyTicketPriorityStyle } from '../lib/priority'
import {
  qyImageRefs,
  qyImagesSettled,
  type QyTicketImageItem,
} from '../lib/uploads'
import type { QyTicketConfig, QyTicketPriority } from '../types'
import { QyTicketImagePicker } from './ticket-image-picker'
import { QyTicketMarkdownHint } from './ticket-markdown-hint'

/**
 * 新建工单。
 *
 * 长度一律用 `Array.from` 数码点而不是 `String.length`：后端按 rune 计数，
 * 一个 emoji 在 `length` 里是 2 而在 rune 里是 1，用 `length` 会让前端提前
 * 报超长（用户对着一个还没写满的输入框被拦），或者反过来放行之后被后端拒。
 */
export function QyTicketComposeDialog(props: {
  open: boolean
  config: QyTicketConfig
  onOpenChange: (open: boolean) => void
  onCreated: () => void
}) {
  const { t } = useTranslation()
  const titleId = useId()
  const bodyId = useId()

  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')
  const [priority, setPriority] = useState<QyTicketPriority>('normal')
  const [images, setImages] = useState<QyTicketImageItem[]>([])

  // 关掉弹窗就清空。不清的话，用户下一次点"新建"会看到上一张单的草稿，
  // 而他很可能直接提交 —— 一张内容对不上标题的工单。
  useEffect(() => {
    if (props.open) return
    setTitle('')
    setBody('')
    setPriority('normal')
    setImages([])
  }, [props.open])

  const titleRunes = [...title.trim()].length
  const bodyRunes = [...body.trim()].length
  const titleTooLong = titleRunes > props.config.title_max_runes
  const bodyTooLong = bodyRunes > props.config.body_max_runes

  const mutation = useMutation({
    mutationFn: () =>
      createQyTicket({
        title: title.trim(),
        body: body.trim(),
        priority,
        attachment_refs: qyImageRefs(images),
      }),
    onSuccess: () => {
      toast.success(t('qy_tk_created'))
      props.onCreated()
      props.onOpenChange(false)
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const canSubmit =
    titleRunes > 0 &&
    bodyRunes > 0 &&
    !titleTooLong &&
    !bodyTooLong &&
    qyImagesSettled(images) &&
    !mutation.isPending

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_tk_new_title')}
      description={t('qy_tk_new_desc')}
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('qy_common_cancel')}
          </Button>
          <Button
            type='button'
            disabled={!canSubmit}
            onClick={() => mutation.mutate()}
          >
            {t('qy_common_submit')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        <div className='space-y-1.5'>
          <Label htmlFor={titleId}>{t('qy_tk_field_title')}</Label>
          <Input
            id={titleId}
            value={title}
            maxLength={props.config.title_max_runes * 2}
            placeholder={t('qy_tk_field_title_ph')}
            onChange={(event) => setTitle(event.target.value)}
          />
          <p
            className={
              titleTooLong
                ? 'text-destructive text-xs'
                : 'text-muted-foreground text-xs'
            }
          >
            {/* 参数名刻意不叫 `count`：i18next 见到 count 会走复数解析，
                去找 `_one` / `_other` 后缀的键，平白多一层回落。 */}
            {t('qy_tk_runes', {
              n: titleRunes,
              max: props.config.title_max_runes,
            })}
          </p>
        </div>

        <div className='space-y-1.5'>
          <Label>{t('qy_tk_field_priority')}</Label>
          <Select
            value={priority}
            onValueChange={(value) => setPriority(value as QyTicketPriority)}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {QY_TICKET_PRIORITIES.map((item) => (
                <SelectItem key={item} value={item}>
                  {t(getQyTicketPriorityStyle(item).labelKey)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className='text-muted-foreground text-xs'>
            {t('qy_tk_field_priority_hint')}
          </p>
        </div>

        <div className='space-y-1.5'>
          <Label htmlFor={bodyId}>{t('qy_tk_field_body')}</Label>
          <Textarea
            id={bodyId}
            rows={8}
            value={body}
            placeholder={t('qy_tk_field_body_ph')}
            onChange={(event) => setBody(event.target.value)}
          />
          <div className='flex flex-wrap items-center justify-between gap-2'>
            <QyTicketMarkdownHint />
            <p
              className={
                bodyTooLong
                  ? 'text-destructive text-xs'
                  : 'text-muted-foreground text-xs'
              }
            >
              {t('qy_tk_runes', {
                n: bodyRunes,
                max: props.config.body_max_runes,
              })}
            </p>
          </div>
        </div>

        <QyTicketImagePicker
          config={props.config}
          disabled={mutation.isPending}
          items={images}
          onItemsChange={setImages}
        />
      </div>
    </QyResponsiveDialog>
  )
}
