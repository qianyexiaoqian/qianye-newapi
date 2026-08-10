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
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'

import { QyConfirmDialog } from '../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { qyKeys } from '../../lib/query-keys'
import type { QyPage } from '../../lib/types'
import { QyPager } from '../components/qy-pager'
import { qyOpsErrorMessage } from '../ops/errors'
import { formatQyTs, QY_EMPTY_TEXT } from '../ops/format'
import { QyFilterBar, QyFilterField } from '../ops/qy-ops-ui'
import {
  deleteQyViolationRule,
  getQyViolationStats,
  listQyViolationRules,
  resetQyViolationBreaker,
  setQyViolationRuleEnabled,
} from './api'
import { QyBuiltinPackSheet } from './components/builtin-pack-sheet'
import { QyRuleBatchBar } from './components/rule-batch-bar'
import { QyRuleFormSheet } from './components/rule-form-sheet'
import { QyShadowHitsSheet } from './components/shadow-hits-sheet'
import { QyViolationCounterCard } from './components/violation-counter-card'
import { QyViolationShadowBanner } from './components/violation-shadow-banner'
import { QY_VIOLATION_PHASES } from './lib/rule-form'
import { qyToggleNeedsConfirm, type QyPendingToggle } from './lib/rule-toggle'
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
  const [pendingToggle, setPendingToggle] = useState<QyPendingToggle | null>(
    null
  )
  // 影子命中面板挂在规则行上：从「我改了这条规则」到「我看它抓到了什么」
  // 必须是一次点击 —— 那正是项目方给影子模式定的唯一用途。
  const [shadowRule, setShadowRule] = useState<QyViolationRule | null>(null)
  // 多选。**只存 id，选中集永远与当前这一页取交集**：批量的影响面提示要读
  // `mode` 与 `enabled`，而翻页之后上一页的规则行已经不在手上，拿一份过期拷贝
  // 去算「其中几条是真实模式」会算出一个与库里不符的数字 —— 而那个数字正是
  // 二次确认框里唯一要人做判断的东西。
  const [selectedIds, setSelectedIds] = useState<ReadonlySet<number>>(
    () => new Set()
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

  // 换页 / 换筛选就清空勾选。不清的话，勾选集里会留着一批**屏幕上已经看不见**的
  // 规则，而批量按钮上的数字照旧把它们算进去 —— 那是一次没有人看过内容的批量。
  useEffect(() => {
    setSelectedIds(new Set())
  }, [params])

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

  /**
   * 行内启停。
   *
   * 乐观更新是必需的而不是锦上添花：这一页最典型的动作是「导入内置规则包之后
   * 逐条点开」，每点一次都等一个往返 + 一次列表重拉，开关会在原地闪回旧状态再
   * 跳到新状态 —— 那个闪回看起来就像「没点上」，于是人会再点一次，把刚打开的又关掉。
   *
   * 失败必须回滚到**接口调用前那一份缓存**，而不是把开关取反：中途可能已经有别的
   * 刷新落地，取反会写出一个第三种状态。
   */
  const toggleMutation = useMutation({
    mutationFn: (vars: QyPendingToggle) =>
      setQyViolationRuleEnabled(vars.rule.id, vars.next),
    onMutate: async (vars) => {
      const key = qyKeys.adminViolationRules(params)
      // 取消在途查询：一次 15 秒前发出的列表请求在乐观更新之后落地，
      // 会把开关原样按回旧值，而用户看到的是「点了又弹回去」。
      await queryClient.cancelQueries({ queryKey: key })
      const previous =
        queryClient.getQueryData<QyPage<QyViolationRule>>(key) ?? null
      queryClient.setQueryData<QyPage<QyViolationRule>>(key, (old) =>
        old == null
          ? old
          : {
              ...old,
              items: old.items.map((item) =>
                item.id === vars.rule.id
                  ? { ...item, enabled: vars.next }
                  : item
              ),
            }
      )
      return { key, previous }
    },
    onError: (error, _vars, context) => {
      if (context != null) {
        queryClient.setQueryData(context.key, context.previous)
      }
      toast.error(qyOpsErrorMessage(error, t))
    },
    onSuccess: (result) => {
      // changed=false = 后端认定什么都没发生（重复点击，或别人抢先改成了同一个值）。
      // 报成「已启用」会让人以为自己改动了状态，而审计里根本没有这一条。
      if (!result.changed) {
        toast.info(t('qy_vio_rule_toggle_unchanged'))
        return
      }
      toast.success(
        result.enabled
          ? t('qy_vio_rule_toggle_enabled')
          : t('qy_vio_rule_toggle_disabled')
      )
    },
    onSettled: () => {
      void queryClient.invalidateQueries({
        queryKey: qyKeys.adminViolationRules(params),
      })
      // 统计里的「影子 N 条 / 真实 N 条」只数**启用中**的规则，
      // 不一起失效的话顶部横幅会继续显示一个已经不成立的口径。
      void queryClient.invalidateQueries({
        queryKey: qyKeys.adminViolationStats(),
      })
      // 内置规则包面板的「已停用」徽标读的正是 rule_enabled，而那份查询有
      // 15 秒 staleTime：不一起失效的话，刚停用的规则在面板上仍显示成
      // 「已是最新 + 影子」，管理员据此点「导入 / 同时升级」，以为规则会跑起来 ——
      // 而导入一个字节都不动 enabled。那个徽标存在的全部理由就是堵这个洞。
      void queryClient.invalidateQueries({
        queryKey: qyKeys.adminViolationBuiltin(),
      })
    },
  })

  const requestToggle = (rule: QyViolationRule, next: boolean) => {
    if (qyToggleNeedsConfirm(rule, next)) {
      setPendingToggle({ rule, next })
      return
    }
    toggleMutation.mutate({ rule, next })
  }

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
  const selectedRules = rules.filter((rule) => selectedIds.has(rule.id))
  const allSelected = rules.length > 0 && selectedRules.length === rules.length

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
              {/* 批量操作条只在有勾选时出现。常驻一条「已选 0 条」的空工具栏，
                  只会让这一页多一行永远无事可做的像素。 */}
              {selectedRules.length > 0 && (
                <QyRuleBatchBar
                  selected={selectedRules}
                  onClear={() => setSelectedIds(new Set())}
                  onDone={() => setSelectedIds(new Set())}
                />
              )}
              <StaticDataTable
                data={rules}
                getRowKey={(row) => row.id}
                columns={[
                  {
                    id: 'select',
                    // 表头的全选只作用于**当前这一页**。跨页全选是一个看不见
                    // 内容的批量：勾选框上写着 200，而屏幕上只有 20 行，
                    // 剩下 180 条改了什么没有人看过。
                    header: (
                      <Checkbox
                        checked={allSelected}
                        disabled={rules.length === 0}
                        aria-label={t('qy_vio_batch_select_all')}
                        onCheckedChange={(checked) => {
                          setSelectedIds(
                            checked === true
                              ? new Set(rules.map((rule) => rule.id))
                              : new Set()
                          )
                        }}
                      />
                    ),
                    className: 'w-10',
                    cell: (row: QyViolationRule) => (
                      <Checkbox
                        checked={selectedIds.has(row.id)}
                        aria-label={t('qy_vio_batch_select_one', {
                          name: row.name,
                        })}
                        onCheckedChange={(checked) => {
                          setSelectedIds((prev) => {
                            const next = new Set(prev)
                            if (checked === true) next.add(row.id)
                            else next.delete(row.id)
                            return next
                          })
                        }}
                      />
                    ),
                  },
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
                        {/* 开关本身就是状态显示。项目方原话:「规则集这里加一个
                            快速启用关闭的按钮」。另挂一个只读徽标 + 一个开关的话,
                            乐观更新期间两者会短暂地互相矛盾,而那一刻恰恰是
                            用户最需要相信界面的时候。 */}
                        <Switch
                          checked={row.enabled}
                          disabled={
                            toggleMutation.isPending &&
                            toggleMutation.variables?.rule.id === row.id
                          }
                          aria-label={t('qy_vio_rule_toggle_aria', {
                            name: row.name,
                          })}
                          onCheckedChange={(checked) => {
                            requestToggle(row, checked === true)
                          }}
                        />
                        <span className='text-muted-foreground text-xs'>
                          {row.enabled
                            ? t('qy_vio_rule_enabled')
                            : t('qy_vio_rule_disabled')}
                        </span>
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

          {/* 启停的二次确认。取舍写在 qyToggleNeedsConfirm 上：只拦「任何停用」
              与「启用真实模式规则」两种方向，启用影子规则一点即生效。 */}
          <QyConfirmDialog
            open={pendingToggle != null}
            onOpenChange={(open) => {
              if (!open) setPendingToggle(null)
            }}
            title={
              pendingToggle?.next === true
                ? t('qy_vio_rule_enable_enforce_title')
                : t('qy_vio_rule_disable_title')
            }
            description={
              pendingToggle?.next === true
                ? t('qy_vio_rule_enable_enforce_desc', {
                    name: pendingToggle.rule.name,
                  })
                : t('qy_vio_rule_disable_desc', {
                    name: pendingToggle?.rule.name ?? '',
                  })
            }
            confirmText={
              pendingToggle?.next === true
                ? t('qy_vio_rule_enable_confirm')
                : t('qy_vio_rule_disable_confirm')
            }
            isLoading={toggleMutation.isPending}
            onConfirm={() => {
              if (pendingToggle == null) return
              toggleMutation.mutate(pendingToggle)
              setPendingToggle(null)
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
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
