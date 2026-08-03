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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Pencil, Plus, Search, Trash2, X } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Spinner } from '@/components/ui/spinner'

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyMaskedUser } from '../../../components/qy-masked-user'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { qyRuneLength } from '../../lib/constants'
import {
  QY_CONTACT_ALIAS_MAX_RUNES,
  qyAddContact,
  qyContactStatusKey,
  qyDeleteContact,
  qyFilterContacts,
  qyRenameContact,
  qyTransferContactsQuery,
  type QyContact,
} from '../contacts'

type TransferContactsDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** `recipient_lookup === 'id_or_email'`，决定添加框接受什么。 */
  acceptsEmail: boolean
  /**
   * 划转入口本身不可用（冷却中、今日次数用尽、风控阻断）。
   *
   * 只禁用「选中」——填一个提交不了的表没有意义。**不禁用增删改**：
   * 冷却和日限额约束的是转账，不是通讯录；因为在冷却里就不让人整理
   * 联系人，是把两件无关的事绑在一起。
   */
  pickDisabled: boolean
  /** 扩展库降级：写接口一定吃 503，管理按钮直接禁用而不是让用户点完才发现。 */
  degraded: boolean
  /** 选中一个联系人。**唯一的副作用就是把收款人输入框填好。** */
  onPick: (contact: QyContact) => void
}

/**
 * 联系人簿（弹窗）。
 *
 * ## 为什么从页面里搬进弹窗
 *
 * 项目方原话：「发起划转的联系人页面改成点击联系人弹窗出联系人列表，不要都在
 * 一个页面上，过度拉伸页面。」原来的实现把整本通讯录 —— 搜索、添加表单、每条
 * 的改名/删除 —— 全部平铺在划转表单**上方**，用户每次转账都要先滚过一屏跟本次
 * 操作无关的内容。现在主表单上只剩一个按钮。
 *
 * ## 它仍然只做一件事
 *
 * 点一个联系人 = 把收款人输入框填成对方的用户 ID，然后关掉弹窗。**仅此而已。**
 *
 * 之后照旧要点「校验收款人」跑 `preview`、照旧要过二次确认弹窗、照旧要输
 * 支付密码、照旧受分组限制与日限额约束。搬进弹窗没有新增任何一条捷径 ——
 * 项目方原话：「联系人只是方便快速填写表，不是因为是联系人就可以绕过支付密码
 * 的验证。」所以 `onPick` 仍然**只接受一个填表用的值**，不返回任何令牌、
 * 不设置任何标记位、也不碰提交请求体。后端侧的同一条约束由
 * `qianye/modules/transfer/contacts_isolation_test.go` 的双向 AST 断言钉死。
 *
 * ## 搜索的边界
 *
 * 搜索框只过滤**已经存下的联系人**（`qyFilterContacts`，纯前端）。它不是
 * "按用户名找陌生人"：那需要一个新的后端接口，而现有的 `lookup.go` 刻意
 * 只支持 ID / 邮箱精确匹配以防用户枚举。**添加**联系人这一步没有变，仍然走
 * `POST /transfer/contacts` → `resolveRecipient`，与 `/transfer/preview`
 * 共用查找方式开关、反枚举日志与按用户限流。
 *
 * ## 状态为什么照常显示可选
 *
 * 对方被封禁或已注销时，条目仍然可点。理由是一致的：联系人不做任何准入判定，
 * **包括反方向的**。判定只有一处 —— 提交那一刻的后端。前端擅自禁用只会多出
 * 一份会漂移的规则，而且在「封禁刚被解除」时给出错误的结论。
 */
export function TransferContactsDialog(props: TransferContactsDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  // 弹窗关着时不打请求：这本通讯录跟"这一次划转"无关，没人点开就不该占一次
  // 后端往返（钱包页上还有另外两张标签同时活着）。
  const contactsQuery = useQuery({
    ...qyTransferContactsQuery(),
    enabled: props.open,
  })

  const [keyword, setKeyword] = useState('')
  const [adding, setAdding] = useState(false)
  const [identifier, setIdentifier] = useState('')
  const [newAlias, setNewAlias] = useState('')
  const [editingId, setEditingId] = useState<number | null>(null)
  const [editingAlias, setEditingAlias] = useState('')
  const [pendingDelete, setPendingDelete] = useState<QyContact | null>(null)

  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: qyKeys.transferContacts() })

  const addMutation = useMutation({
    mutationFn: qyAddContact,
    onSuccess: async () => {
      toast.success(t('qy_tr_ct_added'))
      setAdding(false)
      setIdentifier('')
      setNewAlias('')
      await refresh()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const renameMutation = useMutation({
    mutationFn: qyRenameContact,
    onSuccess: async () => {
      setEditingId(null)
      await refresh()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const deleteMutation = useMutation({
    mutationFn: qyDeleteContact,
    onSuccess: async () => {
      toast.success(t('qy_tr_ct_deleted'))
      setPendingDelete(null)
      await refresh()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const items = contactsQuery.data?.items ?? []
  const shown = qyFilterContacts(items, keyword)
  const max = contactsQuery.data?.max ?? 0
  const atLimit = max > 0 && items.length >= max
  const aliasTooLong = qyRuneLength(newAlias) > QY_CONTACT_ALIAS_MAX_RUNES
  const canAdd =
    !props.degraded &&
    identifier.trim() !== '' &&
    identifier.trim().length <= 64 &&
    !aliasTooLong &&
    !addMutation.isPending

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_tr_ct_title')}
      description={t('qy_tr_ct_desc')}
      contentClassName='sm:max-w-lg'
    >
      <div className='space-y-3'>
        <div className='flex flex-wrap items-center gap-2'>
          <div className='relative min-w-0 flex-1'>
            <Search
              className='text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2'
              aria-hidden='true'
            />
            <Input
              className='h-8 pl-8'
              value={keyword}
              autoComplete='off'
              aria-label={t('qy_tr_ct_search')}
              placeholder={t('qy_tr_ct_search_ph')}
              onChange={(event) => setKeyword(event.target.value)}
            />
          </div>
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={props.degraded || atLimit}
            onClick={() => setAdding((open) => !open)}
          >
            <Plus aria-hidden='true' />
            {t('qy_tr_ct_add')}
          </Button>
        </div>

        {contactsQuery.isPending && (
          <p className='text-muted-foreground flex items-center gap-2 text-xs'>
            <Spinner className='size-3' />
            {t('qy_tr_ct_loading')}
          </p>
        )}

        {contactsQuery.isError && (
          <p className='text-destructive text-xs'>
            {qyErrorMessage(contactsQuery.error, t)}
          </p>
        )}

        {adding && (
          <div className='bg-muted/30 space-y-2 rounded-md border p-3'>
            <div className='space-y-1.5'>
              <Label htmlFor='qy-ct-identifier'>{t('qy_tr_recipient')}</Label>
              <Input
                id='qy-ct-identifier'
                className='h-8'
                value={identifier}
                autoComplete='off'
                inputMode={props.acceptsEmail ? 'text' : 'numeric'}
                placeholder={
                  props.acceptsEmail
                    ? t('qy_tr_recipient_ph_id_email')
                    : t('qy_tr_recipient_ph_id')
                }
                onChange={(event) => setIdentifier(event.target.value)}
              />
              {/* 与收款人输入框同一句提示：添加联系人走的是**同一套**解析
                  （同一个查找方式开关、同一张反枚举日志、同一个限流器），
                  因此接受什么、不接受什么必须显示成同一句话。
                  上面那个搜索框只翻自己已存的记录，两者不是一回事。 */}
              <p className='text-muted-foreground text-xs'>
                {props.acceptsEmail
                  ? t('qy_tr_recipient_help_id_email')
                  : t('qy_tr_recipient_help_id')}
              </p>
            </div>

            <div className='space-y-1.5'>
              <Label htmlFor='qy-ct-alias'>{t('qy_tr_ct_alias')}</Label>
              <Input
                id='qy-ct-alias'
                className='h-8'
                value={newAlias}
                autoComplete='off'
                placeholder={t('qy_tr_ct_alias_ph')}
                aria-invalid={aliasTooLong}
                onChange={(event) => setNewAlias(event.target.value)}
              />
              <p className='text-muted-foreground text-end text-xs tabular-nums'>
                {t('qy_common_rune_counter', {
                  used: qyRuneLength(newAlias),
                  max: QY_CONTACT_ALIAS_MAX_RUNES,
                })}
              </p>
            </div>

            <div className='flex flex-wrap items-center gap-2'>
              <Button
                type='button'
                size='sm'
                disabled={!canAdd}
                onClick={() =>
                  addMutation.mutate({
                    identifier: identifier.trim(),
                    alias: newAlias,
                  })
                }
              >
                {addMutation.isPending
                  ? t('qy_tr_ct_adding')
                  : t('qy_tr_ct_save')}
              </Button>
              <Button
                type='button'
                variant='ghost'
                size='sm'
                onClick={() => setAdding(false)}
              >
                {t('qy_common_cancel')}
              </Button>
            </div>
          </div>
        )}

        {!contactsQuery.isPending && items.length === 0 && (
          <p className='text-muted-foreground text-xs'>{t('qy_tr_ct_empty')}</p>
        )}

        {/* 有联系人但被关键字滤空：与"一个都没存"是两件事，说成同一句会让
            用户以为自己的通讯录没了。 */}
        {items.length > 0 && shown.length === 0 && (
          <p className='text-muted-foreground text-xs'>
            {t('qy_tr_ct_no_match', { keyword: keyword.trim() })}
          </p>
        )}

        {shown.length > 0 && (
          <ul className='space-y-1.5'>
            {shown.map((contact) => (
              <li
                key={contact.id}
                className='flex flex-wrap items-center gap-2 rounded-md border p-2'
              >
                {editingId === contact.id ? (
                  <>
                    <Input
                      className='h-8 min-w-0 flex-1'
                      value={editingAlias}
                      autoComplete='off'
                      aria-label={t('qy_tr_ct_alias')}
                      onChange={(event) => setEditingAlias(event.target.value)}
                    />
                    <Button
                      type='button'
                      variant='ghost'
                      size='icon-sm'
                      aria-label={t('qy_tr_ct_rename_save')}
                      disabled={
                        props.degraded ||
                        renameMutation.isPending ||
                        qyRuneLength(editingAlias) > QY_CONTACT_ALIAS_MAX_RUNES
                      }
                      onClick={() =>
                        renameMutation.mutate({
                          id: contact.id,
                          alias: editingAlias,
                        })
                      }
                    >
                      <Check aria-hidden='true' />
                    </Button>
                    <Button
                      type='button'
                      variant='ghost'
                      size='icon-sm'
                      aria-label={t('qy_common_cancel')}
                      onClick={() => setEditingId(null)}
                    >
                      <X aria-hidden='true' />
                    </Button>
                  </>
                ) : (
                  <>
                    {/* 点它只做两件事：把收款人输入框填好，然后关掉弹窗。
                        关掉是必须的 —— 选完还停在通讯录里，用户会以为
                        自己什么都没选中，于是再点一次。 */}
                    <button
                      type='button'
                      className='min-w-0 flex-1 cursor-pointer text-left'
                      disabled={props.pickDisabled}
                      // 徽章只在异常状态下渲染（一列全是"正常"会把真正要注意
                      // 的那两条淹掉），因此把四档状态统一挂成悬停提示，
                      // 让"这条是正常的"也有一个可查处，而不是只能靠没有徽章推断。
                      title={t(qyContactStatusKey(contact.status))}
                      onClick={() => {
                        props.onPick(contact)
                        props.onOpenChange(false)
                      }}
                    >
                      <span className='flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1'>
                        {contact.alias !== '' && (
                          <span className='truncate text-sm font-medium'>
                            {contact.alias}
                          </span>
                        )}
                        <QyMaskedUser
                          userId={contact.user_id}
                          maskedName={contact.masked_username}
                          className='text-xs'
                        />
                        <ContactStatusBadge status={contact.status} />
                      </span>
                    </button>
                    <Button
                      type='button'
                      variant='ghost'
                      size='icon-sm'
                      aria-label={t('qy_tr_ct_rename')}
                      disabled={props.degraded}
                      onClick={() => {
                        setEditingId(contact.id)
                        setEditingAlias(contact.alias)
                      }}
                    >
                      <Pencil aria-hidden='true' />
                    </Button>
                    <Button
                      type='button'
                      variant='ghost'
                      size='icon-sm'
                      aria-label={t('qy_tr_ct_delete')}
                      disabled={props.degraded}
                      onClick={() => setPendingDelete(contact)}
                    >
                      <Trash2 aria-hidden='true' />
                    </Button>
                  </>
                )}
              </li>
            ))}
          </ul>
        )}

        {max > 0 && (
          <p className='text-muted-foreground text-xs'>
            {t('qy_tr_ct_count', { used: items.length, max })}
          </p>
        )}

        {/* 把「联系人不是免检通道」这件事对用户也说清楚。它不只是提示文案：
            用户如果以为选了联系人就能一键转账，遇到支付密码时会以为是故障。 */}
        <p className='text-muted-foreground text-xs'>{t('qy_tr_ct_note')}</p>
      </div>

      {/* 删除只删自己的快捷方式，不影响任何已发生的划转，因此不标"不可逆"。 */}
      <QyConfirmDialog
        open={pendingDelete != null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null)
        }}
        title={t('qy_tr_ct_delete_title')}
        description={t('qy_tr_ct_delete_desc')}
        isLoading={deleteMutation.isPending}
        confirmText={t('qy_tr_ct_delete')}
        onConfirm={() => {
          if (pendingDelete == null) return
          deleteMutation.mutate(pendingDelete.id)
        }}
      />
    </Dialog>
  )
}

function ContactStatusBadge(props: { status: QyContact['status'] }) {
  const { t } = useTranslation()
  // active 不显示徽章：一列全是"正常"只会把真正需要注意的那两条淹掉。
  if (props.status === 'active') return null

  const variant = props.status === 'disabled' ? 'destructive' : 'outline'
  return (
    <Badge variant={variant} className='shrink-0 text-[10px]'>
      {t(qyContactStatusKey(props.status))}
    </Badge>
  )
}
