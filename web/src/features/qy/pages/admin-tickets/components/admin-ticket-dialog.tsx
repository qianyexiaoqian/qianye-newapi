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
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { useAuthStore } from '@/stores/auth-store'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { qyOpsErrorMessage } from '../../ops/errors'
import { formatQyTs } from '../../ops/format'
import { QyTicketImagePicker } from '../../tickets/components/ticket-image-picker'
import { QyTicketMarkdownHint } from '../../tickets/components/ticket-markdown-hint'
import { QyTicketThread } from '../../tickets/components/ticket-thread'
import {
  QY_TICKET_PRIORITIES,
  getQyTicketPriorityStyle,
} from '../../tickets/lib/priority'
import {
  qyImageRefs,
  qyImagesSettled,
  type QyTicketImageItem,
} from '../../tickets/lib/uploads'
import type { QyTicketConfig, QyTicketPriority } from '../../tickets/types'
import {
  assignQyAdminTicket,
  discardQyAdminTicketImage,
  getQyAdminTicket,
  replyQyAdminTicket,
  setQyAdminTicketPriority,
  setQyAdminTicketStatus,
  uploadQyAdminTicketImage,
} from '../api'

/**
 * 工单处理台：看完整对话（含内部备注）、回复、改等级、关单 / 重开。
 *
 * 认领 / 取消认领就做在这里：那是读完对话之后才该下的判断。列表页刻意**不**做
 * 指派按钮 —— 在表格里分派等于鼓励不看内容就派活（列表页顶部的注释写着同一条）。
 * 把一张单派给**别人**需要选人、需要知道对方在不在班上，那是排班系统的事；
 * 后端接口本身仍然接受任意管理员 id，没有被裁掉。
 *
 * 详情每次打开都重取（`staleTime: 0`）：客服并行处理时，队列里那一行随时可能
 * 已经被同事回过了，拿缓存渲染的结果是两个人各回一遍同一张单。
 */
export function QyAdminTicketDialog(props: {
  ticketId: number | null
  config: QyTicketConfig | undefined
  onClose: () => void
  onChanged: () => void
}) {
  const { t } = useTranslation()
  const replyId = useId()
  const [body, setBody] = useState('')
  const [internal, setInternal] = useState(false)
  const [images, setImages] = useState<QyTicketImageItem[]>([])

  const open = props.ticketId != null

  useEffect(() => {
    setBody('')
    setInternal(false)
    setImages([])
  }, [props.ticketId])

  const detailQuery = useQuery({
    queryKey: qyKeys.adminTicket(props.ticketId ?? 0),
    queryFn: () => getQyAdminTicket(props.ticketId ?? 0),
    enabled: open,
    staleTime: 0,
  })

  const ticket = detailQuery.data
  const closed = ticket?.status === 'closed'
  // config 与详情是两个独立 query，首次进页面必然存在"详情已到、config 还在飞"
  // 的窗口。此时既不能把字数上限显示成 0（"12 / 0 字"），也不能让图片区整段
  // 消失（客服会得出"这个站点不让管理员传图"的结论）—— 两者都要说人话。
  const configLoading = props.config == null
  const maxRunes = props.config?.body_max_runes ?? 0
  const bodyRunes = [...body.trim()].length
  const bodyTooLong = maxRunes > 0 && bodyRunes > maxRunes

  const afterWrite = (message: string) => {
    toast.success(message)
    void detailQuery.refetch()
    props.onChanged()
  }
  const onError = (error: unknown) => toast.error(qyOpsErrorMessage(error, t))

  const replyMutation = useMutation({
    mutationFn: () =>
      replyQyAdminTicket(props.ticketId ?? 0, {
        body: body.trim(),
        attachment_refs: qyImageRefs(images),
        internal,
      }),
    onSuccess: () => {
      setBody('')
      setImages([])
      afterWrite(t(internal ? 'qy_tk_a_note_saved' : 'qy_tk_a_replied'))
    },
    onError,
  })

  const priorityMutation = useMutation({
    mutationFn: (priority: QyTicketPriority) =>
      setQyAdminTicketPriority(props.ticketId ?? 0, priority),
    onSuccess: () => afterWrite(t('qy_tk_a_priority_saved')),
    onError,
  })

  const statusMutation = useMutation({
    mutationFn: (status: 'closed' | 'open') =>
      setQyAdminTicketStatus(props.ticketId ?? 0, { status }),
    onSuccess: () => afterWrite(t('qy_tk_a_status_saved')),
    onError,
  })

  // 指派只提供"认领给我自己 / 取消认领"，理由见文件头。
  const assignMutation = useMutation({
    mutationFn: (assigneeId: number) =>
      assignQyAdminTicket(props.ticketId ?? 0, assigneeId),
    onSuccess: () => afterWrite(t('qy_tk_a_assigned')),
    onError,
  })
  const myId = useAuthStore((state) => state.auth.user?.id) ?? 0
  const mine = ticket != null && ticket.assignee_id === myId && myId > 0

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
      title={ticket?.title ?? t('qy_tk_a_detail_title')}
      description={ticket?.ticket_no}
      contentClassName='sm:max-w-3xl'
      footer={
        <>
          <Button type='button' variant='outline' onClick={props.onClose}>
            {t('qy_common_close')}
          </Button>
          {ticket != null && (
            <Button
              type='button'
              variant='outline'
              disabled={statusMutation.isPending}
              onClick={() => statusMutation.mutate(closed ? 'open' : 'closed')}
            >
              {t(closed ? 'qy_tk_a_reopen' : 'qy_tk_a_close')}
            </Button>
          )}
          <Button
            type='button'
            disabled={!canReply}
            onClick={() => replyMutation.mutate()}
          >
            {t(internal ? 'qy_tk_a_save_note' : 'qy_tk_reply_action')}
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
            <span className='text-muted-foreground text-xs'>
              {t('qy_tk_a_submitter', {
                name: ticket.username,
                id: ticket.user_id,
              })}
            </span>
            <span className='text-muted-foreground text-xs'>
              {t('qy_tk_created_at', { at: formatQyTs(ticket.created_at) })}
            </span>
            {ticket.assignee_id > 0 && (
              <span className='text-muted-foreground text-xs'>
                {t('qy_tk_a_assigned_to', { name: ticket.assignee_name })}
              </span>
            )}
            <Button
              type='button'
              size='sm'
              variant='ghost'
              disabled={assignMutation.isPending || myId <= 0}
              onClick={() => assignMutation.mutate(mine ? 0 : myId)}
            >
              {t(mine ? 'qy_tk_a_unclaim' : 'qy_tk_a_claim')}
            </Button>
          </div>

          <div className='flex flex-wrap items-center gap-2'>
            <Label className='text-xs'>{t('qy_tk_field_priority')}</Label>
            <Select
              value={ticket.priority}
              disabled={priorityMutation.isPending}
              onValueChange={(value) =>
                priorityMutation.mutate(value as QyTicketPriority)
              }
            >
              <SelectTrigger size='sm' className='w-36'>
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
          </div>

          <QyTicketThread messages={ticket.messages} scope='admin' />

          {closed ? (
            <p className='text-muted-foreground rounded-md border border-dashed p-3 text-sm'>
              {t('qy_tk_a_closed_hint')}
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
                  {configLoading
                    ? t('qy_tk_runes_unknown', { n: bodyRunes })
                    : t('qy_tk_runes', { n: bodyRunes, max: maxRunes })}
                </p>
              </div>

              <label className='flex items-center gap-2 text-sm'>
                <Checkbox
                  checked={internal}
                  onCheckedChange={(value) => setInternal(value === true)}
                />
                {t('qy_tk_a_internal_toggle')}
              </label>
              {internal && (
                // 这句必须在勾上之后立刻出现：把内部判断当成答复发给用户
                // 是这一页最贵的误操作，而两者的按钮长在同一个位置。
                <p className='text-warning text-xs'>
                  {t('qy_tk_a_internal_warning')}
                </p>
              )}

              {props.config == null ? (
                <p className='text-muted-foreground text-xs'>
                  {t('qy_tk_config_loading')}
                </p>
              ) : (
                <QyTicketImagePicker
                  config={props.config}
                  disabled={replyMutation.isPending}
                  items={images}
                  onItemsChange={setImages}
                  upload={uploadQyAdminTicketImage}
                  discard={discardQyAdminTicketImage}
                />
              )}
            </div>
          )}
        </div>
      )}
    </QyResponsiveDialog>
  )
}
