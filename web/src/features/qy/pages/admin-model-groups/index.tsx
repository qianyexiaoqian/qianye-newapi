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
import { Trash2 } from 'lucide-react'
import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'

import { qyKeys } from '../../lib/query-keys'
import { qyOpsErrorMessage } from '../ops/errors'
import { qyMgDelete, qyMgImpactQuery, qyMgListQuery, qyMgUpdate } from './api'
import { QyMgDeleteDialog } from './components/delete-dialog'
import type { QyMgRow } from './types'

/**
 * 「模型分组登记」卡片 —— 挂在系统设置 → 计费与支付 → 模型分组页上。
 *
 * ── 它与同页上面那张「分组倍率」表的分工 ──
 *
 * 上面那张表的主语是**兜底倍率**(`options.GroupRatio`),它回答"这次请求按几倍
 * 结账";这一张的主语是**这个名字本身**,它回答三件上面那张表答不了的事:
 *
 *  1. 这个名字**是从哪来的**。项目方原话:「模型分组当前预设有点混乱」——
 *     混乱的具体形状是回填出来的名单里混着 `default` / `group_1` 这类历史残留,
 *     而一行只有名字和倍率时,「一个正在承载 76 行路由的号池」与「一个谁都用不到的
 *     空壳」长得完全一样。
 *  2. 它的**备注**。项目方原话:「模型分组,你这里要加入分组备注,让用户选择的
 *     时候显示出来分组备注内容。」备注会**覆盖**全局「用户可选分组」里那段说明,
 *     所以两者必须同屏 —— 只显示其中一段,运营永远不知道用户看到的是哪一份。
 *  3. **删除**。上面那张表的垃圾桶只是把一行从倍率表里去掉,其余十一处引用
 *     原样留着(授权、套餐解锁、auto 顺序、pin、令牌……)。这里的删除走后端的
 *     联动端点,并且删之前强制看一次影响面。
 *
 * ── 为什么不做"新建"与"改名" ──
 *
 * 新建:一个模型分组是不是存在,由 `options.GroupRatio` 与 abilities 回答。
 * 在登记表里凭空加一行只会造出一个既没有倍率也没有渠道的名字 —— 那正是
 * 「预设混乱」的来源。新建仍然在上面那张表里(加一行倍率)。
 * 改名:要连带重写 abilities(物化路由表)、`channels.group`(逗号串)、
 * `tokens.group` 与两张授权表,血溅路由与计费两条链路。射程之外。
 */
export function QyModelGroupRegistry() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const listQuery = useQuery({ ...qyMgListQuery(), retry: false })

  /** 正在编辑备注的那一行 → 草稿文本。null = 没有人在编辑。 */
  const [noteDraft, setNoteDraft] = useState<Record<string, string>>({})
  const [deleting, setDeleting] = useState<string | null>(null)
  const [forceHasRoute, setForceHasRoute] = useState(false)
  const [forceOrphanTokens, setForceOrphanTokens] = useState(false)

  const impactQuery = useQuery({ ...qyMgImpactQuery(deleting), retry: false })

  const refresh = useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: qyKeys.adminModelGroups() })
  }, [queryClient])

  const noteMutation = useMutation({
    mutationFn: (input: { name: string; note: string }) =>
      qyMgUpdate(input.name, { note: input.note }),
    onSuccess: async (_data, input) => {
      toast.success(t('qy_mg_note_saved'))
      setNoteDraft((current) => {
        const next = { ...current }
        delete next[input.name]
        return next
      })
      await refresh()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const deleteMutation = useMutation({
    mutationFn: (name: string) =>
      qyMgDelete(name, {
        force_has_route: forceHasRoute,
        force_orphan_tokens: forceOrphanTokens,
      }),
    onSuccess: async (result) => {
      // 半成状态必须原样弹出来并**留在屏幕上**:两库不原子的最坏失败方式是
      // 运营看到一句绿色的「已删除」然后走人,而线上停在中间态。
      if (result.partial == null) {
        toast.success(
          t('qy_mg_deleted', { keys: result.removed_from.join('、') })
        )
      } else {
        toast.error(result.partial.message, {
          duration: Number.POSITIVE_INFINITY,
        })
      }
      closeDelete()
      await refresh()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const closeDelete = useCallback(() => {
    setDeleting(null)
    // 两个覆盖勾选**必须**跟着弹窗一起清掉:留着的话下一次删除会带着上一次的
    // 勾选状态打开,而那两个勾选正是"我知道这会让 200 个令牌挂掉"的全部凭据。
    setForceHasRoute(false)
    setForceOrphanTokens(false)
  }, [])

  return (
    <Card>
      <CardHeader className='border-b'>
        <CardTitle>{t('qy_mg_registry_title')}</CardTitle>
        <CardDescription>{t('qy_mg_registry_desc')}</CardDescription>
      </CardHeader>
      <CardContent>
        <StaticDataTable
          data={listQuery.data?.items ?? []}
          getRowKey={(row) => row.name}
          emptyClassName='text-muted-foreground h-20 text-sm'
          emptyContent={t('qy_mg_registry_empty')}
          columns={[
            {
              id: 'name',
              header: t('Group name'),
              className: 'min-w-40',
              cell: (row) => (
                <div className='min-w-0'>
                  <div className='font-medium break-words'>{row.name}</div>
                  <div className='mt-1 flex flex-wrap gap-1'>
                    {row.sources.map((source) => (
                      <StatusBadge
                        key={source}
                        copyable={false}
                        variant={
                          source === 'registry_only' ? 'neutral' : 'info'
                        }
                      >
                        {t(`qy_mg_source_${source}`)}
                      </StatusBadge>
                    ))}
                    {row.legacy_dual && (
                      <StatusBadge variant='warning' copyable={false}>
                        {t('qy_mg_legacy_dual')}
                      </StatusBadge>
                    )}
                  </div>
                </div>
              ),
            },
            {
              id: 'routable',
              header: t('qy_mg_col_routable'),
              className: 'w-40',
              cell: (row) => (
                <span className='text-xs'>
                  {t('qy_mg_col_routable_value', {
                    route: row.has_route ? t('Yes') : t('No'),
                    channels: row.channel_count,
                  })}
                </span>
              ),
            },
            {
              id: 'note',
              header: t('qy_mg_col_note'),
              className: 'min-w-64',
              cell: (row) => (
                <QyMgNoteCell
                  row={row}
                  draft={noteDraft[row.name]}
                  isSaving={
                    noteMutation.isPending &&
                    noteMutation.variables?.name === row.name
                  }
                  onDraftChange={(value) =>
                    setNoteDraft((current) => ({
                      ...current,
                      [row.name]: value,
                    }))
                  }
                  onSave={(value) =>
                    noteMutation.mutate({ name: row.name, note: value })
                  }
                />
              ),
            },
            {
              id: 'actions',
              header: t('Actions'),
              className: 'text-right',
              cellClassName: 'text-right',
              cell: (row) => (
                <Button
                  variant='ghost'
                  size='sm'
                  aria-label={t('Delete')}
                  onClick={() => setDeleting(row.name)}
                >
                  <Trash2 className='h-4 w-4' />
                </Button>
              ),
            },
          ]}
        />
      </CardContent>

      <QyMgDeleteDialog
        open={deleting != null}
        onOpenChange={(open) => {
          if (!open) closeDelete()
        }}
        impact={impactQuery.data ?? null}
        isLoading={impactQuery.isFetching}
        isDeleting={deleteMutation.isPending}
        forceHasRoute={forceHasRoute}
        forceOrphanTokens={forceOrphanTokens}
        onForceHasRouteChange={setForceHasRoute}
        onForceOrphanTokensChange={setForceOrphanTokens}
        onConfirm={() => {
          if (deleting == null) return
          deleteMutation.mutate(deleting)
        }}
      />
    </Card>
  )
}

/**
 * 备注单元格。
 *
 * ── 为什么白名单原文与备注必须同屏 ──
 *
 * `options.UserUsableGroups` 的 value 本来就是给用户看的说明文案(本站已有真实
 * 内容,如「浅夜自有分组用户数据,本站均不会留存…」),而备注**覆盖**它。
 * 只显示备注框的话,运营在这里写下一句话之后,那段原文既没消失也不再生效 ——
 * 「两份文案并存而没人知道哪份生效」正是这次要消掉的混乱,不能换个地方再造一次。
 *
 * 所以原文以灰字常驻在输入框下面,并明确标注它此刻是「生效中」还是「已被覆盖」。
 */
function QyMgNoteCell(props: {
  row: QyMgRow
  draft: string | undefined
  isSaving: boolean
  onDraftChange: (value: string) => void
  onSave: (value: string) => void
}) {
  const { t } = useTranslation()
  const value = props.draft ?? props.row.note
  const dirty = props.draft != null && props.draft !== props.row.note
  const overridden = value.trim() !== ''

  return (
    <div className='min-w-0 space-y-1'>
      <div className='flex items-center gap-2'>
        <Input
          value={value}
          aria-label={t('qy_mg_col_note')}
          placeholder={t('qy_mg_note_placeholder')}
          onChange={(event) => props.onDraftChange(event.target.value)}
        />
        <Button
          size='sm'
          variant='outline'
          disabled={!dirty || props.isSaving}
          onClick={() => props.onSave(value)}
        >
          {props.isSaving ? t('Saving...') : t('Save')}
        </Button>
      </div>
      {props.row.in_usable_groups && (
        <p className='text-muted-foreground text-xs leading-5'>
          {overridden
            ? t('qy_mg_note_overrides_usable', {
                text: props.row.usable_description,
              })
            : t('qy_mg_note_falls_back', {
                text: props.row.usable_description,
              })}
        </p>
      )}
    </div>
  )
}
