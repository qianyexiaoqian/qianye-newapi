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
import { Plus, Save, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { QyAdminUserGroups } from '@/features/qy/pages/admin-user-groups'

import { GroupOptionsJsonDrawer } from './components/group-options-json-drawer'
import {
  buildUsableGroupRows,
  nextRowId,
  serializeUsableGroupRows,
  type UsableGroupRow,
  type GROUP_MATRIX_PAGE_OPTION_KEYS,
} from './lib/group-options'
import { useGroupOptionSave } from './lib/use-group-option-save'

export type GroupMatrixSectionValues = {
  UserUsableGroups: string
}

/**
 * B「用户分组可用的模型分组配置」页。
 *
 * ── 主体是已经在跑的那张矩阵，这里只做外壳 ──
 *
 * `QyAdminUserGroups` 已经承担了「哪一档人能用哪些模型分组 + 交叉倍率」的
 * 全部读写，带着预览闸门、双库部分失败横幅、孤儿基线。它一行都不动 ——
 * 拆页拆的是**上游那张平铺表单**，不是把正在跑的代码再搬一次家。
 *
 * 这一页额外承担两件与矩阵同源、原本挤在上游表单里的东西：
 *
 *  1. `UserUsableGroups`（全局「用户可选分组」清单）。它是**未设定范围**的
 *     用户分组回落到的那份清单 —— 「未设定范围」这个状态只有在矩阵旁边才
 *     解释得通。放到模型分组页去，运营会把它读成「这个模型分组开不开放」，
 *     而它真正的作用域是「所有还没设定范围的那些档人」。
 *  2. 曾经还有一块 `GroupSpecialUsableGroup`（`+:` / `-:` 差分规则）的只读视图。
 *     那套规则已随后端一并下线：它从来没有真正生效过 —— 上游在差分算完之后
 *     无条件把用户分组自己补回去，把唯一有意义的 `-:自己` 恒抵消掉。
 *     「哪一档人能选哪些模型分组」现在只有一个答案，就是这一页上面那张矩阵。
 */
export function GroupMatrixSection(props: {
  defaultValues: GroupMatrixSectionValues
}) {
  return (
    <div className='space-y-6'>
      <QyAdminUserGroups />
      <FallbackUsableGroupsCard defaultValues={props.defaultValues} />
    </div>
  )
}

/**
 * 全局「用户可选分组」清单。
 *
 * 保存按钮**内联而不是走页面动作区**：这一页上面就是矩阵，矩阵自己的差异条
 * 已经占着「保存」这个语义。两个保存按钮挤在页头同一个位置、各自保存不同的
 * 东西，是这套两库设计最容易被点错的地方。
 */
function FallbackUsableGroupsCard(props: {
  defaultValues: GroupMatrixSectionValues
}) {
  const { t } = useTranslation()
  const [rows, setRows] = useState<UsableGroupRow[]>(() =>
    buildUsableGroupRows(props.defaultValues.UserUsableGroups)
  )

  // 键域刻意取 `…_OPTION_KEYS` 而不是整个 `GROUP_MATRIX_PAGE_KEYS`：
  // `GroupGroupRatio` 必须走矩阵的两阶段 PUT，从这里写会绕开预览闸门。
  const { save, resetBaseline, isSaving } = useGroupOptionSave<
    (typeof GROUP_MATRIX_PAGE_OPTION_KEYS)[number]
  >({
    UserUsableGroups: props.defaultValues.UserUsableGroups,
  })

  /*
    服务端回读到达时，用它替换本地一切（草稿 + 基线）。

    ── 为什么不做乐观合并 ──

    保存成功后 `updateOption` 会 invalidate `system-options`，回读到来时这里
    必须整份换成服务端真实值：本地草稿是「我请求过什么」，回读是「服务端现在
    是什么」，把前者当成后者渲染，一次部分失败就会画出一个从未存在过的成功
    画面。另一个管理员在别的标签页改了同一份 option 时同理 —— 服务端赢，
    而不是让本地草稿在下一次保存里把对方的改动整段覆盖掉。

    依赖列表刻意逐个列**原始值**而不是 `defaultValues` 这个对象：上层
    `build(settings)` 每次渲染都新造一个对象，按对象比会让这个 effect 在每一次
    父级重渲染时把正在编辑的内容清掉。
  */
  const serverUsableGroups = props.defaultValues.UserUsableGroups
  useEffect(() => {
    setRows(buildUsableGroupRows(serverUsableGroups))
    resetBaseline({ UserUsableGroups: serverUsableGroups })
  }, [serverUsableGroups, resetBaseline])

  const updateRow = useCallback(
    (id: string, patch: Partial<UsableGroupRow>) => {
      setRows((current) =>
        current.map((row) => (row.id === id ? { ...row, ...patch } : row))
      )
    },
    []
  )

  const handleSave = useCallback(() => {
    void save({ UserUsableGroups: serializeUsableGroupRows(rows) })
  }, [rows, save])

  return (
    <Card>
      <CardHeader className='border-b'>
        <div className='flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between'>
          <div>
            <CardTitle>{t('Selectable groups')}</CardTitle>
            <CardDescription>{t('qy_gs_fallback_usable_desc')}</CardDescription>
          </div>
          <div className='flex shrink-0 flex-wrap gap-2'>
            <GroupOptionsJsonDrawer
              fields={[
                {
                  key: 'UserUsableGroups',
                  label: t('Selectable groups'),
                  value: serializeUsableGroupRows(rows),
                },
              ]}
              onApply={(next) => {
                if (next.UserUsableGroups === undefined) return
                setRows(buildUsableGroupRows(next.UserUsableGroups))
              }}
            />
            <Button
              size='sm'
              variant='outline'
              onClick={() =>
                // id 走单调自增序列，**不从数组长度派生**：删掉任意一行后长度回落，
                // 下一次新增就与一条已存在的行撞 id，此后在其中一个输入框里打字会
                // 同时改掉两行的 name，序列化按 name 作键、后写的覆盖前写的 ——
                // 表现是「我明明加了三个，只出现两个」，而这份清单决定用户在令牌里
                // 能选到哪些模型分组。
                setRows((current) => [
                  ...current,
                  { id: nextRowId('uu'), name: '', description: '' },
                ])
              }
            >
              <Plus className='mr-2 h-4 w-4' />
              {t('Add group')}
            </Button>
            <Button size='sm' onClick={handleSave} disabled={isSaving}>
              <Save className='mr-2 h-4 w-4' />
              {isSaving ? t('Saving...') : t('Save Changes')}
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent>
        <StaticDataTable
          data={rows}
          getRowKey={(row) => row.id}
          emptyClassName='text-muted-foreground h-20 text-sm'
          emptyContent={t('qy_gs_fallback_usable_empty')}
          columns={[
            {
              id: 'name',
              header: t('qy_gs_model_groups_title'),
              className: 'min-w-40',
              cell: (row) => (
                <Input
                  value={row.name}
                  aria-label={t('Group name')}
                  onChange={(event) =>
                    updateRow(row.id, { name: event.target.value })
                  }
                />
              ),
            },
            {
              id: 'description',
              header: t('Description'),
              className: 'min-w-56',
              cell: (row) => (
                <Input
                  value={row.description}
                  placeholder={t('Group description')}
                  onChange={(event) =>
                    updateRow(row.id, { description: event.target.value })
                  }
                />
              ),
            },
            {
              id: 'actions',
              header: t('Actions'),
              className: 'text-right',
              cellClassName: 'text-right',
              cell: (row) => (
                <Button
                  variant='ghost'
                  size='sm'
                  aria-label={t('Delete')}
                  onClick={() =>
                    setRows((current) =>
                      current.filter((item) => item.id !== row.id)
                    )
                  }
                >
                  <Trash2 className='h-4 w-4' />
                </Button>
              ),
            },
          ]}
        />
      </CardContent>
    </Card>
  )
}
