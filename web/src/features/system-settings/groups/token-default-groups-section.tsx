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
import { useQuery } from '@tanstack/react-query'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  getModelGroupOptions,
  getUserGroupOptions,
} from '@/features/users/api'

import { SettingsSection } from '../components/settings-section'
import { useGroupOptionSave } from './lib/use-group-option-save'

/**
 * 「不设置」在 Select 里的哨兵值。
 *
 * 不能用空串：Base UI 的 `SelectItem` 把空 value 当成「没有值」，那一项点下去
 * 不会触发 onValueChange，运营会以为自己清不掉一个已经配好的默认分组。
 */
const NONE = '__none__'

/** 保存到 option 的 JSON 键域：用户分组名 → 模型分组名。 */
type DefaultsMap = Record<string, string>

function parseDefaults(raw: string | undefined): DefaultsMap {
  if (!raw) return {}
  try {
    const parsed: unknown = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    const out: DefaultsMap = {}
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      if (typeof v === 'string' && k !== '' && v !== '') out[k] = v
    }
    return out
  } catch {
    return {}
  }
}

/**
 * 「令牌默认分组」配置页。
 *
 * 每个用户分组单独配一个模型分组；用户打开新建令牌时，分组下拉预选他所在
 * 用户分组配的那一个。
 *
 * ── 三件必须记住的事 ──
 *
 * 1. 这里配的是**界面预选值**，不改变任何已存在的令牌，也不改变请求期行为。
 *    请求期那一档是扩展的 `qy_user_groups.default_model_group`（只作用于
 *    分组为空的令牌），两者刻意分开，理由见 `setting/token_default_group.go`。
 *
 * 2. **不做存在性校验的地方在这里，不在服务端。** 服务端只在读取时按每个用户
 *    真实的可选清单裁一次 —— 同一个模型分组对 A 档合法、对 B 档非法完全正常。
 *    因此这一页允许配一个"某些人选不了"的分组，那些人只是拿不到预选值而已。
 *
 * 3. 候选清单走上游的两个端点，**不依赖扩展是否启用** —— 这正是它没有并进
 *    「用户分组」页的原因（那一页的表行来自扩展的矩阵接口）。
 */
export function TokenDefaultGroupsSection(props: {
  defaultValues: { TokenDefaultGroups: string }
}) {
  const { t } = useTranslation()
  const { defaultValues } = props

  const userGroupsQuery = useQuery({
    queryKey: ['user-group-options'],
    queryFn: getUserGroupOptions,
  })
  const modelGroupsQuery = useQuery({
    queryKey: ['model-group-options'],
    queryFn: getModelGroupOptions,
  })

  const userGroups = useMemo(
    () => userGroupsQuery.data?.data ?? [],
    [userGroupsQuery.data]
  )
  const modelGroups = useMemo(
    () => modelGroupsQuery.data?.data ?? [],
    [modelGroupsQuery.data]
  )

  const [draft, setDraft] = useState<DefaultsMap>(() =>
    parseDefaults(defaultValues.TokenDefaultGroups)
  )

  const { save, resetBaseline, isSaving } =
    useGroupOptionSave<'TokenDefaultGroups'>({
      TokenDefaultGroups: defaultValues.TokenDefaultGroups,
    })

  /*
    服务端回读到达时用它替换本地一切（草稿 + 基线）。

    本地状态是「我请求过什么」，回读是「服务端现在是什么」；把前者当后者渲染，
    一次部分失败就会画出一个从未存在过的成功画面。依赖列**原始字符串**而不是
    `defaultValues` 对象：上层每次渲染都新造一个对象，按对象比会让这个 effect
    在父级每次重渲染时把正在编辑的内容清掉。
  */
  useEffect(() => {
    setDraft(parseDefaults(defaultValues.TokenDefaultGroups))
    resetBaseline({ TokenDefaultGroups: defaultValues.TokenDefaultGroups })
  }, [defaultValues.TokenDefaultGroups, resetBaseline])

  const setOne = useCallback((userGroup: string, modelGroup: string) => {
    setDraft((prev) => {
      const next = { ...prev }
      // 删键而不是留空串：后端 ValidateTokenDefaultGroups 直接拒绝空值，
      // 而「取消默认」在数据上就该表现为这一项不存在。
      if (modelGroup === NONE) delete next[userGroup]
      else next[userGroup] = modelGroup
      return next
    })
  }, [])

  const onSave = async () => {
    try {
      await save({ TokenDefaultGroups: JSON.stringify(draft) })
      toast.success(t('qy_gs_token_default_saved'))
    } catch {
      // useGroupOptionSave 已经把失败详情弹过一次，这里不重复报错。
    }
  }

  const isLoading = userGroupsQuery.isPending || modelGroupsQuery.isPending

  return (
    <SettingsSection title={t('qy_gs_token_default_title')}>
      <p className='text-muted-foreground mb-4 text-sm'>
        {t('qy_gs_token_default_desc')}
      </p>

      {isLoading && (
        <p className='text-muted-foreground text-sm'>{t('Loading...')}</p>
      )}

      {!isLoading && userGroups.length === 0 && (
        <p className='text-muted-foreground text-sm'>
          {t('qy_gs_token_default_no_user_groups')}
        </p>
      )}

      {!isLoading && userGroups.length > 0 && (
        <div className='space-y-3'>
          {userGroups.map((userGroup) => (
            <div
              key={userGroup}
              className='flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'
            >
              <span className='min-w-0 truncate text-sm font-medium'>
                {userGroup}
              </span>
              <Select
                value={draft[userGroup] ?? NONE}
                // Base UI 的 onValueChange 签名是 `string | null`，null 出现在
                // 「清空选择」那条路径上。把它当成 NONE，而不是写进 draft ——
                // 后端拒绝空值，静默写进去的表现是保存时整页报错而看不出是哪一行。
                onValueChange={(value) => setOne(userGroup, value ?? NONE)}
              >
                <SelectTrigger className='w-full sm:w-72'>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE}>
                    {t('qy_gs_token_default_none')}
                  </SelectItem>
                  {/*
                    auto 单列一项：它不在 `/api/model-group/options` 里（那份清单
                    是 options.GroupRatio 的键），但它是一个合法的预选值 ——
                    想让某一档默认走自动分组，只能从这里选。
                  */}
                  <SelectItem value='auto'>
                    {t('qy_gs_token_default_auto')}
                  </SelectItem>
                  {modelGroups.map((modelGroup) => (
                    <SelectItem key={modelGroup} value={modelGroup}>
                      {modelGroup}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          ))}

          <div className='pt-2'>
            <Button onClick={onSave} disabled={isSaving}>
              {t('Save')}
            </Button>
          </div>
        </div>
      )}
    </SettingsSection>
  )
}
