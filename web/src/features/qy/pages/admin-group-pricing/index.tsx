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
import { Plus, Tags } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useDebounce } from '@/hooks'

import { QyConfirmDialog } from '../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { qyKeys } from '../../lib/query-keys'
import { QyPager } from '../components/qy-pager'
import { qyOpsErrorMessage } from '../ops/errors'
import { qyDeleteGpRule, qyGpOptionsQuery, qyGpRulesQuery } from './api'
import { QyGpModeBanner } from './components/mode-banner'
import { QyGpRuleFormSheet } from './components/rule-form-sheet'
import { QyGpRulesFilterBar } from './components/rules-filter-bar'
import { QyGpRulesTable } from './components/rules-table'
import { QyGpShadowPanel } from './components/shadow-panel'
import type { QyGpRule } from './types'

const PAGE_SIZE = 20

/**
 * 模型按分组单独定价（管理端）。
 *
 * 三块内容，顺序不能反：
 *
 *  1. **模式横幅在最上**。同一张表在影子模式下是预演、在真实模式下正在扣钱，
 *     不先回答「现在是哪种」，下面所有数字的含义都是悬空的。
 *  2. **规则表**。每一行都显示后端算好的「分组级价 × 分组倍率 = 实际扣费」，
 *     而不是只显示录入值 —— 用户拍板的是相乘方案，录入值本身不是任何人被扣的钱。
 *  3. **差额对账**。影子模式记录的「若启用会多收/少收多少」，
 *     它是「能不能切到真实模式」这个决定的唯一依据。
 */
export function QyAdminGroupPricing() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [page, setPage] = useState(1)
  const [groupFilter, setGroupFilter] = useState('')
  const [modelFilter, setModelFilter] = useState('')
  const [modeFilter, setModeFilter] = useState('')

  const [editing, setEditing] = useState<QyGpRule | null>(null)
  const [sheetOpen, setSheetOpen] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<QyGpRule | null>(null)

  // 模型是模糊匹配，逐字符发请求会把列表打成一台打字机。
  const debouncedModel = useDebounce(modelFilter.trim(), 350)

  const params = useMemo(
    () => ({
      p: page,
      page_size: PAGE_SIZE,
      group_name: groupFilter === '' ? undefined : groupFilter,
      model_name: debouncedModel === '' ? undefined : debouncedModel,
      mode: modeFilter === '' ? undefined : modeFilter,
    }),
    [debouncedModel, groupFilter, modeFilter, page]
  )

  const query = useQuery(qyGpRulesQuery(params))
  const options = useQuery(qyGpOptionsQuery())
  const data = query.data

  // 筛选一变就回到第一页：停在第 3 页却只剩 1 页数据时，用户看到的是空表。
  useEffect(() => {
    setPage(1)
  }, [debouncedModel, groupFilter, modeFilter])

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: qyKeys.adminGroupPricing() })

  const deleteMutation = useMutation({
    mutationFn: (rule: QyGpRule) => qyDeleteGpRule(rule.id),
    onSuccess: async () => {
      toast.success(t('qy_gp_deleted'))
      setPendingDelete(null)
      await invalidate()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const rules = data?.items ?? []
  // `shadow_mode` 缺省时**当作影子**。字段没到 = 不知道，而在不知道的情况下
  // 宣称「正在真实扣费」，会让人把一份预演当成上线配置去核对；反过来把真实
  // 计费误标成影子，则会让人以为改价没有后果 —— 后者才是不可逆的那一侧。
  const shadowMode = data?.shadow_mode !== false
  const enabledRuleCount = rules.filter((rule) => rule.enabled).length

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {t('qy_nav_a_group_pricing')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button
          type='button'
          disabled={data == null}
          onClick={() => {
            setEditing(null)
            setSheetOpen(true)
          }}
        >
          <Plus aria-hidden='true' />
          {t('qy_gp_create')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <QyPageBoundary query={query}>
          {data != null && (
            <div className='space-y-4'>
              <QyGpModeBanner
                shadowMode={shadowMode}
                enabledRuleCount={enabledRuleCount}
              />

              <Tabs defaultValue='rules'>
                <TabsList>
                  <TabsTrigger value='rules'>
                    {t('qy_gp_tab_rules')}
                  </TabsTrigger>
                  <TabsTrigger value='shadow'>
                    {t('qy_gp_tab_shadow')}
                  </TabsTrigger>
                </TabsList>

                <TabsContent value='rules' className='space-y-4'>
                  <QyGpRulesFilterBar
                    groups={options.data?.groups ?? []}
                    group={groupFilter}
                    onGroupChange={setGroupFilter}
                    model={modelFilter}
                    onModelChange={setModelFilter}
                    mode={modeFilter}
                    onModeChange={setModeFilter}
                  />

                  {rules.length === 0 ? (
                    /* 空表 = 所有模型都走全局价。不说清楚的话，管理员看到一张
                       空表会以为是「还没加载出来」或「功能没生效」。 */
                    <div className='text-muted-foreground rounded-lg border border-dashed p-6 text-center text-sm'>
                      <Tags
                        className='mx-auto mb-2 size-6 opacity-60'
                        aria-hidden='true'
                      />
                      <p className='text-foreground font-medium'>
                        {t('qy_gp_empty_title')}
                      </p>
                      <p className='mt-1'>{t('qy_gp_empty_desc')}</p>
                    </div>
                  ) : (
                    <QyGpRulesTable
                      rules={rules}
                      shadowMode={shadowMode}
                      onEdit={(rule) => {
                        setEditing(rule)
                        setSheetOpen(true)
                      }}
                      onDelete={setPendingDelete}
                    />
                  )}

                  <QyPager
                    page={page}
                    pageSize={PAGE_SIZE}
                    total={data.total}
                    onPageChange={setPage}
                    disabled={query.isFetching}
                  />
                </TabsContent>

                <TabsContent value='shadow'>
                  <QyGpShadowPanel shadowMode={shadowMode} />
                </TabsContent>
              </Tabs>
            </div>
          )}
        </QyPageBoundary>
      </SectionPageLayout.Content>

      <QyGpRuleFormSheet
        open={sheetOpen}
        onOpenChange={setSheetOpen}
        rule={editing}
        groups={options.data?.groups ?? []}
        models={options.data?.models ?? []}
        shadowMode={shadowMode}
        onSaved={() => {
          void invalidate()
        }}
      />

      <QyConfirmDialog
        open={pendingDelete != null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null)
        }}
        title={t('qy_gp_delete')}
        description={t('qy_gp_delete_desc', {
          group: pendingDelete?.group_name ?? '',
          model: pendingDelete?.model_name ?? '',
        })}
        // 删一条正在生效的规则 = 该分组立刻回到全局价，那同样是一次改价。
        irreversible={!shadowMode && pendingDelete?.enabled === true}
        confirmText={t('qy_gp_delete')}
        isLoading={deleteMutation.isPending}
        onConfirm={() => {
          if (pendingDelete != null) deleteMutation.mutate(pendingDelete)
        }}
      />
    </SectionPageLayout>
  )
}
