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
import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { QyConfirmDialog } from '../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { qyKeys } from '../../lib/query-keys'
import {
  qyGmMatrixQuery,
  qyGmOrphansQuery,
  qyGmPreview,
  qyGmPreviewForEnforce,
  qyGmRepairToken,
  qyGmSaveMatrix,
  qyGmSaveScope,
} from '../admin-group-matrix/api'
import { QyGmAxisLegend } from '../admin-group-matrix/components/axis-legend'
import { QyGmDiffBar } from '../admin-group-matrix/components/diff-bar'
import {
  QyGmMatrixGrid,
  type QyGmColumnBulk,
} from '../admin-group-matrix/components/matrix-grid'
import { QyGmOrphansPanel } from '../admin-group-matrix/components/orphans-panel'
import { QyGmPreviewDialog } from '../admin-group-matrix/components/preview-dialog'
import { QyGmScopeSheet } from '../admin-group-matrix/components/scope-sheet'
import { QyGmStatusBanners } from '../admin-group-matrix/components/status-banners'
import {
  qyGmBuildChanges,
  qyGmCaseNearMisses,
  qyGmCellKey,
  qyGmCellKeyUserGroup,
  qyGmCountChanges,
  qyGmDraftFingerprint,
  qyGmGrantedOf,
  qyGmHasRevoke,
  qyGmIndexCells,
  qyGmInvalidCells,
  type QyGmDraftEntry,
  type QyGmRatioDraft,
} from '../admin-group-matrix/lib/draft'
import type {
  QyGmPreviewResponse,
  QyGmSavePartial,
  QyGmSaveResponse,
  QyGmScopeRequest,
  QyGmUserGroup,
} from '../admin-group-matrix/types'
import { qyOpsErrorMessage } from '../ops/errors'
import { QyUgGroupDetail } from './components/user-group-detail'
import { QyUgUserGroupList } from './components/user-group-list'
import {
  qyUgFilterGroups,
  qyUgGrantedCount,
  qyUgResolveSelected,
} from './lib/rows'

/**
 * 「用户分组」页（系统设置 → 计费与支付 → 用户分组）。
 *
 * 项目方原话：「用户分组写一个单独页面，这个页面你可以画 2 个列表框，新增用户
 * 分组，点击分组后进行模型分组单独分配（调整单独用户分组倍率）」。
 *
 * ── 与原「用户分组 × 模型分组」矩阵页的关系：搬家，不是并存 ──
 *
 * 这一页与那一页读写的是**同一组三份数据**（`qy_group_scopes`、`qy_group_grants`、
 * 上游 `options.GroupGroupRatio`），经**同一组三个端点**。两个页面各自持有一份
 * 草稿、各自握着那道「保存前必须看过影响面」的闸门，而写入本身是**两库不原子**
 * 的：一个屏幕上显示绿色「已保存」、另一个屏幕显示线上真实的半成状态，是这套
 * 设计最坏的失败方式。所以矩阵页整体搬到这里，`/qy/admin/group-matrix` 只保留
 * 重定向，系统设置抽屉里那一行入口下线 —— 全站只剩这一个入口。
 *
 * 反过来「新页面做主、矩阵页整体下线」被否掉，理由是能力损失：整列批量、跨档
 * 对比「哪几档人能到达某个模型分组」、以及孤儿令牌基线，这三件事在主从式里
 * 结构性地表达不出来（主从式一次只看一档）。所以它们作为**次要视图**保留成
 * 标签，主视图是项目方要的两个列表框。
 *
 * ── 三份状态的分工（与矩阵页逐字相同，不因换了皮就放松）──
 *  1. 倍率的唯一真相源仍是上游 `options.GroupGroupRatio`，扩展库里一个倍率
 *     字节都不存；
 *  2. 保存后**强制回读**服务端真实状态，前端不做乐观渲染；
 *  3. 含撤销或改价的草稿，在看过影响面之前保存键是禁用的。
 */
/**
 * 取数未回来时的清单占位。
 *
 * **必须是模块级常量**，不能在组件里写 `data?.user_groups ?? []`：后者每次
 * 渲染都是一个新数组，下游 `useMemo` 的依赖恒变，筛选与计数在每一次按键上
 * 全量重算。这一页的每一格都是受控输入框，重算发生在输入的那条路径上。
 */
const QY_UG_NO_GROUPS: readonly QyGmUserGroup[] = []

export function QyAdminUserGroups() {
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
   * 与上面那份通用预览分开存：两者取值范围不同（全部已设定范围的分组 vs 一个
   * 分组），混用会让回传给 scope 端点的 `impact_hash` 与服务端重算的永远对不上。
   */
  const [enforcePreview, setEnforcePreview] = useState<{
    userGroup: string
    result: QyGmPreviewResponse
  } | null>(null)

  /** 左列表的搜索词与选中项。选中项的权威解析在 `lib/rows.ts`。 */
  const [search, setSearch] = useState('')
  const [selectedDraft, setSelectedDraft] = useState<string | null>(null)

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

  const userGroups = data?.user_groups ?? QY_UG_NO_GROUPS
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

  /** 分组名 → 草稿合并后在范围内的模型分组数。左列表徽章与右侧共用。 */
  const grantedCounts = useMemo(() => {
    const map = new Map<string, number>()
    if (data == null) return map
    for (const group of data.user_groups) {
      map.set(
        group.name,
        qyUgGrantedCount(data, serverCells, draft, group.name)
      )
    }
    return map
  }, [data, serverCells, draft])

  /**
   * 本次草稿动过哪几档。
   *
   * 判据取**草稿键**而不是产出的动作列表：一格里粘进了 `1e3` 这种非法值时
   * 不会产出任何动作，但它确实拦住了保存。只按动作列表标记，运营会看到
   * 「保存被禁用」却在左列表上找不到任何一档带标记 —— 而全站唯一能告诉他
   * 「问题在哪一档」的地方就是这个标记。
   */
  const dirtyGroups = useMemo(() => {
    const names = new Set<string>()
    for (const key of draft.keys()) names.add(qyGmCellKeyUserGroup(key))
    return names
  }, [draft])

  const caseNearMiss = useMemo(
    () =>
      data == null
        ? []
        : qyGmCaseNearMisses(data.user_groups, data.model_groups),
    [data]
  )
  /**
   * 范围里不含用户分组自己的那些行。
   *
   * 只对**已设定范围**的行判定：未设定范围的行走上游原行为，而上游在差分算完之后
   * **无条件**把 userGroup 自己补回去，所以那些行永远含自己，标出来是假警报。
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

  /**
   * **未设定范围**的用户分组 —— 图例里那条常驻说明的数据源。
   *
   * 它不是待办，是一条长期条件：这些分组逐位沿用上游原行为。这是本轮拍定的
   * 正确默认，但它必须**看得见** —— 运营在「模型分组定价」里新加一个分组时，
   * 那一档立刻就落进这个状态，而那一页不会提到这回事。
   */
  const unscopedGroups = useMemo(
    () =>
      (data?.user_groups ?? [])
        .filter((row) => row.scope_state === 'unset')
        .map((row) => row.name),
    [data?.user_groups]
  )
  /**
   * **设了范围、却一个模型分组都没勾**的用户分组 —— 真正的待办。
   *
   * 判据只用服务端状态，不掺本地草稿：草稿里勾了几个格子但还没保存时，线上那一档
   * 的人仍然一个都选不了。跟着草稿走会让提示在保存之前就消失，而那正是最需要它
   * 提醒「你还没按保存」的时刻。
   */
  const emptyScopeGroups = useMemo(
    () =>
      (data?.user_groups ?? [])
        .filter((row) => row.scope_state === 'empty')
        .map((row) => row.name),
    [data?.user_groups]
  )

  const previewMatchesDraft =
    previewedFingerprint != null && previewedFingerprint === fingerprint
  /**
   * 保存闸门。
   *
   * **撤销与改价都要闸**：撤销让一批令牌当场 403，改价则在保存成功的那一秒
   * 就开始按新价扣钱 —— 而本轮的整个立论是「倍率从偶尔配的例外提升为主要机制」。
   * 配合整列批量与整档复制，一次点击可以改掉一整批倍率，只看撤销的闸门会让
   * 这种改动完全没有影响面可看。
   * 放开（grant）不闸：它不会让任何一个正在跑的请求变成 403，也不会改价。
   */
  const needsPreview =
    (qyGmHasRevoke(changes) || counts.reprice > 0) && !previewMatchesDraft
  // 打开这一页时手上的倍率哈希 vs 服务端最新的。对不上 = 上游「模型分组定价」
  // 页在这期间改过同一份数据。那个入口刻意不锁死（扩展关掉之后运营必须还能在
  // 原地改倍率），所以只能检测并要求重新载入。
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
   * 整列批量（全站矩阵视图专有）。
   *
   * **只作用于已设定范围的行** —— 未设定范围的行没有权威清单，往它头上写 grant
   * 只会生成一批后端必然拒绝的动作，而运营从界面上看不出来为什么保存失败。
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

  /** 从另一个用户分组整档复制（可选性 + 倍率一起）。 */
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
   * **不能复用上面那一份**：通用预览不带 `user_groups`，服务端会铺开全部已设定
   * 范围的分组；而切换时服务端用 `previewDigest(userGroup)` 只重算那一个分组。
   * 两个 `impact_hash` 因此永远不相等，enforce 会被 409 永久锁死 ——
   * 而灰度的推荐顺序（先全部设范围成 shadow、再逐个切 enforce）恰好保证了
   * 站里同时有两个以上已设定范围的分组。
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

  return (
    <>
      <QyPageBoundary
        query={query}
        isEmpty={data != null && data.user_groups.length === 0}
        emptyIcon={Users}
        emptyTitle={t('qy_group_matrix_row_header')}
        emptyDescription={t('qy_group_matrix_axis_create_hint')}
      >
        {data != null && (
          <div className='space-y-4'>
            {/*
              两个轴的图例 + 「哪些用户分组未设定范围」的常驻状态。

              放在横幅**之前**：横幅回答「下面这些配置现在算不算数」，而图例回答
              「用户分组与模型分组分别是什么」。分不清两者的人，读横幅也没用。
            */}
            <QyGmAxisLegend unscopedGroups={unscopedGroups} />

            <QyGmStatusBanners
              snapshot={data.snapshot}
              partial={partial}
              ratioDrift={ratioDrift}
              selfExcluded={selfExcluded}
              caseNearMiss={caseNearMiss}
              warnings={data.warnings}
              shadowWriteDenies={data.shadow_write_denies}
              emptyScopeGroups={emptyScopeGroups}
              onReload={() => {
                setDraft(new Map())
                setPreview(null)
                setPreviewedFingerprint(null)
                void query.refetch()
              }}
            />

            {/*
              差异条在**标签之外**，因为草稿是跨标签共享的一份：在主从式里改了
              两格再切到全站矩阵，未保存的仍是同一批动作。差异条跟着标签走会让
              它在切换的瞬间消失，而它正是「你还有东西没保存」的唯一常驻信号。
            */}
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
                    grantedCounts={grantedCounts}
                    dirtyGroups={dirtyGroups}
                  />
                  {selectedGroup != null && (
                    <QyUgGroupDetail
                      data={data}
                      userGroup={selectedGroup}
                      serverCells={serverCells}
                      draft={draft}
                      grantedCount={grantedCounts.get(selectedGroup.name) ?? 0}
                      onToggleGranted={(modelGroup, granted) =>
                        handleToggleGranted(
                          selectedGroup.name,
                          modelGroup,
                          granted
                        )
                      }
                      onRatioChange={(modelGroup, ratio) =>
                        handleRatioChange(selectedGroup.name, modelGroup, ratio)
                      }
                      onEditScope={() => setScopeTarget(selectedGroup.name)}
                      onCopyFrom={(fromUserGroup) =>
                        handleCopyRow(fromUserGroup, selectedGroup.name)
                      }
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
        grantedCount={
          scopeRow == null ? 0 : (grantedCounts.get(scopeRow.name) ?? 0)
        }
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
    </>
  )
}
