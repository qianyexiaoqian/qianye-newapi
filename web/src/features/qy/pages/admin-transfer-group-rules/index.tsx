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
import { Link } from '@tanstack/react-router'
import { Pencil, Plus, ScrollText, Trash2, Users } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

import { QyConfirmDialog } from '../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { qyKeys } from '../../lib/query-keys'
import { qyOpsErrorMessage } from '../ops/errors'
import { formatQyTs, QY_EMPTY_TEXT } from '../ops/format'
import {
  qyAdminTransferGroupRulesQuery,
  qyDeleteTransferGroupRule,
} from './api'
import { QyGroupMatrixCard } from './components/group-matrix-card'
import { QyGroupRuleFormSheet } from './components/group-rule-form-sheet'
import { qyIsSelfToken, qySplitGroupList } from './lib/rule-form'
import { QY_GROUP_WILDCARD, type QyTransferGroupRule } from './types'

/**
 * 划转分组限制。
 *
 * 两块内容，顺序不能反：
 *   1. **矩阵在上** —— 「当前谁能转给谁」是运营真正要回答的问题，规则列表只是
 *      达成它的手段。先给结论再给规则，才不会有人改完规则却没意识到副作用。
 *   2. **规则列表在下** —— 增删改查。
 *
 * 规则表为空表示完全不限制，这是升级后的默认状态，页面必须把它说清楚，
 * 否则管理员看到一张空表会以为功能没生效。
 */
export function QyAdminTransferGroupRules() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery(qyAdminTransferGroupRulesQuery())

  const [editing, setEditing] = useState<QyTransferGroupRule | null>(null)
  const [sheetOpen, setSheetOpen] = useState(false)
  const [pendingDelete, setPendingDelete] =
    useState<QyTransferGroupRule | null>(null)

  const deleteMutation = useMutation({
    mutationFn: (rule: QyTransferGroupRule) =>
      qyDeleteTransferGroupRule(rule.id),
    onSuccess: async () => {
      toast.success(t('qy_trg_deleted'))
      setPendingDelete(null)
      await queryClient.invalidateQueries({
        queryKey: qyKeys.adminTransferGroupRules(),
      })
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const data = query.data
  const rules = data?.items ?? []
  const atLimit = data != null && rules.length >= data.max_rule_count

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_transfer_group_rules')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button
          variant='outline'
          size='sm'
          render={<Link to='/qy/admin/transfer-records' />}
        >
          <ScrollText aria-hidden='true' />
          {t('qy_nav_a_transfer_records')}
        </Button>
        <Button
          type='button'
          disabled={atLimit}
          onClick={() => {
            setEditing(null)
            setSheetOpen(true)
          }}
        >
          <Plus aria-hidden='true' />
          {t('qy_trg_create')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <QyPageBoundary query={query}>
          {data != null && (
            <div className='space-y-4'>
              <QyGroupMatrixCard
                matrix={data.matrix}
                knownGroups={data.known_groups}
              />

              {rules.length === 0 ? (
                /* 空表 = 完全不限制。不说清楚的话，管理员看到一张空表
                   会以为是"还没加载出来"或"功能没生效"。 */
                <div className='text-muted-foreground rounded-lg border border-dashed p-6 text-center text-sm'>
                  <Users
                    className='mx-auto mb-2 size-6 opacity-60'
                    aria-hidden='true'
                  />
                  <p className='text-foreground font-medium'>
                    {t('qy_trg_empty_title')}
                  </p>
                  <p className='mt-1'>{t('qy_trg_empty_desc')}</p>
                </div>
              ) : (
                <div className='w-full overflow-x-auto'>
                  <StaticDataTable
                    data={rules}
                    getRowKey={(row) => row.id}
                    tableClassName='min-w-[860px]'
                    columns={[
                      {
                        id: 'from_group',
                        header: t('qy_trg_field_from'),
                        cell: (row: QyTransferGroupRule) => (
                          <span className='font-medium'>
                            {row.from_group === QY_GROUP_WILDCARD
                              ? t('qy_trg_fallback_label')
                              : row.from_group}
                          </span>
                        ),
                      },
                      {
                        id: 'policy',
                        header: t('qy_trg_field_policy'),
                        cell: (row: QyTransferGroupRule) => (
                          <Badge
                            variant={
                              row.policy === 'deny_all'
                                ? 'destructive'
                                : 'outline'
                            }
                          >
                            {t(`qy_trg_policy_${row.policy}`)}
                          </Badge>
                        ),
                      },
                      {
                        id: 'to_groups',
                        header: t('qy_trg_field_to'),
                        cell: (row: QyTransferGroupRule) => (
                          <ToGroupsCell rule={row} />
                        ),
                      },
                      {
                        id: 'enabled',
                        header: t('qy_common_status'),
                        cell: (row: QyTransferGroupRule) => (
                          <StatusBadge
                            label={t(
                              row.enabled
                                ? 'qy_trg_enabled'
                                : 'qy_trg_disabled_label'
                            )}
                            variant={row.enabled ? 'success' : 'neutral'}
                            copyable={false}
                          />
                        ),
                      },
                      {
                        id: 'remark',
                        header: t('qy_common_remark'),
                        cell: (row: QyTransferGroupRule) =>
                          row.remark === '' ? QY_EMPTY_TEXT : row.remark,
                      },
                      {
                        id: 'updated_at',
                        header: t('qy_common_updated_at'),
                        cell: (row: QyTransferGroupRule) =>
                          row.updated_at === 0
                            ? QY_EMPTY_TEXT
                            : formatQyTs(row.updated_at),
                      },
                      {
                        id: 'actions',
                        header: t('qy_common_actions'),
                        cell: (row: QyTransferGroupRule) => (
                          <span className='flex items-center gap-1'>
                            <Button
                              type='button'
                              variant='ghost'
                              size='icon-sm'
                              aria-label={t('qy_trg_edit')}
                              onClick={() => {
                                setEditing(row)
                                setSheetOpen(true)
                              }}
                            >
                              <Pencil aria-hidden='true' />
                            </Button>
                            <Button
                              type='button'
                              variant='ghost'
                              size='icon-sm'
                              aria-label={t('qy_trg_delete')}
                              onClick={() => setPendingDelete(row)}
                            >
                              <Trash2 aria-hidden='true' />
                            </Button>
                          </span>
                        ),
                      },
                    ]}
                  />
                </div>
              )}

              {atLimit && (
                <p className='text-warning text-xs'>
                  {t('qy_trg_at_limit', { max: data.max_rule_count })}
                </p>
              )}
            </div>
          )}
        </QyPageBoundary>
      </QySectionPageLayout.Content>

      <QyGroupRuleFormSheet
        open={sheetOpen}
        onOpenChange={setSheetOpen}
        rule={editing}
        knownGroups={data?.known_groups ?? []}
        onSaved={() => {
          void queryClient.invalidateQueries({
            queryKey: qyKeys.adminTransferGroupRules(),
          })
        }}
      />

      <QyConfirmDialog
        open={pendingDelete != null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null)
        }}
        title={t('qy_trg_delete')}
        description={t('qy_trg_delete_desc', {
          group: pendingDelete?.from_group ?? '',
        })}
        confirmText={t('qy_trg_delete')}
        isLoading={deleteMutation.isPending}
        onConfirm={() => {
          if (pendingDelete != null) deleteMutation.mutate(pendingDelete)
        }}
      />
    </QySectionPageLayout>
  )
}

/**
 * 目标分组名单。
 *
 * `@self` 一律渲染成人话（「同组」），不把令牌原样丢给运营 —— 那是内部编码，
 * 而这一列要回答的是「这条规则允许转给谁」。
 */
function ToGroupsCell(props: { rule: QyTransferGroupRule }) {
  const { t } = useTranslation()
  const rule = props.rule

  if (rule.policy === 'allow_all' || rule.policy === 'deny_all') {
    return <span className='text-muted-foreground'>{QY_EMPTY_TEXT}</span>
  }
  const entries = qySplitGroupList(rule.to_groups)
  if (entries.length === 0) {
    return <span className='text-muted-foreground'>{QY_EMPTY_TEXT}</span>
  }
  return (
    <span className='flex flex-wrap gap-1'>
      {entries.map((entry) => (
        <Badge key={entry} variant='secondary' className='font-normal'>
          {qyIsSelfToken(entry) ? t('qy_trg_self_label') : entry}
        </Badge>
      ))}
    </span>
  )
}
