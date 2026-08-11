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
import {
  Archive,
  Eye,
  EyeOff,
  Pencil,
  Plus,
  ScrollText,
  Wand2,
} from 'lucide-react'
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
import { qyThresholdStateKey } from '../../lib/violation-thresholds'
import { qyOpsErrorMessage } from '../ops/errors'
import {
  qyAdminViolationCategoriesQuery,
  qyArchiveViolationCategory,
} from './api'
import { QyViolationCategoryFormSheet } from './components/category-form-sheet'
import { QySuggestedThresholdsDialog } from './components/suggested-thresholds-dialog'
import type { QyViolationCategory, QyViolationCategoryRow } from './types'

/**
 * 违规类型。
 *
 * 项目方原话：「可以编辑/添加/删除违规类型。违规规则计数绑定到违规类型，
 * 违规类型计次达到多少次封号。这些在用户前端要公示出来。」
 *
 * 这一页要让运营一眼看懂三件事，页面结构就是照这三件事排的：
 *
 *  1. **每一类几次会被处置**（阈值列）。它与「处置策略档」里的账号总量线是
 *     **两条独立的线，任一越过即触发**。页面顶部那句说明就是在回答
 *     「到底几次封号」—— 少了它，两个数字摆在两页上没人对得起来。
 *  2. **这一类下面有几条规则**（规则数列）。归档时这个数决定那句
 *     「这 N 条规则会被改绑到「未分类」」。
 *  3. **公示了没有**。未公示不等于不生效：阈值照样算、照样封号，只是用户看不到。
 *     这两件事在列里是分开的两格，不能合成一个开关。
 */
export function QyAdminViolationCategories() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery(qyAdminViolationCategoriesQuery())

  const [editing, setEditing] = useState<QyViolationCategory | null>(null)
  const [sheetOpen, setSheetOpen] = useState(false)
  const [pendingArchive, setPendingArchive] =
    useState<QyViolationCategoryRow | null>(null)
  const [suggestOpen, setSuggestOpen] = useState(false)

  const archiveMutation = useMutation({
    mutationFn: (row: QyViolationCategoryRow) =>
      qyArchiveViolationCategory(row.category.id),
    onSuccess: async (res) => {
      toast.success(t('qy_vcat_archived', { count: res.moved_rules }))
      setPendingArchive(null)
      await queryClient.invalidateQueries({
        queryKey: qyKeys.adminViolationCategories(),
      })
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const rows = query.data?.items ?? []
  // 「还没配线」的类型数。出厂时它是 6 —— 也就是"到多少次封号"这件事在界面上
  // 一次都没有被回答过。把它顶在页面上，是因为这一页最容易被误读成"已经配好了"：
  // 六行齐齐整整、每行都有阈值列，只是列里全写着"不计门槛"。
  const unsetCount = rows.filter(
    (row) => row.threshold_state === 'unset' && !row.category.is_fallback
  ).length
  // 「一个都没配」是与「还有 N 个没配」性质不同的一件事，所以单独说一句。
  //
  // 上面那条横幅刻意把兜底排除在外（它不是业务类型），可现网正好是**全部**
  // 六类连同兜底一起都是 0 —— 也就是说单类型线一次都不会触发。那条按业务类型
  // 计数的横幅在这一档只说「还有 6 个没配线」，读起来像"配了几个、还差几个"，
  // 而真相是"这条线整条是关的"。规则表单那一格已经按单条规则说过一次，
  // 这里是全站口径的那一句。
  const allIdle =
    rows.length > 0 && rows.every((row) => row.threshold_state !== 'active')

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_violation_categories')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button
          variant='outline'
          size='sm'
          render={<Link to='/qy/admin/violation-rules' />}
        >
          <ScrollText aria-hidden='true' />
          {t('qy_nav_a_violation_rules')}
        </Button>
        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={() => setSuggestOpen(true)}
        >
          <Wand2 aria-hidden='true' />
          {t('qy_vcat_sug_open')}
        </Button>
        <Button
          type='button'
          onClick={() => {
            setEditing(null)
            setSheetOpen(true)
          }}
        >
          <Plus aria-hidden='true' />
          {t('qy_vcat_create')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <QyPageBoundary query={query}>
          <div className='space-y-4'>
            {/* 「到底几次封号」的唯一答案。两条线摆在两页上，这句话是它们之间
                唯一的连接 —— 没有它，管理员会以为类型阈值取代了分组策略档。 */}
            <p className='text-muted-foreground rounded-md border border-dashed p-3 text-xs'>
              {t('qy_vcat_two_lines_note')}
            </p>

            {allIdle && (
              <p className='text-warning border-warning/40 bg-warning/5 rounded-md border p-3 text-xs'>
                {t('qy_vcat_all_idle_banner')}
              </p>
            )}

            {/* 「还有 N 类没配线」。不给这一条的话，六行"不计门槛"看起来
                与"已经配好了"没有区别 —— 而它们的差别是这套功能生效没有。 */}
            {unsetCount > 0 && (
              <div className='flex flex-wrap items-center justify-between gap-2 rounded-md border border-dashed p-3 text-xs'>
                <span>{t('qy_vcat_unset_banner', { count: unsetCount })}</span>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={() => setSuggestOpen(true)}
                >
                  <Wand2 aria-hidden='true' />
                  {t('qy_vcat_sug_open')}
                </Button>
              </div>
            )}

            <StaticDataTable
              data={rows}
              getRowKey={(row) => row.category.id}
              columns={[
                {
                  id: 'name',
                  header: t('qy_vcat_col_name'),
                  cell: (row: QyViolationCategoryRow) => (
                    <span className='flex flex-wrap items-center gap-1'>
                      {row.category.name}
                      <code className='text-muted-foreground text-xs'>
                        {row.category.key}
                      </code>
                      {row.category.is_fallback && (
                        <Badge variant='outline'>
                          {t('qy_vcat_flag_fallback')}
                        </Badge>
                      )}
                    </span>
                  ),
                },
                {
                  id: 'threshold',
                  header: t('qy_vcat_col_threshold'),
                  // 三态各一句话，**由后端下发的 threshold_state 决定**：
                  // 「还没配」与「配了但关着」在封号判定上等价，在这一列上却是
                  // 两句不同的话。把它们塌成同一个 0 正是项目方看到的现象。
                  cell: (row: QyViolationCategoryRow) => (
                    <span
                      className={
                        row.threshold_state === 'active'
                          ? undefined
                          : 'text-muted-foreground'
                      }
                    >
                      {t(qyThresholdStateKey(row.threshold_state), {
                        count: row.category.threshold,
                        hours: row.category.window_hours,
                      })}
                    </span>
                  ),
                },
                {
                  id: 'rules',
                  header: t('qy_vcat_col_rules'),
                  cell: (row: QyViolationCategoryRow) => row.rule_count,
                },
                {
                  id: 'published',
                  header: t('qy_vcat_col_published'),
                  cell: (row: QyViolationCategoryRow) => (
                    <StatusBadge
                      label={
                        row.category.published
                          ? t('qy_vcat_published_yes')
                          : t('qy_vcat_published_no')
                      }
                      variant={row.category.published ? 'success' : 'neutral'}
                      copyable={false}
                    />
                  ),
                },
                {
                  id: 'public_title',
                  header: t('qy_vcat_col_public_title'),
                  cell: (row: QyViolationCategoryRow) => (
                    <span className='flex items-center gap-1'>
                      {row.category.published ? (
                        <Eye aria-hidden='true' className='size-3.5' />
                      ) : (
                        <EyeOff aria-hidden='true' className='size-3.5' />
                      )}
                      {row.category.public_title || '—'}
                    </span>
                  ),
                },
                {
                  id: 'actions',
                  header: t('qy_common_actions'),
                  cell: (row: QyViolationCategoryRow) => (
                    <span className='flex gap-2'>
                      <Button
                        type='button'
                        variant='outline'
                        size='sm'
                        onClick={() => {
                          setEditing(row.category)
                          setSheetOpen(true)
                        }}
                      >
                        <Pencil aria-hidden='true' />
                        {t('qy_common_edit')}
                      </Button>
                      <Button
                        type='button'
                        variant='outline'
                        size='sm'
                        // 兜底类型不可归档：归档它会让没有显式类型的规则变成
                        // 无类型的孤儿，它们的命中从此不计入任何类型线。
                        disabled={row.category.is_fallback}
                        onClick={() => setPendingArchive(row)}
                      >
                        <Archive aria-hidden='true' />
                        {t('qy_vcat_archive')}
                      </Button>
                    </span>
                  ),
                },
              ]}
            />
          </div>
        </QyPageBoundary>

        <QySuggestedThresholdsDialog
          open={suggestOpen}
          onOpenChange={setSuggestOpen}
        />

        <QyViolationCategoryFormSheet
          open={sheetOpen}
          onOpenChange={setSheetOpen}
          category={editing}
          onSaved={() => {
            void queryClient.invalidateQueries({
              queryKey: qyKeys.adminViolationCategories(),
            })
          }}
        />

        {/* 归档确认。文案必须把三件事都说出来，否则管理员会以为这是删除：
            历史记录全部保留、规则改绑到哪里、以及"不再有新记录落到这一类"。 */}
        <QyConfirmDialog
          open={pendingArchive != null}
          onOpenChange={(open) => {
            if (!open) setPendingArchive(null)
          }}
          title={t('qy_vcat_archive_title', {
            name: pendingArchive?.category.name ?? '',
          })}
          description={t('qy_vcat_archive_desc', {
            count: pendingArchive?.rule_count ?? 0,
          })}
          details={
            <p className='text-muted-foreground text-xs'>
              {t('qy_vcat_archive_records_note')}
            </p>
          }
          isLoading={archiveMutation.isPending}
          onConfirm={() => {
            if (pendingArchive != null) archiveMutation.mutate(pendingArchive)
          }}
        />
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
