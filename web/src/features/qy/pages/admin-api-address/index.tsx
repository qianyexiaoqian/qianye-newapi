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
import {
  ArrowDown,
  ArrowUp,
  GripVertical,
  Link2,
  Pencil,
  Plus,
  Trash2,
} from 'lucide-react'
import { Reorder, useDragControls } from 'motion/react'
import {
  useEffect,
  useState,
  type KeyboardEvent,
  type PointerEvent,
} from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

import { QyConfirmDialog } from '../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { qyErrorMessage } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import {
  qyAdminApiAddressesQuery,
  qyCreateApiAddress,
  qyDeleteApiAddress,
  qyReorderApiAddresses,
  qyUpdateApiAddress,
} from './api'
import { QyApiAddressFormDialog } from './components/address-form-dialog'
import type { QyApiAddress, QyApiAddressUpsert } from './types'

/**
 * API 地址簿管理页。
 *
 * ── 它配的是什么 ──
 * 密钥列表上的「复制链接信息」会把 `{"_type":"newapi_channel_conn","key":…,"url":…}`
 * 放进剪贴板给 CC Switch 用。改造前那个 `url` 是前端单方面取的站点地址，
 * 用户想换只能自己手改 JSON。这一页决定「用户点复制时能从哪几个地址里选」。
 *
 * ── 为什么排序是「先拖，再显式保存」而不是拖一下存一次 ──
 * 拖动一次会连续产生多个中间顺序，每一个都是一次写请求 + 一条审计。更要命的是
 * 中间态会被别的管理员看到（他刷新时正好落在你拖到一半的那个顺序上）。
 * 攒成一次提交之后，整表重排是原子的：后端要求提交的 id 集合与库里当前完全
 * 一致，有人在你排序期间加了一条就直接 409，而不是把他那条静默挤到末尾。
 */
export function QyAdminApiAddress() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery(qyAdminApiAddressesQuery())
  const config = query.data

  // 排序草稿。服务端数据一到就重置 —— 保留旧草稿会让人基于过期基线排序，
  // 把别人刚加的那条排没了（后端会 409，但那是一次白费的操作）。
  const [draft, setDraft] = useState<QyApiAddress[]>([])
  useEffect(() => {
    if (config == null) return
    setDraft(config.items)
  }, [config])

  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<QyApiAddress | null>(null)
  const [deleting, setDeleting] = useState<QyApiAddress | null>(null)

  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: qyKeys.all })
  }

  const saveMutation = useMutation({
    mutationFn: (body: QyApiAddressUpsert) =>
      editing == null
        ? qyCreateApiAddress(body)
        : qyUpdateApiAddress(editing.id, body),
    onSuccess: async () => {
      setFormOpen(false)
      toast.success(t('qy_aa_saved'))
      await refresh()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => qyDeleteApiAddress(id),
    onSuccess: async () => {
      setDeleting(null)
      toast.success(t('qy_aa_deleted'))
      await refresh()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const reorderMutation = useMutation({
    mutationFn: (ids: number[]) => qyReorderApiAddresses(ids),
    onSuccess: async () => {
      toast.success(t('qy_aa_order_saved'))
      await refresh()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const serverOrder = (config?.items ?? []).map((item) => item.id).join(',')
  const draftOrder = draft.map((item) => item.id).join(',')
  const orderDirty = config != null && serverOrder !== draftOrder
  const atLimit = config != null && config.items.length >= config.max

  const move = (index: number, direction: 'down' | 'up') => {
    const target = direction === 'up' ? index - 1 : index + 1
    if (target < 0 || target >= draft.length) return
    const next = [...draft]
    ;[next[index], next[target]] = [next[target], next[index]]
    setDraft(next)
  }

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_api_address')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <QyPageBoundary query={query}>
          {config != null && (
            <Card data-card-hover='false'>
              <CardHeader>
                <CardTitle>{t('qy_aa_title')}</CardTitle>
                <CardDescription>{t('qy_aa_desc')}</CardDescription>
              </CardHeader>
              <CardContent className='space-y-4'>
                <div className='flex flex-wrap items-center justify-between gap-2'>
                  <p className='text-muted-foreground text-xs'>
                    {t('qy_aa_count', {
                      count: config.items.length,
                      max: config.max,
                    })}
                  </p>
                  <div className='flex gap-2'>
                    {orderDirty && (
                      <Button
                        variant='outline'
                        size='sm'
                        disabled={reorderMutation.isPending}
                        onClick={() =>
                          reorderMutation.mutate(draft.map((item) => item.id))
                        }
                      >
                        {t('qy_aa_save_order')}
                      </Button>
                    )}
                    <Button
                      size='sm'
                      disabled={atLimit}
                      onClick={() => {
                        setEditing(null)
                        setFormOpen(true)
                      }}
                    >
                      <Plus />
                      {t('qy_aa_create')}
                    </Button>
                  </div>
                </div>

                {config.items.length === 0 ? (
                  <Alert>
                    <Link2 />
                    <AlertDescription>{t('qy_aa_empty')}</AlertDescription>
                  </Alert>
                ) : (
                  <Reorder.Group
                    axis='y'
                    values={draft}
                    onReorder={setDraft}
                    className='flex flex-col gap-2'
                  >
                    {draft.map((item, index) => (
                      <AddressRow
                        key={item.id}
                        item={item}
                        index={index}
                        count={draft.length}
                        onMove={move}
                        onEdit={() => {
                          setEditing(item)
                          setFormOpen(true)
                        }}
                        onDelete={() => setDeleting(item)}
                      />
                    ))}
                  </Reorder.Group>
                )}

                {orderDirty && (
                  <p className='text-muted-foreground text-xs'>
                    {t('qy_aa_order_dirty')}
                  </p>
                )}
              </CardContent>
            </Card>
          )}
        </QyPageBoundary>
      </QySectionPageLayout.Content>

      <QyApiAddressFormDialog
        open={formOpen}
        onOpenChange={setFormOpen}
        current={editing}
        isPending={saveMutation.isPending}
        onSubmit={(body) => saveMutation.mutate(body)}
      />

      <QyConfirmDialog
        open={deleting != null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={t('qy_aa_delete_title')}
        description={t('qy_aa_delete_desc')}
        confirmText={t('qy_aa_delete_confirm')}
        irreversible
        isLoading={deleteMutation.isPending}
        details={
          deleting == null ? null : (
            <p className='text-sm'>
              <strong>{deleting.name}</strong>
              <span className='text-muted-foreground'> — {deleting.url}</span>
            </p>
          )
        }
        onConfirm={() => deleting != null && deleteMutation.mutate(deleting.id)}
      />
    </QySectionPageLayout>
  )
}

type AddressRowProps = {
  item: QyApiAddress
  index: number
  count: number
  onMove: (index: number, direction: 'down' | 'up') => void
  onEdit: () => void
  onDelete: () => void
}

/**
 * 一行地址。拖拽把手与上下按钮并存：拖拽是鼠标用户最快的路径，上下按钮是
 * 键盘与触屏用户唯一能走通的路径 —— 只给拖拽等于把排序功能对他们关掉。
 * 这两条并存的写法沿用上游 `features/keys/components/auto-group-order-editor.tsx`。
 */
function AddressRow(props: AddressRowProps) {
  const { t } = useTranslation()
  const dragControls = useDragControls()

  return (
    <Reorder.Item
      value={props.item}
      dragListener={false}
      dragControls={dragControls}
      className='bg-background flex items-center gap-2 rounded-lg border p-2'
    >
      <Button
        type='button'
        variant='ghost'
        size='icon-sm'
        className='text-muted-foreground cursor-grab touch-none active:cursor-grabbing'
        aria-label={t('qy_aa_drag', { name: props.item.name })}
        onPointerDown={(event: PointerEvent<HTMLButtonElement>) =>
          dragControls.start(event)
        }
        onKeyDown={(event: KeyboardEvent<HTMLButtonElement>) => {
          if (event.key === 'ArrowUp') {
            event.preventDefault()
            props.onMove(props.index, 'up')
          }
          if (event.key === 'ArrowDown') {
            event.preventDefault()
            props.onMove(props.index, 'down')
          }
        }}
      >
        <GripVertical aria-hidden='true' />
      </Button>

      <div className='min-w-0 flex-1'>
        <div className='flex flex-wrap items-center gap-2'>
          <span className='truncate text-sm font-medium'>
            {props.item.name}
          </span>
          {!props.item.enabled && (
            <Badge variant='outline'>{t('qy_aa_disabled')}</Badge>
          )}
        </div>
        <p className='text-muted-foreground truncate font-mono text-xs'>
          {props.item.url}
        </p>
        {props.item.remark !== '' && (
          <p className='text-muted-foreground truncate text-xs'>
            {props.item.remark}
          </p>
        )}
      </div>

      <div className='flex shrink-0 gap-1'>
        <Button
          type='button'
          variant='ghost'
          size='icon-sm'
          disabled={props.index === 0}
          aria-label={t('qy_aa_move_up', { name: props.item.name })}
          onClick={() => props.onMove(props.index, 'up')}
        >
          <ArrowUp />
        </Button>
        <Button
          type='button'
          variant='ghost'
          size='icon-sm'
          disabled={props.index === props.count - 1}
          aria-label={t('qy_aa_move_down', { name: props.item.name })}
          onClick={() => props.onMove(props.index, 'down')}
        >
          <ArrowDown />
        </Button>
        <Button
          type='button'
          variant='ghost'
          size='icon-sm'
          aria-label={t('qy_aa_edit')}
          onClick={props.onEdit}
        >
          <Pencil />
        </Button>
        <Button
          type='button'
          variant='ghost'
          size='icon-sm'
          className='text-destructive hover:text-destructive'
          aria-label={t('qy_aa_delete_title')}
          onClick={props.onDelete}
        >
          <Trash2 />
        </Button>
      </div>
    </Reorder.Item>
  )
}
