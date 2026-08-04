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
import { useMutation, useQuery } from '@tanstack/react-query'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { qyOpsErrorMessage } from '../../ops/errors'
import { formatQyTs } from '../../ops/format'
import { closeQyTicket, getQyTicket, replyQyTicket } from '../api'
import { getQyTicketPriorityStyle } from '../lib/priority'
import {
  qyImageRefs,
  qyImagesSettled,
  type QyTicketImageItem,
} from '../lib/uploads'
import type { QyTicketConfig } from '../types'
import { QyTicketImagePicker } from './ticket-image-picker'
import { QyTicketMarkdownHint } from './ticket-markdown-hint'
import { QyTicketThread } from './ticket-thread'

/**
 * 我的工单详情 + 追加回复。
 *
 * 详情**每次打开都重新取**（`staleTime: 0`）而不是复用列表里那一行：列表接口
 * 不带消息，而且客服可能在用户打开列表之后刚回过。拿一份缓存渲染的结果是
 * 用户看到"还没有人回复"，然后关掉页面去发第二张单。
 */
export function QyTicketDetailDialog(props: {
  /** 业务单号。用户端一律按它寻址 —— 列表下发的 `id` 恒为 0。 */
  ticketNo: string | null
  config: QyTicketConfig
  onClose: () => void
  onChanged: () => void
}) {
  const { t } = useTranslation()
  const replyId = useId()
  const [body, setBody] = useState('')
  const [images, setImages] = useState<QyTicketImageItem[]>([])

  const open = props.ticketNo != null
  const ticketNo = props.ticketNo ?? ''

  useEffect(() => {
    setBody('')
    setImages([])
  }, [props.ticketNo])

  const detailQuery = useQuery({
    queryKey: qyKeys.ticketDetail(ticketNo),
    queryFn: () => getQyTicket(ticketNo),
    enabled: open,
    staleTime: 0,
  })

  const ticket = detailQuery.data
  const closed = ticket?.status === 'closed'
  const bodyRunes = [...body.trim()].length
  const bodyTooLong = bodyRunes > props.config.body_max_runes

  const replyMutation = useMutation({
    mutationFn: () =>
      replyQyTicket(ticketNo, {
        body: body.trim(),
        attachment_refs: qyImageRefs(images),
      }),
    onSuccess: () => {
      toast.success(t('qy_tk_replied'))
      setBody('')
      setImages([])
      void detailQuery.refetch()
      props.onChanged()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const closeMutation = useMutation({
    mutationFn: () => closeQyTicket(ticketNo),
    onSuccess: () => {
      toast.success(t('qy_tk_closed_done'))
      void detailQuery.refetch()
      props.onChanged()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const canReply =
    !closed &&
    bodyRunes > 0 &&
    !bodyTooLong &&
    qyImagesSettled(images) &&
    !replyMutation.isPending

  return (
    <QyResponsiveDialog
      open={open}
      onOpenChange={(next) => {
        if (!next) props.onClose()
      }}
      title={ticket?.title ?? t('qy_tk_detail_title')}
      description={ticket?.ticket_no}
      contentClassName='sm:max-w-3xl'
      footer={
        <>
          <Button type='button' variant='outline' onClick={props.onClose}>
            {t('qy_common_close')}
          </Button>
          {/* 关单按钮只在还开着的时候出现。已关闭的单不给"重新打开" ——
              那条出边只留给管理员：用户能自己关掉再开回来的话，
              "未关闭工单数上限"那道闸就形同虚设。 */}
          {ticket != null && !closed && (
            <Button
              type='button'
              variant='outline'
              disabled={closeMutation.isPending}
              onClick={() => closeMutation.mutate()}
            >
              {t('qy_tk_close_action')}
            </Button>
          )}
          <Button
            type='button'
            disabled={!canReply}
            onClick={() => replyMutation.mutate()}
          >
            {t('qy_tk_reply_action')}
          </Button>
        </>
      }
    >
      {detailQuery.isLoading && <LoadingState />}

      {/* 三个分支写成并列而不是嵌套三元:取数失败必须说出来并给一次重试。
          只有"加载中"和"有数据"两态的话,一次 503(扩展降级)或 404(这张单
          已被清理)都会表现为弹窗永远转圈 —— 看不到原因,也只能反复关掉再开。 */}
      {!detailQuery.isLoading && ticket == null && (
        <ErrorState
          title={t('qy_cfg_error_title')}
          description={qyErrorMessage(detailQuery.error, t)}
          onRetry={() => {
            void detailQuery.refetch()
          }}
        />
      )}

      {ticket != null && (
        <div className='space-y-4'>
          <div className='flex flex-wrap items-center gap-2'>
            <QyStatusBadge status={ticket.status} />
            <StatusBadge
              label={t(getQyTicketPriorityStyle(ticket.priority).labelKey)}
              variant={getQyTicketPriorityStyle(ticket.priority).variant}
              copyable={false}
              size='sm'
            />
            <span className='text-muted-foreground text-xs'>
              {t('qy_tk_created_at', { at: formatQyTs(ticket.created_at) })}
            </span>
          </div>

          <QyTicketThread messages={ticket.messages} />

          {closed ? (
            // 关闭是终态。如实说清"要继续请新建一张"，而不是画一个点不动的
            // 输入框让人反复试。
            <p className='text-muted-foreground rounded-md border border-dashed p-3 text-sm'>
              {t('qy_tk_closed_hint')}
            </p>
          ) : (
            <div className='space-y-1.5'>
              <Label htmlFor={replyId}>{t('qy_tk_reply_label')}</Label>
              <Textarea
                id={replyId}
                rows={5}
                value={body}
                placeholder={t('qy_tk_reply_ph')}
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
              <QyTicketImagePicker
                config={props.config}
                disabled={replyMutation.isPending}
                items={images}
                onItemsChange={setImages}
              />
            </div>
          )}
        </div>
      )}
    </QyResponsiveDialog>
  )
}
