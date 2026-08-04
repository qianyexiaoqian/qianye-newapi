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
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { useDebounce } from '@/hooks'
import { cn } from '@/lib/utils'

import { qyKeys } from '../../../lib/query-keys'
import { qyOpsErrorMessage } from '../../ops/errors'
import { qyPreviewGpRule } from '../api'
import { qyParseDecimal } from '../lib/pricing-math'
import type { QyGpMode, QyGpPreviewInput } from '../types'
import { QyGpEffectivePanel } from './effective-price'

/** 停止输入多久之后才去试算。太短会把每一次击键都变成一次请求。 */
const PREVIEW_DEBOUNCE_MS = 400

type QyGpEffectivePreviewProps = {
  groupName: string
  modelName: string
  mode: QyGpMode
  /** 用户正在敲的原始字符串，可能是半成品。 */
  value: string
  /**
   * 站在哪个**用户分组**的角度试算。空串 = 还没选。
   *
   * 后端必填，缺省直接 400。前端刻意不塞一个默认值蒙混过去：
   * 真实倍率的键是 (用户分组, 模型分组)，随便挑一个默认值得到的是一个
   * 「看起来精确」的错数字，而这一页存在的全部理由就是消灭那种数字。
   */
  userGroup: string
  shadowMode: boolean
}

/**
 * 录入时的实时折算。
 *
 * 折算走后端的 `POST /admin/group-pricing/preview`，前端不自己乘一遍 ——
 * 后端那一份是唯一会真正参与扣费的实现，前端再写一份就是第二个真相。
 * 而且只有后端读得到该模型当前的全局计费口径，能回答「这条规则配了会不会
 * 生效」这个前端凭一份模型名单根本判断不出来的问题。
 *
 * **正在计算时必须把结果压暗并标注**：面板上残留的是上一次输入的折算结果，
 * 让它看起来像是当前输入的答案，正是这一页最不能有的那种「看起来精确的错」。
 */
export function QyGpEffectivePreview(props: QyGpEffectivePreviewProps) {
  const { t } = useTranslation()

  // 用 useMemo 固定引用：useDebounce 的 effect 依赖的是值的引用，每次渲染都
  // 新建一个对象会让定时器被无限重置，防抖永远不会落地。
  const draft = useMemo<QyGpPreviewInput>(
    () => ({
      group_name: props.groupName,
      model_name: props.modelName.trim(),
      user_group: props.userGroup.trim(),
      mode: props.mode,
      value: props.value.trim(),
      // 试算是只读的，这两个字段后端不看，给固定值以免它们进 queryKey 造成
      // 无意义的重复请求。
      enabled: false,
      remark: '',
    }),
    [props.groupName, props.modelName, props.mode, props.userGroup, props.value]
  )
  const debounced = useDebounce(draft, PREVIEW_DEBOUNCE_MS)

  const ready =
    debounced.model_name !== '' &&
    debounced.user_group !== '' &&
    qyParseDecimal(debounced.value) != null

  const query = useQuery({
    queryKey: qyKeys.adminGroupPricingPreview(debounced),
    queryFn: () => qyPreviewGpRule(debounced),
    enabled: ready,
    staleTime: 60_000,
    // 试算失败最常见的原因是「值不合法」，那是 400，重试没有意义，
    // 只会让错误提示晚几秒才出现。
    retry: false,
  })

  if (!ready) {
    // 「还没选用户分组」与「还没填模型/值」是两种不同的未就绪，提示必须分开：
    // 前者是这一轮新增的必填项，用通用的「等待输入」文案会让人一直找不到
    // 到底缺了什么。
    const hint =
      debounced.user_group === ''
        ? 'qy_group_pricing_trial_user_group_required'
        : 'qy_gp_preview_idle'
    return (
      <p className='text-muted-foreground rounded-lg border border-dashed p-3 text-xs'>
        {t(hint)}
      </p>
    )
  }

  if (query.isError) {
    return (
      <p className='text-destructive rounded-lg border border-dashed p-3 text-xs'>
        {qyOpsErrorMessage(query.error, t)}
      </p>
    )
  }

  if (query.data == null) {
    return (
      <p className='text-muted-foreground rounded-lg border border-dashed p-3 text-xs'>
        {t('qy_gp_preview_loading')}
      </p>
    )
  }

  // 输入已经变了但新结果还没回来 —— 面板上的数字属于上一次输入。
  const stale = draft !== debounced || query.isFetching

  return (
    <div className='relative'>
      <QyGpEffectivePanel
        effective={query.data.effective}
        byUserGroup={query.data.effective_by_user_group}
        groupName={debounced.group_name}
        modelName={debounced.model_name}
        shadowMode={props.shadowMode}
        className={cn(stale && 'opacity-50')}
      />
      {stale && (
        <span className='bg-background text-muted-foreground absolute top-2 right-3 rounded px-1 text-xs'>
          {t('qy_gp_preview_loading')}
        </span>
      )}
    </div>
  )
}
