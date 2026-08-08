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

import { qyKeys } from '../../../lib/query-keys'
import { qyUgGrantedCount } from '../../admin-user-groups/lib/rows'
import { qyOpsErrorMessage } from '../../ops/errors'
import {
  qyGmMatrixQuery,
  qyGmPreview,
  qyGmPreviewForEnforce,
  qyGmSaveMatrix,
  qyGmSaveScope,
} from '../api'
import type {
  QyGmPreviewResponse,
  QyGmSavePartial,
  QyGmSaveResponse,
  QyGmScopeRequest,
  QyGmUserGroup,
} from '../types'
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
  qyGmNoteDraftOf,
  type QyGmColumnBulk,
  type QyGmDraftEntry,
  type QyGmRatioDraft,
} from './draft'

/**
 * 「用户分组 × 模型分组」的编辑状态机 —— **全站唯一一份**。
 *
 * ── 为什么它必须是一个 hook 而不是留在页面组件里 ──
 *
 * 这份状态机现在有两个外壳：整页的主从式视图（`pages/admin-user-groups`），
 * 以及需求 4 要的那个弹窗（`components/user-group-scope-dialog`，从「用户分组」
 * 表的行内直接打开，不再跳页）。两个外壳读写的是**同一组三份数据**
 * （`qy_group_scopes`、`qy_group_grants`、上游 `options.GroupGroupRatio`），
 * 经**同一组端点**，而写入本身是**两库不原子**的。
 *
 * 把闸门（"含撤销或改价的草稿，预览之前不许保存"）、指纹比对、以及"保存后强制
 * 回读、绝不乐观渲染"这三件事各写一份，最坏的失败方式不是难维护，而是**两个外壳
 * 的闸门不一样严** —— 弹窗那一份漏掉改价的闸，运营就能在弹窗里一键改掉一整档的
 * 价而完全没有影响面可看，而页面上同样的动作是被拦住的。所以这里一份实现，
 * 两个外壳只负责画。
 *
 * ── 三条不变量（与拆分之前逐字相同）──
 *  1. 倍率的唯一真相源是上游 `options.GroupGroupRatio`，扩展库里一个倍率字节都不存；
 *  2. 保存后**强制回读**服务端真实状态，前端不做乐观渲染；
 *  3. 含撤销或改价的草稿，在看过影响面之前保存键是禁用的。
 */

/** 取数未回来时的清单占位。见 {@link useQyGmEditor} 里的说明。 */
const QY_GM_NO_GROUPS: readonly QyGmUserGroup[] = []

export type QyGmEditor = ReturnType<typeof useQyGmEditor>

export type QyGmEditorOptions = {
  /**
   * 范围设置**保存成功之后**的回调。
   *
   * 只有整页那个外壳需要它（把独立的范围抽屉关掉）。刻意不在提交的那一刻关：
   * 提交失败时表单必须还在，否则运营填的模式与备注连同错误一起消失，而他并不
   * 知道刚才那次到底写进去没有。弹窗外壳把范围表单内嵌在自己里，保存成功后
   * 原地留着即可，因此不传。
   */
  onScopeSaved?: () => void
}

export function useQyGmEditor(options?: QyGmEditorOptions) {
  const onScopeSaved = options?.onScopeSaved
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const query = useQuery(qyGmMatrixQuery())
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

  /**
   * 取数未回来时的清单。
   *
   * **必须是模块级常量**，不能写 `data?.user_groups ?? []`：后者每次渲染都是
   * 一个新数组，下游 `useMemo` 的依赖恒变，筛选与计数在每一次按键上全量重算。
   * 这一页的每一格都是受控输入框，重算发生在输入的那条路径上。
   */
  const userGroups = data?.user_groups ?? QY_GM_NO_GROUPS

  /** 分组名 → 草稿合并后在范围内的模型分组数。列表徽章与详情共用同一个数。 */
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
   * 「保存被禁用」却在列表上找不到任何一档带标记 —— 而全站唯一能告诉他
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

  /**
   * 勾 / 取消勾一个模型分组。
   *
   * ── 取消勾选时**连同这一格的备注草稿一起清掉** ──
   *
   * 按格备注落的是 `qy_group_grants` 那一行的一列，撤销会把整行删掉 —— 备注跟着
   * 消失是这条数据的事实。不清的话草稿里会留下一条 `set_note`，而它指向一个保存
   * 之后不存在的格子：后端会以「这个模型分组不在可用清单里」400 掉整次保存
   * （连同同一次提交里的改价一起失败），而运营看着屏幕上那个已经取消勾选的行，
   * 完全想不到报错说的是它。
   */
  const toggleGranted = useCallback(
    (userGroup: string, modelGroup: string, granted: boolean) => {
      patchDraft(qyGmCellKey(userGroup, modelGroup), {
        granted,
        ...(granted ? {} : { note: undefined }),
      })
    },
    [patchDraft]
  )

  const changeRatio = useCallback(
    (userGroup: string, modelGroup: string, ratio: QyGmRatioDraft) => {
      patchDraft(qyGmCellKey(userGroup, modelGroup), { ratio })
    },
    [patchDraft]
  )

  /**
   * 按格备注。
   *
   * 与倍率走**同一份草稿、同一次保存**：它落的是同一张 `qy_group_grants` 行，
   * 单独开一条"备注立即保存"的旁路等于让这一格有两个写入者，而其中一个绕开了
   * 保存后的强制回读 —— 回读之后屏幕上的备注会跳回旧值，运营会再存一次。
   */
  const changeNote = useCallback(
    (userGroup: string, modelGroup: string, note: string) => {
      patchDraft(qyGmCellKey(userGroup, modelGroup), { note })
    },
    [patchDraft]
  )

  /**
   * 整列批量（全站矩阵视图专有）。
   *
   * **只作用于已设定范围的行** —— 未设定范围的行没有权威清单，往它头上写 grant
   * 只会生成一批后端必然拒绝的动作，而运营从界面上看不出来为什么保存失败。
   */
  const columnBulk = useCallback(
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
  const copyRow = useCallback(
    (fromUserGroup: string, toUserGroup: string) => {
      if (data == null) return
      setDraft((current) => {
        const next = new Map(current)
        for (const column of data.model_groups) {
          const fromKey = qyGmCellKey(fromUserGroup, column.name)
          const source = serverCells.get(fromKey)
          const entry: QyGmDraftEntry = {
            granted: qyGmGrantedOf(source, current.get(fromKey)),
            // 备注一起复制：整档复制的语义是「这一档照着那一档配」，漏掉备注
            // 会让复制出来的那一档在用户侧显示模型分组的默认说明，而运营以为
            // 两档一模一样。
            note: qyGmNoteDraftOf(source, current.get(fromKey)),
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

  const resetDraft = useCallback(() => {
    setDraft(new Map())
    setPreviewedFingerprint(null)
  }, [])

  const reload = useCallback(() => {
    setDraft(new Map())
    setPreview(null)
    setPreviewedFingerprint(null)
    void query.refetch()
  }, [query])

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
      onScopeSaved?.()
      applyServerState(fresh)
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  /**
   * 提交范围设置。
   *
   * `enforce` 那一支必须带上**单分组预览**算出的两个哈希，否则服务端重算的
   * `impact_hash` 与前端手上的对不上，切换会被 409 永久锁死。两个外壳都从这里
   * 走，闸门只有一份。
   */
  const submitScope = useCallback(
    (userGroup: string, body: QyGmScopeRequest) => {
      scopeMutation.mutate({
        userGroup,
        body:
          body.mode === 'enforce' && enforcePreview?.userGroup === userGroup
            ? {
                ...body,
                draft_hash: enforcePreview.result.draft_hash,
                impact_hash: enforcePreview.result.impact_hash,
              }
            : body,
      })
    },
    [enforcePreview, scopeMutation]
  )

  return {
    query,
    data,
    userGroups,
    serverCells,
    draft,
    counts,
    invalidCells,
    grantedCounts,
    dirtyGroups,
    caseNearMiss,
    selfExcluded,
    unscopedGroups,
    emptyScopeGroups,
    needsPreview,
    ratioDrift,
    partial,
    preview,
    previewOpen,
    setPreviewOpen,
    enforcePreview,
    isPreviewing: previewMutation.isPending,
    isEnforcePreviewing: enforcePreviewMutation.isPending,
    isSaving: saveMutation.isPending,
    isScopeSaving: scopeMutation.isPending,
    toggleGranted,
    changeRatio,
    changeNote,
    columnBulk,
    copyRow,
    resetDraft,
    reload,
    runPreview: () => previewMutation.mutate(),
    runSave: () => saveMutation.mutate(),
    runEnforcePreview: (userGroup: string) =>
      enforcePreviewMutation.mutate(userGroup),
    submitScope,
  }
}
