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
import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { QyConfirmDialog } from '../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { qyKeys } from '../../lib/query-keys'
import { qyOpsErrorMessage } from '../ops/errors'
import {
  qyGmMatrixQuery,
  qyGmOrphansQuery,
  qyGmPreview,
  qyGmPreviewForEnforce,
  qyGmRepairToken,
  qyGmSaveMatrix,
  qyGmSaveScope,
} from './api'
import { QyGmDiffBar } from './components/diff-bar'
import { QyGmMatrixGrid, type QyGmColumnBulk } from './components/matrix-grid'
import { QyGmOrphansPanel } from './components/orphans-panel'
import { QyGmPreviewDialog } from './components/preview-dialog'
import { QyGmScopeSheet } from './components/scope-sheet'
import { QyGmStatusBanners } from './components/status-banners'
import {
  qyGmBuildChanges,
  qyGmCaseNearMisses,
  qyGmCellKey,
  qyGmCountChanges,
  qyGmDraftFingerprint,
  qyGmGrantedOf,
  qyGmHasRevoke,
  qyGmIndexCells,
  qyGmInvalidCells,
  type QyGmDraftEntry,
  type QyGmRatioDraft,
} from './lib/draft'
import type {
  QyGmPreviewResponse,
  QyGmSavePartial,
  QyGmSaveResponse,
  QyGmScopeRequest,
} from './types'

/**
 * 用户分组 × 模型分组 矩阵（管理端）。
 *
 * ── 这一页在回答什么 ──
 * 项目方原话：「用户分组：有不同的分组倍率，然后只能选择这个用户分组拥有的
 * 模型分组。当前似乎都是大杂烩，只要可见，就全部都可以选择。」
 *
 * 行 = 用户分组（一个人属于哪一档），列 = 模型分组（一次请求走哪批渠道），
 * 格 = 该组合的倍率，空 = 这一档的人根本选不了那批渠道。
 *
 * ── 两处不能省的克制 ──
 *
 *  1. **倍率的唯一真相源仍然是上游 `options.GroupGroupRatio`。** 这一页只是
 *     它的编辑界面，扩展库里一个倍率字节都不存。存一份镜像就必须有同步机制，
 *     而同步失败的表现是「管理端显示 A、热路径乘 B」—— 与本轮正在修的那个
 *     缺陷完全同形。
 *  2. **保存后强制回读服务端真实状态**，前端不做乐观渲染。倍率落上游 options、
 *     清单落扩展库，两库不原子；部分失败时运营必须立刻看到「倍率已生效、
 *     清单未生效」，而乐观渲染画出来的是一个从未存在过的成功画面。
 */
export function QyAdminGroupMatrix() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const query = useQuery(qyGmMatrixQuery())
  const orphansQuery = useQuery(qyGmOrphansQuery())
  const data = query.data

  const [draft, setDraft] = useState<Map<string, QyGmDraftEntry>>(new Map())
  const [preview, setPreview] = useState<QyGmPreviewResponse | null>(null)
  /**
   * 预览时那一份草稿的本地指纹。
   *
   * 保存前拿它和当前草稿比：不相等 = 运营预览之后又动了格子，闸门必须重新
   * 锁上。只比后端返回的 `draft_hash` 是不够的 —— 那要发一次请求才知道，
   * 而运营会先看到保存按钮亮着然后吃一个 409。
   */
  const [previewedFingerprint, setPreviewedFingerprint] = useState<
    string | null
  >(null)
  const [previewOpen, setPreviewOpen] = useState(false)
  const [scopeTarget, setScopeTarget] = useState<string | null>(null)
  const [pendingRepair, setPendingRepair] = useState<{
    tokenId: number
    tokenName: string
  } | null>(null)
  /** 上一次保存的半成状态。非空时页面顶部常驻一条横幅，直到下一次成功保存。 */
  const [partial, setPartial] = useState<QyGmSavePartial | null>(null)
  /**
   * 切 `enforce` 用的那一份**单分组**预览。
   *
   * 与上面那份通用预览分开存：两者取值范围不同（全部已接管分组 vs 一个分组），
   * 混用会让回传给 scope 端点的 `impact_hash` 与服务端重算的永远对不上。
   */
  const [enforcePreview, setEnforcePreview] = useState<{
    userGroup: string
    result: QyGmPreviewResponse
  } | null>(null)

  const serverCells = useMemo(
    () => qyGmIndexCells(data?.cells ?? []),
    [data?.cells]
  )
  const changes = useMemo(
    () => qyGmBuildChanges(draft, serverCells),
    [draft, serverCells]
  )
  const counts = useMemo(() => qyGmCountChanges(changes), [changes])
  const invalidCells = useMemo(() => qyGmInvalidCells(draft), [draft])
  const fingerprint = useMemo(() => qyGmDraftFingerprint(changes), [changes])

  const caseNearMiss = useMemo(
    () =>
      data == null
        ? []
        : qyGmCaseNearMisses(data.user_groups, data.model_groups),
    [data]
  )
  /**
   * 权威清单里不含用户分组自己的那些行。
   *
   * 只对已接管的行判定：未接管的行走上游原行为，而上游在差分算完之后**无条件**
   * 把 userGroup 自己补回去，所以那些行永远含自己，标出来是假警报。
   */
  const selfExcluded = useMemo(() => {
    if (data == null) return []
    return data.user_groups
      .filter((row) => row.managed)
      .filter((row) => {
        const key = qyGmCellKey(row.name, row.name)
        const hasColumn = data.model_groups.some(
          (column) => column.name === row.name
        )
        if (!hasColumn) return false
        return !qyGmGrantedOf(serverCells.get(key), draft.get(key))
      })
      .map((row) => row.name)
  }, [data, draft, serverCells])

  const previewMatchesDraft =
    previewedFingerprint != null && previewedFingerprint === fingerprint
  /**
   * 保存闸门。
   *
   * **撤销与改价都要闸**：撤销让一批令牌当场 403，改价则在保存成功的那一秒
   * 就开始按新价扣钱 —— 而本轮的整个立论是「倍率从偶尔配的例外提升为主要机制」。
   * 配合整列批量与整行复制，一次点击可以改掉一整列/一整行的倍率，
   * 只看撤销的闸门会让这种改动完全没有影响面可看。
   * 放开（grant）不闸：它不会让任何一个正在跑的请求变成 403，也不会改价。
   */
  const needsPreview =
    (qyGmHasRevoke(changes) || counts.reprice > 0) && !previewMatchesDraft
  // 打开这一页时手上的倍率哈希 vs 服务端最新的。对不上 = 上游「系统设置 →
  // 分组倍率」页在这期间改过同一份数据。那个入口刻意不锁死（扩展关掉之后
  // 运营必须还能在原地改倍率），所以只能检测并要求重新载入。
  const ratioDrift =
    preview != null &&
    data != null &&
    preview.base_ratio_hash !== data.base_ratio_hash

  const patchDraft = useCallback((key: string, patch: QyGmDraftEntry) => {
    setDraft((current) => {
      const next = new Map(current)
      next.set(key, { ...next.get(key), ...patch })
      return next
    })
    // 草稿一变，之前那份预览就不再描述将要保存的东西。指纹比对已经能拦住
    // 保存，但把预览面板一起清掉可以避免运营对着一份过期报告做决定。
    setPreviewedFingerprint(null)
  }, [])

  const handleToggleGranted = useCallback(
    (userGroup: string, modelGroup: string, granted: boolean) => {
      patchDraft(qyGmCellKey(userGroup, modelGroup), { granted })
    },
    [patchDraft]
  )

  const handleRatioChange = useCallback(
    (userGroup: string, modelGroup: string, ratio: QyGmRatioDraft) => {
      patchDraft(qyGmCellKey(userGroup, modelGroup), { ratio })
    },
    [patchDraft]
  )

  /**
   * 整列批量。
   *
   * **只作用于已接管的行** —— 未接管的行没有权威清单，往它头上写 grant 只会
   * 生成一批后端必然拒绝的动作，而运营从界面上看不出来为什么保存失败。
   */
  const handleColumnBulk = useCallback(
    (modelGroup: string, action: QyGmColumnBulk) => {
      if (data == null) return
      setDraft((current) => {
        const next = new Map(current)
        for (const row of data.user_groups) {
          if (!row.managed) continue
          const key = qyGmCellKey(row.name, modelGroup)
          const entry = { ...next.get(key) }
          if (action === 'select_all') entry.granted = true
          else if (action === 'clear') entry.granted = false
          else entry.ratio = { kind: 'inherit' }
          next.set(key, entry)
        }
        return next
      })
      setPreviewedFingerprint(null)
    },
    [data]
  )

  /** 从另一个用户分组整行复制（可选性 + 倍率一起）。 */
  const handleCopyRow = useCallback(
    (fromUserGroup: string, toUserGroup: string) => {
      if (data == null) return
      setDraft((current) => {
        const next = new Map(current)
        for (const column of data.model_groups) {
          const fromKey = qyGmCellKey(fromUserGroup, column.name)
          const source = serverCells.get(fromKey)
          const entry: QyGmDraftEntry = {
            granted: qyGmGrantedOf(source, current.get(fromKey)),
          }
          const sourceRatio =
            source == null || source.source === 'inherit' ? null : source.ratio
          entry.ratio =
            sourceRatio == null
              ? { kind: 'inherit' }
              : { kind: 'set', raw: String(sourceRatio) }
          next.set(qyGmCellKey(toUserGroup, column.name), entry)
        }
        return next
      })
      setPreviewedFingerprint(null)
    },
    [data, serverCells]
  )

  /**
   * 用服务端回读的真实状态替换本地一切。
   *
   * **不做乐观合并**：倍率落上游 options、清单落扩展库，两库不原子。把请求的
   * 回声当成新状态渲染，部分失败时画出来的是一个从未存在过的成功画面。
   */
  const applyServerState = useCallback(
    (fresh: QyGmSaveResponse) => {
      queryClient.setQueryData(qyKeys.adminGroupMatrixData(), fresh)
      setDraft(new Map())
      setPreview(null)
      setPreviewedFingerprint(null)
      setEnforcePreview(null)
      setPartial(fresh.partial ?? null)
    },
    [queryClient]
  )

  const previewMutation = useMutation({
    mutationFn: () => qyGmPreview(changes),
    onSuccess: (result) => {
      setPreview(result)
      setPreviewedFingerprint(fingerprint)
      setPreviewOpen(true)
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  /**
   * 切 `enforce` 专用的预览。
   *
   * **不能复用上面那一份**：通用预览不带 `user_groups`，服务端会铺开全部已接管
   * 分组；而切换时服务端用 `previewDigest(userGroup)` 只重算那一个分组。
   * 两个 `impact_hash` 因此永远不相等，enforce 会被 409 永久锁死 ——
   * 而灰度的推荐顺序（先全部接管成 shadow、再逐个切 enforce）恰好保证了
   * 站里同时有两个以上被接管的分组。
   */
  const enforcePreviewMutation = useMutation({
    mutationFn: (userGroup: string) => qyGmPreviewForEnforce(userGroup),
    onSuccess: (result, userGroup) => {
      setEnforcePreview({ userGroup, result })
      setPreview(result)
      setPreviewOpen(true)
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const saveMutation = useMutation({
    mutationFn: () =>
      qyGmSaveMatrix({
        cells: changes,
        draft_hash: preview?.draft_hash,
        impact_hash: preview?.impact_hash,
        base_ratio_hash: data?.base_ratio_hash ?? '',
      }),
    onSuccess: (fresh) => {
      // 部分失败绝不能报成功：那正是这套两库设计最坏的失败方式 —— 运营看到
      // 一句绿色的「已保存」然后走人，而线上是半成状态。
      if (fresh.partial == null) toast.success(t('qy_group_matrix_saved'))
      else toast.error(fresh.partial.message)
      applyServerState(fresh)
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const scopeMutation = useMutation({
    mutationFn: (input: { userGroup: string; body: QyGmScopeRequest }) =>
      qyGmSaveScope(input.userGroup, input.body),
    onSuccess: (fresh) => {
      toast.success(t('qy_group_matrix_scope_saved'))
      setScopeTarget(null)
      applyServerState(fresh)
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

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
  const scopeGrantedCount =
    data == null || scopeRow == null
      ? 0
      : data.model_groups.filter((column) => {
          const key = qyGmCellKey(scopeRow.name, column.name)
          return qyGmGrantedOf(serverCells.get(key), draft.get(key))
        }).length

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_group_matrix_title')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <QyPageBoundary query={query}>
          {data != null && (
            <div className='space-y-4'>
              <p className='text-muted-foreground text-sm'>
                {t('qy_group_matrix_desc')}
              </p>

              <QyGmStatusBanners
                snapshot={data.snapshot}
                partial={partial}
                ratioDrift={ratioDrift}
                selfExcluded={selfExcluded}
                caseNearMiss={caseNearMiss}
                warnings={data.warnings}
                shadowWriteDenies={data.shadow_write_denies}
                onReload={() => {
                  setDraft(new Map())
                  setPreview(null)
                  setPreviewedFingerprint(null)
                  void query.refetch()
                }}
              />

              <Tabs defaultValue='matrix'>
                <TabsList>
                  <TabsTrigger value='matrix'>
                    {t('qy_group_matrix_tab_matrix')}
                  </TabsTrigger>
                  <TabsTrigger value='orphans'>
                    {t('qy_group_matrix_tab_orphans')}
                  </TabsTrigger>
                </TabsList>

                <TabsContent value='matrix' className='space-y-3'>
                  <QyGmDiffBar
                    counts={counts}
                    invalidCount={invalidCells.length}
                    hasDraft={draft.size > 0}
                    needsPreview={needsPreview}
                    isPreviewing={previewMutation.isPending}
                    isSaving={saveMutation.isPending}
                    onPreview={() => previewMutation.mutate()}
                    onSave={() => saveMutation.mutate()}
                    onReset={() => {
                      setDraft(new Map())
                      setPreviewedFingerprint(null)
                    }}
                  />

                  <QyGmMatrixGrid
                    data={data}
                    serverCells={serverCells}
                    draft={draft}
                    onToggleGranted={handleToggleGranted}
                    onRatioChange={handleRatioChange}
                    onColumnBulk={handleColumnBulk}
                    onCopyRow={handleCopyRow}
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
      </QySectionPageLayout.Content>

      <QyGmPreviewDialog
        open={previewOpen}
        onOpenChange={setPreviewOpen}
        preview={preview}
        isLoading={previewMutation.isPending}
      />

      <QyGmScopeSheet
        open={scopeTarget != null}
        onOpenChange={(open) => {
          if (!open) setScopeTarget(null)
        }}
        userGroup={scopeRow}
        grantedCount={scopeGrantedCount}
        hasUnsavedDraft={counts.total > 0}
        enforcePreview={
          scopeRow != null && enforcePreview?.userGroup === scopeRow.name
            ? enforcePreview.result
            : null
        }
        isPreviewing={enforcePreviewMutation.isPending}
        onPreviewForEnforce={() => {
          if (scopeRow == null) return
          enforcePreviewMutation.mutate(scopeRow.name)
        }}
        isSaving={scopeMutation.isPending}
        onSubmit={(body) => {
          if (scopeRow == null) return
          scopeMutation.mutate({
            userGroup: scopeRow.name,
            body:
              body.mode === 'enforce'
                ? {
                    ...body,
                    draft_hash: enforcePreview?.result.draft_hash,
                    impact_hash: enforcePreview?.result.impact_hash,
                  }
                : body,
          })
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
    </QySectionPageLayout>
  )
}
