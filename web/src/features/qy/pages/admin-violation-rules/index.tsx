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
  Eye,
  Gauge,
  PackagePlus,
  Pencil,
  Plus,
  RefreshCw,
  ShieldAlert,
  Trash2,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
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
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
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
import { QyBuiltinPackSheet } from './components/builtin-pack-sheet'
import { QyRuleFormSheet } from './components/rule-form-sheet'
import { QyShadowHitsSheet } from './components/shadow-hits-sheet'
import { QyViolationCounterCard } from './components/violation-counter-card'
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
  const [builtinOpen, setBuiltinOpen] = useState(false)
  // 影子命中面板挂在规则行上：从「我改了这条规则」到「我看它抓到了什么」
  // 必须是一次点击 —— 那正是项目方给影子模式定的唯一用途。
  const [shadowRule, setShadowRule] = useState<QyViolationRule | null>(null)

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
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_vio_rules_title')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        {/* 内置防护规则包。放在「新建规则」左边是刻意的：一张空规则表面前,
            「让我自己想出攻击特征串」这件事没有人做得到,先导入再改窄才是
            可行的路径。 */}
        <Button
          type='button'
          variant='outline'
          onClick={() => setBuiltinOpen(true)}
        >
          <PackagePlus aria-hidden='true' />
          {t('qy_vio_builtin_open')}
        </Button>
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
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <div className='space-y-3'>
          <QyViolationShadowBanner
            stats={statsQuery.data}
            onResetBreaker={() => breakerMutation.mutate()}
            isResetting={breakerMutation.isPending}
          />

          {/* 频率判据的计数降级。这个计数器只在存在 request_rate 规则时才会被
              推进，所以它自己就是「这一页需不需要看这条提示」的开关。
              不摆出来的话，运营会照着被稀释成 1/N 的数字一路调低阈值，
              等 Redis 恢复、真实计数回来时一次性误伤一大批人。 */}
          {(statsQuery.data?.breaker.rate_local_hits ?? 0) > 0 && (
            <Alert className='border-warning/40 bg-warning/5 [&>svg]:text-warning'>
              <Gauge />
              <AlertTitle>{t('qy_vio_rate_degraded_title')}</AlertTitle>
              <AlertDescription>
                {t('qy_vio_rate_degraded_desc')}
              </AlertDescription>
            </Alert>
          )}

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
                    // 豁免方向必须在列表就看得见：同一串分组名在两种方向下
                    // 的含义完全相反，只显示名单等于把最要紧的一半藏起来。
                    cell: (row: QyViolationRule) =>
                      [
                        row.model_scope,
                        row.group_scope === ''
                          ? ''
                          : `${t(`qy_vio_group_scope_mode_${row.group_scope_mode === 'exclude' ? 'exclude' : 'include'}`)}: ${row.group_scope}`,
                      ]
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
                        {/* 模式必须在列表就看得见,而且两种取值都要显示。
                            只在影子时才挂一个 Badge 的话,「真实执行」就成了
                            一个靠"没有标记"表达的状态 —— 而那与"这一列还没
                            加载出来"在视觉上完全一样。 */}
                        <Badge
                          variant={
                            row.mode === 'enforce' ? 'destructive' : 'outline'
                          }
                        >
                          {t(
                            `qy_vio_mode_${row.mode === 'enforce' ? 'enforce' : 'shadow'}`
                          )}
                        </Badge>
                        {row.source === 'builtin' && (
                          <Badge variant='secondary'>
                            {t('qy_vio_source_builtin')}
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
                          aria-label={t('qy_vio_shadow_hits_open')}
                          onClick={() => setShadowRule(row)}
                        >
                          <Eye aria-hidden='true' />
                        </Button>
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

          <QyBuiltinPackSheet
            open={builtinOpen}
            onOpenChange={setBuiltinOpen}
            onImported={() => {
              void queryClient.invalidateQueries({ queryKey: qyKeys.all })
            }}
          />

          <QyShadowHitsSheet
            open={shadowRule != null}
            onOpenChange={(open) => {
              if (!open) setShadowRule(null)
            }}
            rule={shadowRule}
          />

          <QyRuleFormSheet
            open={sheetOpen}
            onOpenChange={setSheetOpen}
            rule={editing}
            onSaved={() => {
              void queryClient.invalidateQueries({ queryKey: qyKeys.all })
            }}
          />

          {/* 计数器维护紧挨着模式开关:两者说的是同一件事的两面 ——
              影子期间不再累计违规次数,而切换之前累计下来的那些是脏的。 */}
          <QyViolationCounterCard />

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
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
