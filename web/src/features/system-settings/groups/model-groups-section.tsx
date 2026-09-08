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
import { AlertTriangle, GripVertical, Plus, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { qyKeys } from '@/features/qy/lib/query-keys'
import {
  qyMgDelete,
  qyMgImpactQuery,
  qyMgListQuery,
  qyMgUpdate,
} from '@/features/qy/pages/admin-model-groups/api'
import { QyMgDeleteDialog } from '@/features/qy/pages/admin-model-groups/components/delete-dialog'
import {
  qyMgBuildRows,
  qyMgFreeNames,
  qyMgInvalidRatioNames,
  qyMgSerializeRatios,
  qyMgSerializeUsableGroups,
  qyMgSilentlyBilledNames,
  type QyMgMergedRow,
} from '@/features/qy/pages/admin-model-groups/lib/merged-rows'
import { QyPager } from '@/features/qy/pages/components/qy-pager'
import { qyOpsErrorMessage } from '@/features/qy/pages/ops/errors'

import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { GroupOptionsJsonDrawer } from './components/group-options-json-drawer'
import { GroupPricingGuideButton } from './components/group-pricing-guide'
import {
  duplicateRowNames,
  moveAutoGroup,
  nextRowId,
  parseAutoGroups,
  parseGroupDescriptionMap,
  parseGroupRatioMap,
  serializeAutoGroups,
  type MODEL_GROUP_PAGE_KEYS,
} from './lib/group-options'
import {
  changedGroupOptionKeys,
  useGroupOptionSave,
} from './lib/use-group-option-save'

/**
 * 每页几个模型分组。与「用户分组」「令牌默认分组」两张表同一个数（服务端那一侧
 * 是 `httpq.GroupTablePageSize`）—— 三张表在同一个菜单组里，页长不一致会让人
 * 以为其中一张漏了几行。
 */
const MODEL_GROUP_PAGE_SIZE = 10

export type ModelGroupsSectionValues = {
  GroupRatio: string
  AutoGroups: string
  MaxTokenAutoGroups: number
  /** 「用户可选」那一列就是它的**键**。见 `qyMgSerializeUsableGroups` 的口径说明。 */
  UserUsableGroups: string
}

/**
 * 「模型分组」页 —— **一张表**。
 *
 * ── 项目方点名的四列 ──
 *
 * 「模型分组：分组名称，兜底倍率，用户可选，分组备注。」
 *
 * 这一页此前也是两张表（上面「分组倍率」、下面「模型分组登记」），同一批名字
 * 出现两次、各带一半的列。合并的判据与用户分组页逐字相同：运营在这一页上要
 * 回答的是一个问题（这个池子按几倍收、用户看不看得见、备注写什么），那就该是
 * 一行。合并逻辑与序列化在
 * `features/qy/pages/admin-model-groups/lib/merged-rows.ts`，有测试。
 *
 * ── 「渠道数」为什么是实时反查而不是一个可配置字段 ──
 *
 * 「这个模型分组底下还有没有池子」是**事实**不是配置，而它正是 503 的直接原因：
 * 一个有倍率、没渠道的模型分组在令牌下拉里长得和正常的一模一样，选中之后每一次
 * 请求都 503。落成一个字段就一定会与现实不同步 —— 而不同步的方向恰好是
 * 「界面说有、线上没有」。拉不到时显示 `—` 且**不出告警**：把「不确定」画成
 * 「没有渠道」，整张表会挂满假警报，而假警报比没有警报更糟。
 */
export function ModelGroupsSection(props: {
  defaultValues: ModelGroupsSectionValues
}) {
  const { t } = useTranslation()
  const { defaultValues } = props
  const queryClient = useQueryClient()

  /*
    ── 服务端翻页 ────────────────────────────────────────────────────────

    项目方原话：「若分组过多加载会出现卡顿不易编辑」。这一页的服务端成本此前是
    **每行一条** `SELECT COUNT(*) FROM abilities`（「这个池子还有没有渠道」），
    三十个分组就是三十条查询；前端成本是三十行受控输入框在每一次按键上一起重算。
    翻页把两者都收窄到本页，行集合与排序改由服务端给出（并集必须与切页在同一侧，
    见 `qyMgBuildRows` 的 `names`）。

    `page` 不进 URL：这是设置页里的一张表，不是一条可以分享的路由，而
    system-settings 的路由 search schema 是整组共用的。
  */
  const [page, setPage] = useState(1)
  const pageParams = useMemo(
    () => ({ p: page, page_size: MODEL_GROUP_PAGE_SIZE }),
    [page]
  )
  const registryQuery = useQuery({ ...qyMgListQuery(pageParams), retry: false })

  /** 服务端这一页的行，未经本地草稿覆盖。 */
  const [pageRows, setPageRows] = useState<QyMgMergedRow[]>([])
  /**
   * 改过的行，**按名字索引且跨页留存**。
   *
   * ── 为什么草稿不能只活在 `pageRows` 里 ──
   *
   * 保存写的是整份 `options.GroupRatio` / `UserUsableGroups`，一次提交涵盖全站。
   * 草稿跟着 `pageRows` 走的话，翻一页就把上一页改过而没保存的倍率静默丢掉 ——
   * 而运营翻页的动机恰恰是"我要把这一批都改一遍再保存"。所以草稿按名字存，
   * 翻页只换显示的那一段，改动一条不掉。
   */
  const [drafts, setDrafts] = useState<Record<string, QyMgMergedRow>>({})
  /**
   * 本地新加、还没保存过的行。**在每一页上都渲染**（钉在表格最上面）。
   *
   * 让它只属于某一页是做不到的：它还没有名字，也就没有在服务端行轴上的位置。
   * 藏起来的表现是「点了『添加分组』什么也没发生」，或者更糟 —— 敲了一半的一行
   * 在翻页之后消失。
   */
  const [newRows, setNewRows] = useState<QyMgMergedRow[]>([])
  const [autoGroups, setAutoGroups] = useState<string[]>(() =>
    parseAutoGroups(defaultValues.AutoGroups)
  )
  const [maxTokenAutoGroups, setMaxTokenAutoGroups] = useState(
    String(defaultValues.MaxTokenAutoGroups)
  )
  const [deleting, setDeleting] = useState<string | null>(null)
  const [forceHasRoute, setForceHasRoute] = useState(false)
  const [forceOrphanTokens, setForceOrphanTokens] = useState(false)

  // 键域由归属清单给出，多写一个键即编译失败。理由见 `user-groups-section`。
  const { save, resetBaseline, isSaving } = useGroupOptionSave<
    (typeof MODEL_GROUP_PAGE_KEYS)[number]
  >({
    GroupRatio: defaultValues.GroupRatio,
    AutoGroups: defaultValues.AutoGroups,
    MaxTokenAutoGroups: defaultValues.MaxTokenAutoGroups,
    UserUsableGroups: defaultValues.UserUsableGroups,
  })

  const listData = registryQuery.data
  const registryItems = listData?.items
  /*
    总条数拿不到时回落成**本页行数**而不是 0：0 会让翻页条整条隐藏，于是老后端
    （不认翻页参数、原样回整表）上这一页会一次性画出全部行且没有任何翻页控件 ——
    那与"翻页功能没做"长得一模一样，而真实原因是后端没升级。
  */
  const total = listData?.total ?? registryItems?.length ?? 0
  /** 全量行名（服务端排序）。老后端不下发它，此时退回"只知道本页"。 */
  const allNames = listData?.names
  const serverNoChannelNames = listData?.no_channel_names

  /*
    ── 两个 effect，因为它们回答的是两个问题 ──────────────────────────────

    A) 「这一页现在该画哪几行」—— 服务端这一页 ⊗ 当前 option。翻页会重跑它。
    B) 「本地草稿还算数吗」—— 只有 option 本身变了才不算数。

    合成一个（也就是让 A 的依赖里那个 `registryItems` 一并触发清空草稿）会让
    **翻一页就丢掉上一页所有没保存的改动** —— 而运营翻页的动机恰恰是"我要把
    这一批都改一遍再一起保存"。这是本轮翻页最容易埋进去的那个坑。

    依赖刻意逐个列**原始值**：上层 `build(settings)` 每次渲染都新造一个对象，
    按对象比会让这两个 effect 在每一次父级重渲染时把正在编辑的内容清掉。
  */
  useEffect(() => {
    setPageRows(
      qyMgBuildRows({
        registry: registryItems ?? [],
        groupRatios: parseGroupRatioMap(defaultValues.GroupRatio),
        usableGroups: parseGroupDescriptionMap(defaultValues.UserUsableGroups),
        autoGroups: parseAutoGroups(defaultValues.AutoGroups),
        // 服务端下发行轴时原样用它（含排序与并集）。取数未回来时是 undefined，
        // 此时退回 qyMgBuildRows 自己的本地并集 —— 那正是老后端上的行为。
        names: registryItems?.map((row) => row.name),
      })
    )
  }, [
    registryItems,
    defaultValues.GroupRatio,
    defaultValues.UserUsableGroups,
    defaultValues.AutoGroups,
  ])

  /*
    option 变了 = 保存落地了，或者另一个管理员在别的标签页改了同一份。两种情况下
    本地草稿都必须整体丢弃：草稿是「我请求过什么」，option 是「服务端现在是什么」，
    把前者当后者渲染，一次部分失败就会画出一个从未存在过的成功画面 —— 服务端赢。

    `newRows` 一起清掉：保存成功之后那几行已经是服务端的行了，留着会让同一个分组
    在表上出现两次（一次钉在顶部的"新行"、一次在服务端那一页里）。
  */
  useEffect(() => {
    setDrafts({})
    setNewRows([])
    setAutoGroups(parseAutoGroups(defaultValues.AutoGroups))
    setMaxTokenAutoGroups(String(defaultValues.MaxTokenAutoGroups))
    resetBaseline({
      GroupRatio: defaultValues.GroupRatio,
      AutoGroups: defaultValues.AutoGroups,
      MaxTokenAutoGroups: defaultValues.MaxTokenAutoGroups,
      UserUsableGroups: defaultValues.UserUsableGroups,
    })
  }, [
    defaultValues.GroupRatio,
    defaultValues.AutoGroups,
    defaultValues.MaxTokenAutoGroups,
    defaultValues.UserUsableGroups,
    resetBaseline,
  ])

  /*
    删掉最后一页上最后一个分组之后，页码会停在一个不存在的页上 —— 表格空、
    翻页条写着「第 4 页 / 共 3 页」，而运营刚做的是一次成功的删除。
  */
  useEffect(() => {
    if (listData == null) return
    const lastPage = Math.max(1, Math.ceil(total / MODEL_GROUP_PAGE_SIZE))
    if (page > lastPage) setPage(lastPage)
  }, [listData, total, page])

  /** 屏幕上这一张表：新加的行钉在最上面，其余是本页的行叠上草稿。 */
  const rows = useMemo(
    () => [...newRows, ...pageRows.map((row) => drafts[row.name] ?? row)],
    [newRows, pageRows, drafts]
  )

  /**
   * 这一页此刻**持有**的行 —— 保存时它有权改写的那些键。
   *
   * 与 `rows` 的区别是刻意的：`rows` 是"屏幕上画什么"，这一份是"保存写什么"。
   * 没被动过的本页行不在里面 —— 把它们一起写回去，只会让另一个管理员在这期间
   * 对同一个键的改动被本页打开那一刻的快照覆盖，而两边都看不出发生过什么。
   */
  const ownedRows = useMemo(
    () => [...newRows, ...Object.values(drafts)],
    [newRows, drafts]
  )

  /** 基线：服务端此刻的整份 option。保存时未被本页持有的键原样带过去。 */
  const baseRatios = useMemo(
    () => parseGroupRatioMap(defaultValues.GroupRatio),
    [defaultValues.GroupRatio]
  )
  const baseUsable = useMemo(
    () => parseGroupDescriptionMap(defaultValues.UserUsableGroups),
    [defaultValues.UserUsableGroups]
  )

  const nextRatios = useMemo(
    () => qyMgSerializeRatios(ownedRows, baseRatios),
    [ownedRows, baseRatios]
  )
  const nextUsable = useMemo(
    () => qyMgSerializeUsableGroups(ownedRows, baseUsable),
    [ownedRows, baseUsable]
  )

  /*
    ── 三条告警按**全站**口径算，不是按本页 ────────────────────────────

    它们说的都是钱：「用户选得到、却没有兜底倍率」的分组按凭空的 1.0 计费，
    「倍率是 0」的分组白送。一条随翻页出现又消失的资金告警，读到的人只会认为
    它不可靠 —— 那比没有告警更糟。

    判据取**保存后会写进去的那两份 map**（基线叠上本页草稿），而不是屏幕上的行：
    前者恰好就是"按下保存之后全站会变成什么样"，也正是运营需要在按之前看到的。
  */
  const mergedRatios = useMemo(
    () => parseGroupRatioMap(nextRatios),
    [nextRatios]
  )
  const mergedUsable = useMemo(
    () => parseGroupDescriptionMap(nextUsable),
    [nextUsable]
  )
  /*
    把那两份 map 铺成一套**全站**的行，再喂给与本页表格同一批判据函数。

    不另写一份 `Object.keys(...).filter(...)`：那两条判据（「可选却没有兜底倍率」
    = 正按凭空的 1.0 计费、「倍率是 0」= 白送）是有测试盯着的，而它们的第二份
    实现会照着本仓一贯的形状漂移 —— 漂移的方向恰好是"某一档钱的问题不再报警"。
    `registry: []` 是刻意的：这里只关心倍率与可选性，登记表那些列（渠道数、
    来源徽标）与这两条判据无关。
  */
  const mergedRows = useMemo(
    () =>
      qyMgBuildRows({
        registry: [],
        groupRatios: mergedRatios,
        usableGroups: mergedUsable,
        autoGroups,
      }),
    [mergedRatios, mergedUsable, autoGroups]
  )
  const silentlyBilled = useMemo(
    () => qyMgSilentlyBilledNames(mergedRows),
    [mergedRows]
  )
  const freeGroups = useMemo(() => qyMgFreeNames(mergedRows), [mergedRows])
  const emptyPools = useMemo(() => {
    // 服务端给的是**全量**「可选却没有渠道」名单（no_channel_names）。本页动过的
    // 行再叠一遍，让开关刚被关掉的那一行立刻从告警里消失、刚被打开的立刻出现。
    const names = new Set(
      (serverNoChannelNames ?? []).filter((name) =>
        Object.hasOwn(mergedUsable, name)
      )
    )
    for (const row of rows) {
      const name = row.name.trim()
      if (name === '') continue
      if (row.selectable && row.hasRoute === false) names.add(name)
      else if (!row.selectable) names.delete(name)
    }
    return [...names]
  }, [serverNoChannelNames, mergedUsable, rows])

  /*
    重名判据要跨页：在第 2 页新建一个与第 1 页同名的分组，保存时会静默覆盖
    那一行的兜底倍率。`allNames` 是服务端下发的全量行名；拿不到（老后端）时
    退回只比本页，与改造之前一致。
  */
  const duplicates = useMemo(() => {
    const found = new Set(duplicateRowNames(rows))
    const existing = new Set(allNames ?? [])
    for (const row of newRows) {
      const name = row.name.trim()
      if (name !== '' && existing.has(name)) found.add(name)
    }
    return [...found]
  }, [rows, newRows, allNames])
  const invalidRatios = useMemo(() => qyMgInvalidRatioNames(rows), [rows])

  const parsedMax = Number(maxTokenAutoGroups)
  const maxInvalid =
    !Number.isInteger(parsedMax) || parsedMax < 1 || maxTokenAutoGroups === ''

  /**
   * 改一行。新加的行改在 `newRows` 里，已存在的行落进跨页草稿。
   *
   * 定位用 `id` 而不是名字：新加那一行的名字正在被敲，按名字定位会在每一个
   * 字符上换一次索引键。
   */
  const updateRow = useCallback(
    (id: string, patch: Partial<QyMgMergedRow>) => {
      if (newRows.some((row) => row.id === id)) {
        setNewRows((current) =>
          current.map((row) => (row.id === id ? { ...row, ...patch } : row))
        )
        return
      }
      const target = pageRows.find((row) => row.id === id)
      if (target == null) return
      setDrafts((current) => ({
        ...current,
        [target.name]: { ...(current[target.name] ?? target), ...patch },
      }))
    },
    [newRows, pageRows]
  )

  const addRow = useCallback(() => {
    const taken = new Set([
      ...(allNames ?? []),
      ...pageRows.map((row) => row.name.trim()),
    ])
    setNewRows((current) => {
      for (const row of current) taken.add(row.name.trim())
      let index = 1
      let name = `group_${index}`
      while (taken.has(name)) {
        index += 1
        name = `group_${index}`
      }
      return [
        ...current,
        {
          // id 走单调自增序列，**不从名字派生**：这一行的名字正在被敲，
          // 从名字派生就是每敲一个字换一次 React key —— 输入框每个字符失焦一次。
          id: nextRowId('mg'),
          name,
          ratio: '1',
          selectable: false,
          note: '',
          usableDescription: '',
          sources: [],
          hasRoute: null,
          channelCount: null,
          legacyDual: false,
          autoPosition: 0,
          registered: false,
          isNew: true,
        },
      ]
    })
  }, [allNames, pageRows])

  const handleSave = useCallback(() => {
    void save({
      GroupRatio: nextRatios,
      UserUsableGroups: nextUsable,
      AutoGroups: serializeAutoGroups(autoGroups),
      MaxTokenAutoGroups: parsedMax,
    })
  }, [save, nextRatios, nextUsable, autoGroups, parsedMax])

  const refreshRegistry = useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: qyKeys.adminModelGroups() })
  }, [queryClient])

  /*
    备注保存成功后**刻意不刷新登记表**。

    刷新会让 `registryItems` 换一个引用，上面那个 effect 随即用服务端数据整个
    `setRows()` + `resetBaseline()`——于是同一张表上别的行里还没保存的兜底倍率与
    「用户可选」改动被静默复原，而屏幕上只有一句绿色的「备注已保存」。运营接着
    点顶部「保存」，写回去的是旧值，他以为改价已经落地。

    这次写入的唯一产物就是这一行的 `note`，而本地行里已经是它了 —— 没有任何
    需要从服务端拿回来的东西。删除是另一回事：那会改变行的集合，必须重建。
  */
  const noteMutation = useMutation({
    mutationFn: (input: { name: string; note: string }) =>
      qyMgUpdate(input.name, { note: input.note }),
    onSuccess: () => toast.success(t('qy_mg_note_saved')),
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const impactQuery = useQuery({ ...qyMgImpactQuery(deleting), retry: false })

  const deleteMutation = useMutation({
    mutationFn: (name: string) =>
      qyMgDelete(name, {
        force_has_route: forceHasRoute,
        force_orphan_tokens: forceOrphanTokens,
      }),
    onSuccess: async (result) => {
      // 半成状态必须原样弹出来并留在屏幕上：两库不原子的最坏失败方式是运营看到
      // 一句绿色的「已删除」然后走人，而线上停在中间态。
      if (result.partial == null) {
        toast.success(
          t('qy_mg_deleted', { keys: result.removed_from.join('、') })
        )
      } else {
        toast.error(result.partial.message, {
          duration: Number.POSITIVE_INFINITY,
        })
      }
      closeDelete()
      await refreshRegistry()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const closeDelete = useCallback(() => {
    setDeleting(null)
    // 两个覆盖勾选**必须**跟着弹窗一起清掉：留着的话下一次删除会带着上一次的
    // 勾选状态打开，而那两个勾选正是「我知道这会让 200 个令牌挂掉」的全部凭据。
    setForceHasRoute(false)
    setForceOrphanTokens(false)
  }, [])

  /*
    auto 顺序的候选清单必须是**全站**的：只列本页的话，第 11 个以后的模型分组
    永远进不了 auto 队列，而界面上看不出少了什么。`allNames` 是服务端下发的
    全量行名，再并上本地新加、还没保存过的那几行。
  */
  const autoCandidates = useMemo(() => {
    const names = new Set([
      ...(allNames ?? rows.map((row) => row.name)),
      ...newRows.map((row) => row.name),
    ])
    return [...names]
      .map((name) => name.trim())
      .filter((name) => name !== '' && !autoGroups.includes(name))
  }, [allNames, rows, newRows, autoGroups])

  const saveBlocked =
    duplicates.length > 0 || invalidRatios.length > 0 || maxInvalid

  /*
    这一页此刻有没有**还没按保存**的改动。

    删除是唯一一个必须让服务端重建整张表的动作（它改的正是 `options.GroupRatio`
    的键集合），所以它会连带丢掉同屏未保存的兜底倍率 /「用户可选」草稿。
    拦不住也不该拦 —— 但必须说出来：一句绿色的「已删除」加上一批悄悄复原的数字，
    正是这一页最贵的失败方式。判据复用保存路径那份逐键差分，不另写一套。
  */
  const hasUnsavedEdits =
    changedGroupOptionKeys(
      {
        AutoGroups: serializeAutoGroups(autoGroups),
        GroupRatio: nextRatios,
        MaxTokenAutoGroups: parsedMax,
        UserUsableGroups: nextUsable,
      },
      {
        AutoGroups: defaultValues.AutoGroups,
        GroupRatio: defaultValues.GroupRatio,
        MaxTokenAutoGroups: defaultValues.MaxTokenAutoGroups,
        UserUsableGroups: defaultValues.UserUsableGroups,
      }
    ).length > 0

  return (
    <SettingsSection title={t('qy_gs_model_groups_title')}>
      <SettingsPageFormActions
        onSave={handleSave}
        isSaving={isSaving}
        isSaveDisabled={saveBlocked}
      />

      <div className='flex flex-wrap items-center justify-between gap-2'>
        <p className='text-muted-foreground min-w-0 text-sm'>
          {t('qy_gs_model_groups_desc')}
        </p>
        <div className='flex shrink-0 flex-wrap gap-2'>
          <GroupPricingGuideButton />
          <GroupOptionsJsonDrawer
            fields={[
              {
                key: 'GroupRatio',
                label: t('Group ratios'),
                // **整份**而不是本页：抽屉是"直接编 JSON"的逃生口，给它一份只有
                // 10 个键的文本，运营按下应用就等于删掉了其余全部分组的倍率。
                value: nextRatios,
              },
              {
                key: 'UserUsableGroups',
                label: t('Selectable groups'),
                value: nextUsable,
                description: t('qy_mg_usable_value_is_legacy'),
              },
              {
                key: 'AutoGroups',
                label: t('Auto assignment order'),
                value: serializeAutoGroups(autoGroups),
              },
            ]}
            onApply={(next) => {
              /*
                三份 option 一起重建行：只应用其中一份的话，另外两份仍是旧值，
                而它们共用同一批行 —— 表现是勾选状态与倍率对不上号。

                重建的是**本页的行**，然后整页登记成草稿：抽屉编的是整份 JSON，
                它对本页之外的键的改动必须原样保留到保存那一刻，而这一页只对
                自己持有的行负责（见 `ownedRows`）。所以本页每一行都进草稿，
                页外的差异靠 `nextRatios` 的基线带过去。
              */
              const applied = qyMgBuildRows({
                registry: registryItems ?? [],
                groupRatios: parseGroupRatioMap(next.GroupRatio ?? nextRatios),
                usableGroups: parseGroupDescriptionMap(
                  next.UserUsableGroups ?? nextUsable
                ),
                autoGroups: parseAutoGroups(
                  next.AutoGroups ?? serializeAutoGroups(autoGroups)
                ),
                names: registryItems?.map((row) => row.name),
              })
              setPageRows(applied)
              setDrafts(
                Object.fromEntries(applied.map((row) => [row.name, row]))
              )
              if (next.AutoGroups !== undefined) {
                setAutoGroups(parseAutoGroups(next.AutoGroups))
              }
            }}
          />
        </div>
      </div>

      {duplicates.length > 0 && (
        <Alert variant='destructive'>
          <AlertTriangle className='h-4 w-4' />
          <AlertDescription>
            {t('Duplicate group names: {{names}}', {
              names: duplicates.join(', '),
            })}
          </AlertDescription>
        </Alert>
      )}

      {invalidRatios.length > 0 && (
        <Alert variant='destructive'>
          <AlertTriangle className='h-4 w-4' />
          <AlertDescription>
            {t('qy_gs_invalid_ratio_warn', { names: invalidRatios.join(', ') })}
          </AlertDescription>
        </Alert>
      )}

      {silentlyBilled.length > 0 && (
        <Alert>
          <AlertTriangle className='h-4 w-4' />
          <AlertTitle>{t('qy_gs_missing_ratio_title')}</AlertTitle>
          <AlertDescription>
            {t('qy_gs_missing_ratio_warn', {
              names: silentlyBilled.join(', '),
            })}
          </AlertDescription>
        </Alert>
      )}

      {emptyPools.length > 0 && (
        <Alert>
          <AlertTriangle className='h-4 w-4' />
          <AlertTitle>{t('qy_gs_no_channels_title')}</AlertTitle>
          <AlertDescription>
            {t('qy_gs_no_channels_warn', { names: emptyPools.join(', ') })}
          </AlertDescription>
        </Alert>
      )}

      {freeGroups.length > 0 && (
        <Alert>
          <AlertTriangle className='h-4 w-4' />
          <AlertDescription>
            {t('qy_gs_free_ratio_warn', { names: freeGroups.join(', ') })}
          </AlertDescription>
        </Alert>
      )}

      <Card>
        <CardHeader className='border-b'>
          <div className='flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between'>
            <div>
              <CardTitle>{t('qy_gs_model_groups_title')}</CardTitle>
              <CardDescription>{t('qy_mg_table_desc')}</CardDescription>
            </div>
            <Button onClick={addRow} size='sm' className='sm:self-start'>
              <Plus className='mr-2 h-4 w-4' />
              {t('Add group')}
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          <StaticDataTable
            data={rows}
            getRowKey={(row) => row.id}
            emptyClassName='text-muted-foreground h-20 text-sm'
            emptyContent={t('No groups yet. Add a group to get started.')}
            columns={[
              {
                id: 'name',
                header: t('Group name'),
                className: 'min-w-48',
                cell: (row) =>
                  row.isNew ? (
                    // 还没保存过的一行才可以改名字。已存在的名字改在这里只会改掉
                    // `options.GroupRatio` 的键，而路由表、令牌、授权、套餐解锁
                    // 一个都不动 —— 详见 `QyMgMergedRow.isNew` 的说明。
                    <Input
                      value={row.name}
                      aria-label={t('Group name')}
                      aria-invalid={duplicates.includes(row.name.trim())}
                      onChange={(event) =>
                        updateRow(row.id, { name: event.target.value })
                      }
                    />
                  ) : (
                    <div className='min-w-0'>
                      <div className='text-sm font-medium break-words'>
                        {row.name}
                      </div>
                      <div className='mt-1 flex flex-wrap gap-1'>
                        {row.sources.map((source) => (
                          <StatusBadge
                            key={source}
                            copyable={false}
                            variant={
                              source === 'registry_only' ? 'neutral' : 'info'
                            }
                          >
                            {t(`qy_mg_source_${source}`)}
                          </StatusBadge>
                        ))}
                        {row.hasRoute === false && (
                          <StatusBadge variant='danger' copyable={false}>
                            {t('qy_gs_no_channels')}
                          </StatusBadge>
                        )}
                        {row.legacyDual && (
                          <StatusBadge variant='warning' copyable={false}>
                            {t('qy_mg_legacy_dual')}
                          </StatusBadge>
                        )}
                        {row.autoPosition > 0 && (
                          <StatusBadge variant='neutral' copyable={false}>
                            {t('Position {{position}}', {
                              position: row.autoPosition,
                            })}
                          </StatusBadge>
                        )}
                      </div>
                    </div>
                  ),
              },
              {
                id: 'ratio',
                header: t('qy_gs_col_base_ratio'),
                className: 'w-32',
                cell: (row) =>
                  row.ratio === null ? (
                    // 「不在 GroupRatio 里」不是 1，也不是空 —— 它是一个正在
                    // 静默按 1.0 扣费的状态。给一个显式的「补上倍率」动作，
                    // 而不是一个看起来已经填好的输入框。
                    <Button
                      variant='outline'
                      size='sm'
                      onClick={() => updateRow(row.id, { ratio: '1' })}
                    >
                      {t('qy_gs_add_missing_ratio')}
                    </Button>
                  ) : (
                    <Input
                      type='number'
                      min={0}
                      step={0.1}
                      value={row.ratio}
                      aria-label={t('qy_gs_col_base_ratio')}
                      aria-invalid={invalidRatios.includes(row.name.trim())}
                      onChange={(event) =>
                        updateRow(row.id, { ratio: event.target.value })
                      }
                    />
                  ),
              },
              {
                /*
                  「用户可选」= 这个名字在 `options.UserUsableGroups` 里有没有键。

                  它是用户分组**没设**可用清单时的判据（项目方原话：「若没有配置
                  模型分组，则用户可选选中的…全部都可以选择」），所以它必须与
                  用户分组页那一列的「按模型分组的用户可选来」是同一份数据。
                */
                id: 'selectable',
                header: t('User selectable'),
                className: 'w-28 text-center',
                cellClassName: 'text-center',
                cell: (row) => (
                  <Switch
                    checked={row.selectable}
                    aria-label={t('User selectable')}
                    onCheckedChange={(checked) =>
                      updateRow(row.id, { selectable: checked })
                    }
                  />
                ),
              },
              {
                id: 'note',
                header: t('qy_mg_col_note'),
                className: 'min-w-64',
                cell: (row) => (
                  <QyMgNoteCell
                    row={row}
                    isSaving={
                      noteMutation.isPending &&
                      noteMutation.variables?.name === row.name
                    }
                    onDraftChange={(value) =>
                      updateRow(row.id, { note: value })
                    }
                    onSave={(value) =>
                      noteMutation.mutate({ name: row.name, note: value })
                    }
                  />
                ),
              },
              {
                id: 'actions',
                header: t('Actions'),
                className: 'text-right',
                cellClassName: 'text-right',
                cell: (row) =>
                  row.isNew ? (
                    // 没保存过的行只需要从本地列表里去掉，没有任何服务端引用。
                    <Button
                      variant='ghost'
                      size='sm'
                      aria-label={t('Delete')}
                      onClick={() =>
                        setNewRows((current) =>
                          current.filter((item) => item.id !== row.id)
                        )
                      }
                    >
                      <Trash2 className='h-4 w-4' />
                    </Button>
                  ) : (
                    /*
                      已存在的行走**联动删除**，删之前强制看一次影响面。

                      合并之前这里有两个垃圾桶：倍率表那个只把一行从
                      `options.GroupRatio` 里去掉（其余十一处引用原样留着，
                      而那个分组从此按凭空的 1.0 计费），登记表那个才是真的删。
                      两个长得一样、后果差一个量级 —— 留后者。
                    */
                    <Button
                      variant='ghost'
                      size='sm'
                      aria-label={t('Delete')}
                      disabled={!row.registered}
                      title={
                        row.registered
                          ? undefined
                          : t('qy_mg_delete_no_registry')
                      }
                      onClick={() => {
                        // 删除会让服务端重建整张表。同屏未保存的改动因此会被
                        // 复原，而屏幕上只有一句「已删除」——先把这件事说出来。
                        if (hasUnsavedEdits) {
                          toast.warning(t('qy_mg_delete_discards_draft'), {
                            duration: Number.POSITIVE_INFINITY,
                          })
                        }
                        setDeleting(row.name)
                      }}
                    >
                      <Trash2 className='h-4 w-4' />
                    </Button>
                  ),
              },
            ]}
          />

          <QyPager
            page={page}
            pageSize={MODEL_GROUP_PAGE_SIZE}
            total={total}
            onPageChange={setPage}
            disabled={registryQuery.isFetching}
          />

          {/*
            保存写的是**整份** `GroupRatio` / `UserUsableGroups`，不只是屏幕上这
            10 行（页外的键由基线原样带过去，见 `ownedRows`）。说出来是因为翻页
            会让人自然以为保存的粒度也跟着变成一页 —— 而那个误解的方向是危险的：
            他会在每一页上各按一次保存，其中任何一次失败都看不出来。
          */}
          <p className='text-muted-foreground mt-2 text-xs leading-5'>
            {t('qy_mg_save_scope')}
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className='border-b'>
          <CardTitle>{t('Auto assignment order')}</CardTitle>
          <CardDescription>
            {t(
              'Priority order for tokens in the auto group. The system tries groups from top to bottom.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className='space-y-4'>
          <div className='space-y-1.5'>
            <Label htmlFor='max-token-auto-groups'>
              {t('Maximum custom groups per token')}
            </Label>
            <Input
              id='max-token-auto-groups'
              type='number'
              min={1}
              step={1}
              value={maxTokenAutoGroups}
              aria-invalid={maxInvalid}
              onChange={(event) => setMaxTokenAutoGroups(event.target.value)}
            />
            <p className='text-muted-foreground text-xs leading-5'>
              {maxInvalid
                ? t('Enter a positive integer')
                : t(
                    'Limits only token-specific Auto snapshots. Global Auto inheritance remains unlimited.'
                  )}
            </p>
          </div>

          <Select
            value={null}
            onValueChange={(value) => {
              if (typeof value !== 'string' || value === '') return
              setAutoGroups((current) =>
                current.includes(value) ? current : [...current, value]
              )
            }}
          >
            <SelectTrigger className='w-56'>
              <SelectValue placeholder={t('Add group')} />
            </SelectTrigger>
            <SelectContent alignItemWithTrigger={false}>
              <SelectGroup>
                {autoCandidates.map((name) => (
                  <SelectItem key={name} value={name}>
                    {name}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>

          {autoGroups.length > 0 && (
            <div className='space-y-2'>
              {autoGroups.map((group, index) => (
                <div
                  key={group}
                  className='flex items-center gap-2 rounded-md border p-3'
                >
                  <GripVertical className='text-muted-foreground h-4 w-4' />
                  <span className='font-medium'>{group}</span>
                  {/*
                    判据是**全站**的兜底倍率表（保存后会写进去的那一份），不是
                    屏幕上这一页：按本页判，auto 队列里的每一项在别的页上都会被
                    标成"不在倍率表里"——一条恒亮的假红标。
                  */}
                  {!Object.hasOwn(mergedRatios, group) && (
                    <StatusBadge variant='danger' copyable={false}>
                      <AlertTriangle className='mr-1 h-3 w-3' />
                      {t('Not in pricing table')}
                    </StatusBadge>
                  )}
                  <div className='ml-auto flex gap-1'>
                    <Button
                      variant='ghost'
                      size='sm'
                      disabled={index === 0}
                      onClick={() =>
                        setAutoGroups((current) =>
                          moveAutoGroup(current, index, 'up')
                        )
                      }
                    >
                      ↑
                    </Button>
                    <Button
                      variant='ghost'
                      size='sm'
                      disabled={index === autoGroups.length - 1}
                      onClick={() =>
                        setAutoGroups((current) =>
                          moveAutoGroup(current, index, 'down')
                        )
                      }
                    >
                      ↓
                    </Button>
                    <Button
                      variant='ghost'
                      size='sm'
                      aria-label={t('Delete')}
                      onClick={() =>
                        setAutoGroups((current) =>
                          current.filter((_, position) => position !== index)
                        )
                      }
                    >
                      <Trash2 className='h-4 w-4' />
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      <QyMgDeleteDialog
        open={deleting != null}
        onOpenChange={(open) => {
          if (!open) closeDelete()
        }}
        impact={impactQuery.data ?? null}
        isLoading={impactQuery.isFetching}
        isDeleting={deleteMutation.isPending}
        forceHasRoute={forceHasRoute}
        forceOrphanTokens={forceOrphanTokens}
        onForceHasRouteChange={setForceHasRoute}
        onForceOrphanTokensChange={setForceOrphanTokens}
        onConfirm={() => {
          if (deleting == null) return
          deleteMutation.mutate(deleting)
        }}
      />
    </SettingsSection>
  )
}

/**
 * 分组备注单元格。
 *
 * ── 它是这句文案**唯一**的编辑面 ──
 *
 * 站上此前有两份用户侧文案：这里的 `note`，以及 `options.UserUsableGroups` 的
 * value（上游本来就把它当说明用，本站里有真实内容，如「浅夜自有分组用户数据，
 * 本站均不会留存…」）。两份并存时，无论谁覆盖谁，界面上都要多解释一条覆盖规则，
 * 而"一份文案两个来源"正是这次要消掉的复杂度。
 *
 * 口径拍定为：**`note` 唯一**。理由是它能表达项目方要的那条优先级链的第二层，
 * 而 `UserUsableGroups` 的 value 是 per-模型分组 的，结构上表达不了第一层
 * （用户分组 × 模型分组 的按格备注）—— 留着它只会多一条永远排第三的路。
 *
 * 历史数据不靠迁移脚本：`note` 为空而旧文案非空的行上给一句灰字与一个
 * 「采用为分组备注」的按钮，运营点一次就把它搬进唯一的那个来源。搬完之前，
 * 用户看到的仍然是旧文案（后端在 `note` 为空时回落它），所以这里必须**说出来**
 * 它此刻正在生效，而不是让运营对着一个空输入框以为没人配过。
 *
 * 备注**单独一个保存键**，不跟着页面顶部那个走：它落的是登记表
 * （`qy_model_groups`），页面顶部那个写的是上游 `options`。两者失败方式完全不同，
 * 合成一次点击的话，一半成功一半失败时界面上只有一句「已保存」。
 */
function QyMgNoteCell(props: {
  row: QyMgMergedRow
  isSaving: boolean
  onDraftChange: (value: string) => void
  onSave: (value: string) => void
}) {
  const { t } = useTranslation()
  const { row } = props
  const legacy = row.usableDescription.trim()
  const showLegacy = legacy !== '' && row.note.trim() === ''

  return (
    <div className='min-w-0 space-y-1'>
      <div className='flex items-center gap-2'>
        {/*
          未登记的行（刚用「新建分组」加出来的、以及只存在于 options 里的历史
          名字）在 `qy_model_groups` 里没有行，备注端点无从下手。禁用而不是隐藏，
          但**必须带上 title**：新建流程必然命中这一档，而一个灰掉且不作任何解释
          的输入框会让运营以为这一列坏了，而不是"先按顶部保存"。
        */}
        <Input
          value={row.note}
          disabled={!row.registered}
          title={row.registered ? undefined : t('qy_mg_note_needs_registry')}
          aria-label={t('qy_mg_col_note')}
          placeholder={t('qy_mg_note_placeholder')}
          onChange={(event) => props.onDraftChange(event.target.value)}
        />
        <Button
          size='sm'
          variant='outline'
          disabled={!row.registered || props.isSaving}
          title={row.registered ? undefined : t('qy_mg_note_needs_registry')}
          onClick={() => props.onSave(row.note)}
        >
          {props.isSaving ? t('Saving...') : t('Save')}
        </Button>
      </div>
      {!row.registered && (
        <p className='text-muted-foreground text-[11px] leading-4'>
          {t('qy_mg_note_needs_registry')}
        </p>
      )}
      {showLegacy && (
        <p className='text-muted-foreground flex flex-wrap items-center gap-1 text-xs leading-5'>
          <span>{t('qy_mg_note_legacy_active', { text: legacy })}</span>
          <Button
            size='sm'
            variant='ghost'
            className='h-6 px-1.5 text-xs'
            disabled={!row.registered}
            onClick={() => props.onDraftChange(legacy)}
          >
            {t('qy_mg_note_adopt_legacy')}
          </Button>
        </p>
      )}
    </div>
  )
}
