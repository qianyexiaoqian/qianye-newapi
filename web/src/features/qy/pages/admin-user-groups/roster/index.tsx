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
import { PencilLine, Plus, Trash2 } from 'lucide-react'
import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'

import { qyKeys } from '../../../lib/query-keys'
import { qyOpsErrorMessage } from '../../ops/errors'
import {
  qyUgrCreate,
  qyUgrDelete,
  qyUgrImpactQuery,
  qyUgrListQuery,
  qyUgrRename,
  qyUgrUpdate,
} from './api'
import { QyUgrCreateDialog } from './components/create-dialog'
import { QyUgrDeleteDialog } from './components/delete-dialog'
import { QyUgrRenameDialog } from './components/rename-dialog'
import type { QyUgrCreateRequest, QyUgrRewriteResult, QyUgrRow } from './types'

const QY_UGR_EMPTY_DRAFT: QyUgrCreateRequest = {
  display_name: '',
  name: '',
  note: '',
}

/**
 * 「用户分组登记」卡片 —— 挂在系统设置 → 计费与支付 → 用户分组页上。
 *
 * ── 项目方原话 ──
 *
 * 「用户分组当前无法删除/无法添加。用户分组删除时,若存在用户,弹出选择窗口,
 * 这批用户要迁移到哪个用户分组?」
 *
 * ── 它与同页上面那张表的分工 ──
 *
 * 上面那张表的清单是**观测出来的**(`users.group` 的现值),它回答"站上此刻
 * 有哪几档人、每一档充值按几折"。这一张的主语是**这个名字本身**:登记表
 * (`qy_user_groups`)是运营维护出来的,而一个刚建出来的分组还没有任何人 ——
 * 它只存在于这一张表里。新建 / 改名 / 删除因此只能在这里做。
 *
 * ── 删除为什么必须先拉一次影响面 ──
 *
 * 删掉一个用户分组同时是一次批量改价与一次批量权限变更。判据全在服务端
 * (`buildUserGroupImpact`),前端一个都不自己推:两边各推一遍必然漂移,
 * 而漂移的方向永远是"按钮亮着、点下去 400"。
 */
export function QyUserGroupRoster() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const listQuery = useQuery({ ...qyUgrListQuery(), retry: false })

  const [creating, setCreating] = useState(false)
  const [createDraft, setCreateDraft] = useState(QY_UGR_EMPTY_DRAFT)
  const [renaming, setRenaming] = useState<QyUgrRow | null>(null)
  const [newName, setNewName] = useState('')
  const [deleting, setDeleting] = useState<QyUgrRow | null>(null)
  const [target, setTarget] = useState('')
  const [ack, setAck] = useState(false)
  const [noteDraft, setNoteDraft] = useState<Record<string, string>>({})

  const impactQuery = useQuery({
    ...qyUgrImpactQuery(deleting?.name ?? null, target),
    retry: false,
  })

  const refresh = useCallback(async () => {
    await queryClient.invalidateQueries({
      queryKey: qyKeys.adminUserGroupRoster(),
    })
    // 矩阵页的行轴、以及「用户分组」那张表的在册人数都会随之变化。
    // 不一起失效的表现是:删完之后同一屏上另一张表还列着那个已经不存在的分组。
    await queryClient.invalidateQueries({ queryKey: qyKeys.adminGroupMatrix() })
  }, [queryClient])

  /**
   * 跨库改写的收尾。
   *
   * `partial` 非空 = 两库只写成了一半,**必须**原样弹出来并留在屏幕上:
   * 报一句绿色的「已完成」然后走人,是这套设计最坏的失败方式 —— 线上停在
   * 中间态,而运营以为一切正常。`stragglers` 同理:那几个账号此刻正指着一个
   * 已经被删掉的分组名,是这条链上唯一会产出孤儿的缝隙。
   */
  const settleRewrite = useCallback(
    async (result: QyUgrRewriteResult, okMessage: string) => {
      if (result.partial != null) {
        toast.error(result.partial.message, {
          duration: Number.POSITIVE_INFINITY,
        })
      } else if (result.stragglers > 0) {
        toast.warning(t('qy_ugr_stragglers', { count: result.stragglers }), {
          duration: Number.POSITIVE_INFINITY,
        })
      } else {
        toast.success(okMessage)
      }
      await refresh()
    },
    [refresh, t]
  )

  const createMutation = useMutation({
    mutationFn: () => qyUgrCreate(createDraft),
    onSuccess: async (result) => {
      toast.success(t('qy_ugr_created', { name: result.name }))
      // 「建好了但还不能用」是服务端现算的(它读得到授权与 abilities 的现值)。
      // 逐条常驻地弹出来:折进一句"请注意配置"的话,运营下一步照样会把人挪进来,
      // 然后拿到一批 403。
      for (const warning of result.warnings) {
        toast.warning(warning, { duration: Number.POSITIVE_INFINITY })
      }
      setCreating(false)
      setCreateDraft(QY_UGR_EMPTY_DRAFT)
      await refresh()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const noteMutation = useMutation({
    mutationFn: (input: { name: string; note: string }) =>
      qyUgrUpdate(input.name, { note: input.note }),
    onSuccess: async (_data, input) => {
      toast.success(t('qy_ugr_note_saved'))
      setNoteDraft((current) => {
        const next = { ...current }
        delete next[input.name]
        return next
      })
      await refresh()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const renameMutation = useMutation({
    mutationFn: () =>
      qyUgrRename(renaming?.name ?? '', {
        // 回传运营在界面上看到的人数。对不上时后端返回 409 而不是照着新数字改。
        expect_users: renaming?.users ?? 0,
        new_name: newName.trim(),
      }),
    onSuccess: async (result) => {
      await settleRewrite(
        result,
        t('qy_ugr_renamed', { from: result.from, to: result.to })
      )
      setRenaming(null)
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const deleteMutation = useMutation({
    mutationFn: () =>
      qyUgrDelete(deleting?.name ?? '', {
        ack_loses_everything: ack,
        expect_users: impactQuery.data?.users ?? 0,
        migrate_to: target,
      }),
    onSuccess: async (result) => {
      await settleRewrite(
        result,
        result.to === ''
          ? t('qy_ugr_deleted', { name: result.from })
          : t('qy_ugr_deleted_migrated', {
              name: result.from,
              target: result.to,
              users: result.users,
            })
      )
      closeDelete()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const closeDelete = useCallback(() => {
    setDeleting(null)
    // 迁移目标与强制勾选**必须**跟着弹窗一起清掉:留着的话下一次删除会带着
    // 上一次的选择打开,而那个勾选正是"我知道这会让一批人的令牌全部挂掉"的
    // 全部凭据 —— 它不该被上一次操作代签。
    setTarget('')
    setAck(false)
  }, [])

  return (
    <Card>
      <CardHeader className='border-b'>
        <CardTitle>{t('qy_ugr_registry_title')}</CardTitle>
        <CardDescription>{t('qy_ugr_registry_desc')}</CardDescription>
        <CardAction>
          <Button
            size='sm'
            onClick={() => {
              setCreateDraft(QY_UGR_EMPTY_DRAFT)
              setCreating(true)
            }}
          >
            <Plus className='h-4 w-4' />
            {t('qy_ugr_create_title')}
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent>
        <StaticDataTable
          data={listQuery.data?.items ?? []}
          getRowKey={(row) => row.name}
          emptyClassName='text-muted-foreground h-20 text-sm'
          emptyContent={t('qy_ugr_registry_empty')}
          columns={[
            {
              id: 'name',
              header: t('Group name'),
              className: 'min-w-40',
              cell: (row) => (
                <div className='min-w-0'>
                  <div className='font-medium break-words'>{row.name}</div>
                  <div className='mt-1 flex flex-wrap gap-1'>
                    {!row.enabled && (
                      <StatusBadge variant='neutral' copyable={false}>
                        {t('Disabled')}
                      </StatusBadge>
                    )}
                    {row.legacy_dual && (
                      <StatusBadge variant='warning' copyable={false}>
                        {t('qy_ugr_legacy_dual')}
                      </StatusBadge>
                    )}
                  </div>
                </div>
              ),
            },
            {
              /*
                在册人数与空分组令牌数摆在同一格。

                后者是「没有指定令牌分组」的那一批 —— 它们的模型分组由
                `users.group`(或本分组的默认模型分组)解析,是迁移之后行为
                变化最直接的一批。只显示人数的话,运营会以为"3 个人而已"。
              */
              id: 'users',
              header: t('qy_ugr_col_users'),
              className: 'w-40',
              cell: (row) => (
                <span className='text-xs tabular-nums'>
                  {t('qy_ugr_col_users_value', {
                    empty: row.empty_group_tokens,
                    users: row.users,
                  })}
                </span>
              ),
            },
            {
              id: 'note',
              header: t('qy_ugr_col_note'),
              className: 'min-w-56',
              cell: (row) => (
                <div className='flex items-center gap-2'>
                  <Input
                    value={noteDraft[row.name] ?? row.note}
                    aria-label={t('qy_ugr_col_note')}
                    placeholder={t('qy_ugr_field_note_placeholder')}
                    onChange={(event) =>
                      setNoteDraft((current) => ({
                        ...current,
                        [row.name]: event.target.value,
                      }))
                    }
                  />
                  <Button
                    size='sm'
                    variant='outline'
                    disabled={
                      noteDraft[row.name] == null ||
                      noteDraft[row.name] === row.note ||
                      noteMutation.isPending
                    }
                    onClick={() =>
                      noteMutation.mutate({
                        name: row.name,
                        note: noteDraft[row.name] ?? row.note,
                      })
                    }
                  >
                    {t('Save')}
                  </Button>
                </div>
              ),
            },
            {
              id: 'actions',
              header: t('Actions'),
              className: 'text-right',
              cellClassName: 'text-right',
              cell: (row) => (
                <div className='flex justify-end gap-1'>
                  <Button
                    variant='ghost'
                    size='sm'
                    aria-label={t('qy_ugr_rename_action')}
                    onClick={() => {
                      setRenaming(row)
                      setNewName(row.name)
                    }}
                  >
                    <PencilLine className='h-4 w-4' />
                  </Button>
                  <Button
                    variant='ghost'
                    size='sm'
                    aria-label={t('Delete')}
                    onClick={() => {
                      setTarget('')
                      setAck(false)
                      setDeleting(row)
                    }}
                  >
                    <Trash2 className='h-4 w-4' />
                  </Button>
                </div>
              ),
            },
          ]}
        />
      </CardContent>

      <QyUgrCreateDialog
        open={creating}
        onOpenChange={setCreating}
        draft={createDraft}
        onDraftChange={(patch) =>
          setCreateDraft((current) => ({ ...current, ...patch }))
        }
        isSaving={createMutation.isPending}
        onConfirm={() => createMutation.mutate()}
      />

      <QyUgrRenameDialog
        open={renaming != null}
        onOpenChange={(open) => {
          if (!open) setRenaming(null)
        }}
        row={renaming}
        newName={newName}
        onNewNameChange={setNewName}
        isSaving={renameMutation.isPending}
        onConfirm={() => renameMutation.mutate()}
      />

      <QyUgrDeleteDialog
        open={deleting != null}
        onOpenChange={(open) => {
          if (!open) closeDelete()
        }}
        impact={impactQuery.data ?? null}
        isLoading={impactQuery.isLoading}
        isRefreshing={impactQuery.isFetching && impactQuery.data != null}
        isDeleting={deleteMutation.isPending}
        target={target}
        onTargetChange={(value) => {
          setTarget(value)
          // 换目标 = 换一份差异。上一个目标的"我知道他们会全部挂掉"不能顺延过来。
          setAck(false)
        }}
        ack={ack}
        onAckChange={setAck}
        onConfirm={() => deleteMutation.mutate()}
      />
    </Card>
  )
}
