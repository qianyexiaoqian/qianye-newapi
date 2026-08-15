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
import { Users } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { QyConfirmDialog } from '../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { qyKeys } from '../../lib/query-keys'
import { qyGmOrphansQuery, qyGmRepairToken } from '../admin-group-matrix/api'
import { QyGmAxisLegend } from '../admin-group-matrix/components/axis-legend'
import { QyGmDiffBar } from '../admin-group-matrix/components/diff-bar'
import { QyGmMatrixGrid } from '../admin-group-matrix/components/matrix-grid'
import { QyGmOrphansPanel } from '../admin-group-matrix/components/orphans-panel'
import { QyGmPreviewDialog } from '../admin-group-matrix/components/preview-dialog'
import { QyGmScopeSheet } from '../admin-group-matrix/components/scope-sheet'
import { QyGmStatusBanners } from '../admin-group-matrix/components/status-banners'
import { useQyGmEditor } from '../admin-group-matrix/lib/use-editor'
import { qyOpsErrorMessage } from '../ops/errors'
import { QyUgGroupDetail } from './components/user-group-detail'
import { QyUgUserGroupList } from './components/user-group-list'
import { qyUgFilterGroups, qyUgResolveSelected } from './lib/rows'

/**
 * 「用户分组」矩阵页（系统设置 → 计费与支付 → 用户分组可用的模型分组配置）。
 *
 * 项目方原话：「用户分组写一个单独页面，这个页面你可以画 2 个列表框，新增用户
 * 分组，点击分组后进行模型分组单独分配（调整单独用户分组倍率）」。
 *
 * ── 需求 4 之后：这一页降级为**次要视图**，主入口是表行上的弹窗 ──
 *
 * 项目方后来点名「不要再跳转」，于是「用户分组」那张表的行内直接开
 * {@link QyUgScopeDialog}（同一份状态机、同一道闸门）。这一页留下来，因为它
 * 有三件在主从式 / 弹窗里**结构性表达不出来**的事：整列批量、跨档对比
 * 「哪几档人能到达某个模型分组」、以及孤儿令牌基线。它们一次要看多个用户分组，
 * 而弹窗一次只看一档。
 *
 * ── 三份状态的分工 ──
 * 全部收在 {@link useQyGmEditor} 里：倍率的唯一真相源是上游
 * `options.GroupGroupRatio`、保存后强制回读、含撤销或改价的草稿预览前不许保存。
 * 弹窗与这一页共用那一份，闸门不可能松紧不一。
 */
export function QyAdminUserGroups() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  // 这个组件有两个入口：`/system-settings/billing/group-matrix`（超管专属），以及
  // 旧路由 `/qy/admin/group-matrix` —— 后者**专门**留给普通管理员（role=10），
  // 因为本页的后端一直是 `AdminAuth`。空态里的指路必须跟着分叉：把够不着
  // `/system-settings` 的人指过去，他拿到的是一个没有解释的 403，而空态是这一页
  // 在这个状态下唯一的指引。
  const canOpenGroupPricing =
    useAuthStore((state) => state.auth.user?.role) === ROLE.SUPER_ADMIN

  const [scopeTarget, setScopeTarget] = useState<string | null>(null)
  // 范围抽屉只在**保存成功之后**关掉：提交失败时表单必须还在，否则运营填的
  // 模式与备注连同错误一起消失，而他并不知道刚才那次到底写进去没有。
  const editor = useQyGmEditor({ onScopeSaved: () => setScopeTarget(null) })
  const data = editor.data
  const orphansQuery = useQuery(qyGmOrphansQuery())

  const [pendingRepair, setPendingRepair] = useState<{
    tokenId: number
    tokenName: string
  } | null>(null)

  /** 左列表的搜索词与选中项。选中项的权威解析在 `lib/rows.ts`。 */
  const [search, setSearch] = useState('')
  const [selectedDraft, setSelectedDraft] = useState<string | null>(null)

  const userGroups = editor.userGroups
  const filteredGroups = useMemo(
    () => qyUgFilterGroups(userGroups, search),
    [userGroups, search]
  )
  /**
   * 真正选中的分组名。
   *
   * **不用 `useEffect` 把它同步进 state**：那样会先渲染一帧指向已消失分组的
   * 详情，再被纠正。这里让 state 只记「运营点过谁」，展示值每次从服务端清单
   * 现算 —— 另一个管理员在「模型分组定价」里删掉一档时，这一页下一次回读就
   * 自动落回第一档，不会短暂地渲染一个不存在的分组。
   */
  const selected = qyUgResolveSelected(userGroups, selectedDraft)
  const selectedGroup =
    userGroups.find((group) => group.name === selected) ?? null

  const repairMutation = useMutation({
    mutationFn: (tokenId: number) => qyGmRepairToken(tokenId),
    onSuccess: async () => {
      toast.success(t('qy_group_matrix_orphan_repaired'))
      setPendingRepair(null)
      await queryClient.invalidateQueries({
        queryKey: qyKeys.adminGroupMatrix(),
      })
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const scopeRow =
    data?.user_groups.find((row) => row.name === scopeTarget) ?? null

  return (
    <>
      <QyPageBoundary
        query={editor.query}
        isEmpty={data != null && data.user_groups.length === 0}
        emptyIcon={Users}
        emptyTitle={t('qy_group_matrix_row_header')}
        // `..._hint` 以冒号结尾，它是给后面那个 <Link> 用的（见 `user-group-list`
        // 与 `axis-legend`）。空态这里收不下链接（`emptyDescription` 只吃字符串），
        // 直接复用会渲染出一个悬空的冒号、且不说去哪儿建分组，所以另起一句自带路径。
        emptyDescription={
          canOpenGroupPricing
            ? t('qy_group_matrix_axis_create_empty')
            : t('qy_group_matrix_axis_create_empty_no_access')
        }
      >
        {data != null && (
          <div className='space-y-4'>
            {/*
              两个轴的图例 + 「哪些用户分组未设定范围」的常驻状态。

              放在横幅**之前**：横幅回答「下面这些配置现在算不算数」，而图例回答
              「用户分组与模型分组分别是什么」。分不清两者的人，读横幅也没用。
            */}
            <QyGmAxisLegend unscopedGroups={editor.unscopedGroups} />

            <QyGmStatusBanners
              snapshot={data.snapshot}
              partial={editor.partial}
              ratioDrift={editor.ratioDrift}
              selfExcluded={editor.selfExcluded}
              caseNearMiss={editor.caseNearMiss}
              warnings={data.warnings}
              shadowWriteDenies={data.shadow_write_denies}
              emptyScopeGroups={editor.emptyScopeGroups}
              onReload={editor.reload}
            />

            {/*
              差异条在**标签之外**，因为草稿是跨标签共享的一份：在主从式里改了
              两格再切到全站矩阵，未保存的仍是同一批动作。差异条跟着标签走会让
              它在切换的瞬间消失，而它正是「你还有东西没保存」的唯一常驻信号。
            */}
            <QyGmDiffBar
              counts={editor.counts}
              invalidCount={editor.invalidCells.length}
              hasDraft={editor.draft.size > 0}
              needsPreview={editor.needsPreview}
              isPreviewing={editor.isPreviewing}
              isSaving={editor.isSaving}
              onPreview={editor.runPreview}
              onSave={editor.runSave}
              onReset={editor.resetDraft}
            />

            <Tabs defaultValue='groups'>
              <TabsList>
                <TabsTrigger value='groups'>
                  {t('qy_group_matrix_row_header')}
                </TabsTrigger>
                <TabsTrigger value='matrix'>
                  {t('qy_group_matrix_tab_matrix')}
                </TabsTrigger>
                <TabsTrigger value='orphans'>
                  {t('qy_group_matrix_tab_orphans')}
                </TabsTrigger>
              </TabsList>

              {/*
                主视图：左列表 = 用户分组，右列表 = 该档的模型分组分配 + 倍率。
                窄屏塌成一列（列表在上、详情在下），两个面板各自内部滚动。
              */}
              <TabsContent value='groups'>
                {/*
                  栅格写在**内层 div** 上，不写在面板本身：面板在未选中时靠
                  `hidden` 属性隐藏，而任何显式的 `display` 都会盖掉 `hidden`
                  的 UA 样式 —— 三张标签会同时铺在页面上，而这种坏法只在
                  `keepMounted` 生效的那条路径上出现，本地随手点两下未必撞得到。
                */}
                <div className='grid min-w-0 gap-3 lg:grid-cols-[minmax(0,18rem)_minmax(0,1fr)] lg:items-start'>
                  <QyUgUserGroupList
                    groups={filteredGroups}
                    totalCount={userGroups.length}
                    selected={selected}
                    onSelect={setSelectedDraft}
                    search={search}
                    onSearchChange={setSearch}
                    grantedCounts={editor.grantedCounts}
                    dirtyGroups={editor.dirtyGroups}
                  />
                  {selectedGroup != null && (
                    <QyUgGroupDetail
                      data={data}
                      userGroup={selectedGroup}
                      serverCells={editor.serverCells}
                      draft={editor.draft}
                      grantedCount={
                        editor.grantedCounts.get(selectedGroup.name) ?? 0
                      }
                      onToggleGranted={(modelGroup, granted) =>
                        editor.toggleGranted(
                          selectedGroup.name,
                          modelGroup,
                          granted
                        )
                      }
                      onRatioChange={(modelGroup, ratio) =>
                        editor.changeRatio(
                          selectedGroup.name,
                          modelGroup,
                          ratio
                        )
                      }
                      onEditScope={() => setScopeTarget(selectedGroup.name)}
                      onCopyFrom={(fromUserGroup) =>
                        editor.copyRow(fromUserGroup, selectedGroup.name)
                      }
                      /*
                        范围写入的唯一通道，两个外壳共用 —— 那道「这次写入会
                        清空草稿」的闸门因此不可能松紧不一。这一页额外保留右上角
                        那枚「设定模型分组范围」按钮（`onEditScope`）：它是高级页
                        上直接改 `managed` / `allow_auto` 的逃生口，日常路径
                        （建清单、清空）已经收进详情面板自己。
                      */
                      scope={{
                        isSaving: editor.isScopeSaving,
                        hasUnsavedDraft: editor.counts.total > 0,
                        onSubmit: (body, afterApply) =>
                          editor.submitScope(
                            selectedGroup.name,
                            body,
                            afterApply
                          ),
                      }}
                    />
                  )}
                </div>
              </TabsContent>

              <TabsContent value='matrix' className='space-y-3'>
                <p className='text-muted-foreground text-sm'>
                  {t('qy_group_scope_matrix_desc')}
                </p>
                <QyGmMatrixGrid
                  data={data}
                  serverCells={editor.serverCells}
                  draft={editor.draft}
                  onToggleGranted={editor.toggleGranted}
                  onRatioChange={editor.changeRatio}
                  onColumnBulk={editor.columnBulk}
                  onCopyRow={editor.copyRow}
                  onEditScope={setScopeTarget}
                />
              </TabsContent>

              <TabsContent value='orphans'>
                <QyPageBoundary query={orphansQuery}>
                  {orphansQuery.data != null && (
                    <QyGmOrphansPanel
                      data={orphansQuery.data}
                      repairingTokenId={
                        repairMutation.isPending
                          ? (pendingRepair?.tokenId ?? null)
                          : null
                      }
                      onRepairToken={(tokenId, tokenName) =>
                        setPendingRepair({ tokenId, tokenName })
                      }
                    />
                  )}
                </QyPageBoundary>
              </TabsContent>
            </Tabs>
          </div>
        )}
      </QyPageBoundary>

      <QyGmPreviewDialog
        open={editor.previewOpen}
        onOpenChange={editor.setPreviewOpen}
        preview={editor.preview}
        isLoading={editor.isPreviewing}
      />

      <QyGmScopeSheet
        open={scopeTarget != null}
        onOpenChange={(open) => {
          if (!open) setScopeTarget(null)
        }}
        userGroup={scopeRow}
        grantedCount={
          scopeRow == null ? 0 : (editor.grantedCounts.get(scopeRow.name) ?? 0)
        }
        hasUnsavedDraft={editor.counts.total > 0}
        enforcePreview={
          scopeRow != null && editor.enforcePreview?.userGroup === scopeRow.name
            ? editor.enforcePreview.result
            : null
        }
        isPreviewing={editor.isEnforcePreviewing}
        onPreviewForEnforce={() => {
          if (scopeRow == null) return
          editor.runEnforcePreview(scopeRow.name)
        }}
        isSaving={editor.isScopeSaving}
        onSubmit={(body) => {
          if (scopeRow == null) return
          editor.submitScope(scopeRow.name, body)
        }}
      />

      <QyConfirmDialog
        open={pendingRepair != null}
        onOpenChange={(open) => {
          if (!open) setPendingRepair(null)
        }}
        title={t('qy_group_matrix_orphan_repair')}
        description={t('qy_group_matrix_orphan_repair_confirm', {
          token: pendingRepair?.tokenName ?? '',
        })}
        isLoading={repairMutation.isPending}
        onConfirm={() => {
          if (pendingRepair == null) return
          repairMutation.mutate(pendingRepair.tokenId)
        }}
      />
    </>
  )
}
