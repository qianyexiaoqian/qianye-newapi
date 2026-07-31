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
import { Pencil, Plus, RefreshCw, ShieldAlert, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { SectionPageLayout } from '@/components/layout'
import { StatusBadge } from '@/components/status-badge'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { QyConfirmDialog } from '../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { qyKeys } from '../../lib/query-keys'
import { QyPager } from '../components/qy-pager'
import { qyOpsErrorMessage } from '../ops/errors'
import { formatQyTs, QY_EMPTY_TEXT } from '../ops/format'
import { QyFilterBar, QyFilterField } from '../ops/qy-ops-ui'
import {
  deleteQyViolationRule,
  getQyViolationStats,
  listQyViolationRules,
  resetQyViolationBreaker,
} from './api'
import { QyRuleFormSheet } from './components/rule-form-sheet'
import { QyViolationShadowBanner } from './components/violation-shadow-banner'
import { QY_VIOLATION_PHASES } from './lib/rule-form'
import type { QyViolationRule } from './types'

const PAGE_SIZE = 20
const ALL_PHASES = 'all'

/**
 * 违规规则配置。
 *
 * 一条规则直接决定谁被扣钱、谁被封号，因此这一页的重点不是「能不能增删改查」，
 * 而是三件事：当前是不是影子模式（顶部横幅）、这条规则会不会命中（内置试跑）、
 * 改了什么谁改的（后端强制写审计）。
 */
export function QyAdminViolationRules() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [page, setPage] = useState(1)
  const [phase, setPhase] = useState<string>(ALL_PHASES)
  const [keyword, setKeyword] = useState('')
  const [editing, setEditing] = useState<QyViolationRule | null>(null)
  const [sheetOpen, setSheetOpen] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<QyViolationRule | null>(
    null
  )

  const params = useMemo(
    () => ({
      p: page,
      page_size: PAGE_SIZE,
      phase: phase === ALL_PHASES ? undefined : phase,
      keyword: keyword.trim() === '' ? undefined : keyword.trim(),
    }),
    [keyword, page, phase]
  )

  const rulesQuery = useQuery({
    queryKey: qyKeys.adminViolationRules(params),
    queryFn: () => listQyViolationRules(params),
    staleTime: 15_000,
  })

  const statsQuery = useQuery({
    queryKey: qyKeys.adminViolationStats(),
    queryFn: () => getQyViolationStats({ hours: 24 }),
    staleTime: 30_000,
    refetchInterval: 60_000,
  })

  const breakerMutation = useMutation({
    mutationFn: resetQyViolationBreaker,
    onSuccess: () => {
      toast.success(t('qy_vio_breaker_reset_done'))
      void queryClient.invalidateQueries({
        queryKey: qyKeys.adminViolationStats(),
      })
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const deleteMutation = useMutation({
    mutationFn: (rule: QyViolationRule) => deleteQyViolationRule(rule.id),
    onSuccess: () => {
      toast.success(t('qy_vio_rule_deleted'))
      setPendingDelete(null)
      void queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const rules = rulesQuery.data?.items ?? []

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {t('qy_vio_rules_title')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button
          type='button'
          onClick={() => {
            setEditing(null)
            setSheetOpen(true)
          }}
        >
          <Plus aria-hidden='true' />
          {t('qy_vio_rule_create')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='space-y-3'>
          <QyViolationShadowBanner
            stats={statsQuery.data}
            onResetBreaker={() => breakerMutation.mutate()}
            isResetting={breakerMutation.isPending}
          />

          <QyFilterBar>
            <QyFilterField label={t('qy_vio_field_phase')}>
              <Select
                value={phase}
                onValueChange={(value) => {
                  setPhase(value ?? ALL_PHASES)
                  setPage(1)
                }}
              >
                <SelectTrigger className='w-40'>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL_PHASES}>
                    {t('qy_common_all')}
                  </SelectItem>
                  {QY_VIOLATION_PHASES.map((item) => (
                    <SelectItem key={item} value={item}>
                      {t(`qy_vio_phase_${item}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </QyFilterField>
            <QyFilterField label={t('qy_vio_field_name')}>
              <Input
                className='w-48'
                value={keyword}
                onChange={(event) => {
                  setKeyword(event.target.value)
                  setPage(1)
                }}
                placeholder={t('qy_vio_rule_search')}
              />
            </QyFilterField>
            <Button
              type='button'
              variant='outline'
              size='sm'
              disabled={rulesQuery.isFetching}
              onClick={() => {
                void rulesQuery.refetch()
              }}
            >
              <RefreshCw
                aria-hidden='true'
                className={rulesQuery.isFetching ? 'animate-spin' : undefined}
              />
              {t('qy_common_refresh')}
            </Button>
          </QyFilterBar>

          <QyPageBoundary
            query={rulesQuery}
            isEmpty={rulesQuery.data != null && rules.length === 0}
            emptyIcon={ShieldAlert}
            emptyTitle={t('qy_vio_rules_empty')}
            emptyDescription={t('qy_vio_rules_empty_desc')}
          >
            <div className='space-y-3'>
              <StaticDataTable
                data={rules}
                getRowKey={(row) => row.id}
                columns={[
                  {
                    id: 'priority',
                    header: t('qy_vio_field_priority'),
                    cellClassName: 'tabular-nums',
                    cell: (row: QyViolationRule) => row.priority,
                  },
                  {
                    id: 'name',
                    header: t('qy_vio_field_name'),
                    cell: (row: QyViolationRule) => row.name,
                  },
                  {
                    id: 'phase',
                    header: t('qy_vio_field_phase'),
                    cell: (row: QyViolationRule) =>
                      t(`qy_vio_phase_${row.phase}`),
                  },
                  {
                    id: 'match',
                    header: t('qy_vio_field_match_type'),
                    cell: (row: QyViolationRule) =>
                      t(`qy_vio_match_${row.match_type}`),
                  },
                  {
                    id: 'action',
                    header: t('qy_vio_field_action'),
                    cell: (row: QyViolationRule) =>
                      t(`qy_vio_action_${row.action}`),
                  },
                  {
                    id: 'fee',
                    header: t('qy_vio_field_fee_mode'),
                    cell: (row: QyViolationRule) =>
                      t(`qy_vio_fee_${row.fee_mode}`),
                  },
                  {
                    id: 'scope',
                    header: t('qy_vio_field_scope'),
                    cell: (row: QyViolationRule) =>
                      [row.model_scope, row.group_scope]
                        .filter((item) => item !== '')
                        .join(' / ') || t('qy_vio_scope_all'),
                  },
                  {
                    id: 'state',
                    header: t('qy_common_status'),
                    cell: (row: QyViolationRule) => (
                      <span className='flex flex-wrap items-center gap-1'>
                        <StatusBadge
                          label={
                            row.enabled
                              ? t('qy_vio_rule_enabled')
                              : t('qy_vio_rule_disabled')
                          }
                          variant={row.enabled ? 'success' : 'neutral'}
                          copyable={false}
                        />
                        {/* 规则级影子必须在列表就看得见：否则管理员会以为
                            这条规则已经在扣费，实际只是在记录。 */}
                        {row.dry_run && (
                          <Badge variant='outline'>
                            {t('qy_vio_field_dry_run')}
                          </Badge>
                        )}
                      </span>
                    ),
                  },
                  {
                    id: 'updated_at',
                    header: t('qy_common_updated_at'),
                    cell: (row: QyViolationRule) =>
                      row.updated_at === 0
                        ? QY_EMPTY_TEXT
                        : formatQyTs(row.updated_at),
                  },
                  {
                    id: 'actions',
                    header: t('qy_common_actions'),
                    cell: (row: QyViolationRule) => (
                      <span className='flex items-center gap-1'>
                        <Button
                          type='button'
                          variant='ghost'
                          size='icon-sm'
                          aria-label={t('qy_vio_rule_edit')}
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
                          aria-label={t('qy_vio_rule_delete')}
                          onClick={() => setPendingDelete(row)}
                        >
                          <Trash2 aria-hidden='true' />
                        </Button>
                      </span>
                    ),
                  },
                ]}
              />

              <QyPager
                page={page}
                pageSize={PAGE_SIZE}
                total={rulesQuery.data?.total ?? 0}
                onPageChange={setPage}
              />
            </div>
          </QyPageBoundary>

          <QyRuleFormSheet
            open={sheetOpen}
            onOpenChange={setSheetOpen}
            rule={editing}
            onSaved={() => {
              void queryClient.invalidateQueries({ queryKey: qyKeys.all })
            }}
          />

          <QyConfirmDialog
            open={pendingDelete != null}
            onOpenChange={(open) => {
              if (!open) setPendingDelete(null)
            }}
            title={t('qy_vio_rule_delete')}
            description={t('qy_vio_rule_delete_desc', {
              name: pendingDelete?.name ?? '',
            })}
            confirmText={t('qy_vio_rule_delete')}
            isLoading={deleteMutation.isPending}
            onConfirm={() => {
              if (pendingDelete != null) deleteMutation.mutate(pendingDelete)
            }}
          />
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
