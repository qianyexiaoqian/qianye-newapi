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
import { CloudOff, Clock, Ban, RefreshCw, TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'

import { formatQyDuration } from '../../ops/format'
import type { QyGmSavePartial, QyGmSnapshotInfo } from '../types'

type QyGmStatusBannersProps = {
  snapshot: QyGmSnapshotInfo
  /** 上一次保存只成功了一半。非空时常驻，直到下一次完整成功。 */
  partial: QyGmSavePartial | null
  /** 打开这一页时的哈希 vs 服务端最新哈希对不上 = 别处改过倍率。 */
  ratioDrift: boolean
  /** 权威清单里不含用户分组自己的那些行。 */
  selfExcluded: string[]
  /** 两个轴上仅大小写不同的名字。 */
  caseNearMiss: { left: string; right: string }[]
  /** 后端下发的、保存前应当被看见但**不拦截**的问题。 */
  warnings: string[]
  /**
   * **设了范围、却一个模型分组都没勾**的用户分组名。
   *
   * 这是这一页上唯一一条"有人正在被挡着"的待办：那一档的用户此刻无法把令牌指向
   * 任何模型分组。判据必须用**服务端下发的**状态而不是本地草稿 —— 草稿里勾了几个
   * 格子但还没保存时，线上那一档的人仍然一个都选不了，跟着草稿走会让提示在保存
   * 之前就消失，而那正是最需要它提醒"你还没按保存"的时刻。
   *
   * 与图例里那条「未设定范围的分组」刻意分家：那是一条**长期条件**（而且是正确的
   * 默认），这里是**待办**（配好就消失）。混在一起会让长期条件长在告警栏上，
   * 两周之内变成没人看的背景。
   */
  emptyScopeGroups: string[]
  onReload: () => void
}

/**
 * 页面顶部的三类前提横幅。它们回答的是「下面这张表现在到底算不算数」。
 *
 * 顺序不能反 —— 快照没加载时，下面所有的格子都还没有生效，先看那个。
 */
export function QyGmStatusBanners(props: QyGmStatusBannersProps) {
  const { t } = useTranslation()

  return (
    <div className='space-y-3'>
      {/*
        从未成功加载过快照 = 权威清单**一条都没生效**，全站仍按上游全局白名单
        放行，而这张表看起来配得好好的。这正是本仓反复出现的「写了但没接」，
        只是发生在运行期。必须是最醒目的一条。
      */}
      {!props.snapshot.loaded && (
        <Alert variant='destructive'>
          <CloudOff />
          <AlertTitle>{t('qy_group_matrix_snapshot_unloaded')}</AlertTitle>
          <AlertDescription>
            {t('qy_group_matrix_snapshot_unloaded_desc')}
          </AlertDescription>
        </Alert>
      )}

      {/*
        两库写入只成功了一半。**必须是常驻横幅而不是一闪而过的 toast**：
        半成状态会一直持续到有人再保存一次，而 toast 三秒后就没了，之后谁打开
        这一页都看不出线上正处在「倍率已生效、清单未生效」这种状态里。
      */}
      {props.partial != null && (
        <Alert variant='destructive'>
          <TriangleAlert />
          <AlertTitle>
            {props.partial.ratio_applied && !props.partial.membership_applied
              ? t('qy_group_matrix_save_partial_ratio_only')
              : t('qy_group_matrix_save_partial_list_only')}
          </AlertTitle>
          <AlertDescription>{props.partial.message}</AlertDescription>
        </Alert>
      )}

      {/*
        有用户分组设了范围、却一个模型分组都没勾。

        它排在快照与半成状态之后、其余提示之前，因为它是这一页上唯一一条
        **有人正在被挡着**的待办：那一档的用户此刻无法把令牌指向任何模型分组
        （enforce 下直接 403）。配好之后自动消失 —— 长期挂着的提示等于没有提示。

        不做成 destructive：空范围是**合法配置**（隔离组 / 封禁组 / 待配置组）。
        画成红色错误会让运营去查哪里坏了，而正确的动作要么是给它勾上模型分组，
        要么是确认"就是要一个都不给"。
      */}
      {props.emptyScopeGroups.length > 0 && (
        <Alert>
          <Ban />
          <AlertTitle>{t('qy_group_scope_empty_todo_title')}</AlertTitle>
          <AlertDescription>
            <span className='block'>{props.emptyScopeGroups.join('、')}</span>
            <span className='block'>{t('qy_group_scope_empty_todo_desc')}</span>
          </AlertDescription>
        </Alert>
      )}

      {/*
        陈旧但不丢弃：丢弃只有两个方向，回落上游（更宽松 —— 站里有 ratio=0 的
        模型分组，等于资金泄漏）或全拒（全站 403）。按「资金安全 > 可回退」排序，
        沿用最后一份成功快照是唯一正确解。所以这里是提示而不是错误。
      */}
      {props.snapshot.loaded && props.snapshot.stale === true && (
        <Alert>
          <Clock />
          <AlertTitle>{t('qy_group_matrix_snapshot_stale')}</AlertTitle>
          <AlertDescription>
            {t('qy_group_matrix_snapshot_stale_desc', {
              age: formatQyDuration(props.snapshot.age_seconds),
            })}
          </AlertDescription>
        </Alert>
      )}

      {/*
        上游「系统设置 → 计费与支付 → 模型分组定价」页改的是同一份 `GroupGroupRatio`，而那个入口
        **刻意不锁死**（扩展关掉之后运营必须还能在原地改倍率，这是回退能力的
        一部分）。所以只能检测并要求重新载入，不能默默用手上这份覆盖回去。
      */}
      {props.ratioDrift && (
        <Alert>
          <RefreshCw />
          <AlertTitle>{t('qy_group_matrix_ratio_drift_banner')}</AlertTitle>
          <AlertDescription>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={props.onReload}
            >
              {t('qy_group_matrix_ratio_reload')}
            </Button>
          </AlertDescription>
        </Alert>
      )}

      {/*
        范围里不含用户分组自己：这推翻了上游存在多年的不变量（`service/group.go`
        在差分算完之后无条件把 userGroup 补回去）。项目方明确要它能被推翻，
        所以是警告不是拦截。
      */}
      {props.selfExcluded.length > 0 && (
        <Alert>
          <TriangleAlert />
          <AlertTitle>{t('qy_group_matrix_conflict_self_excluded')}</AlertTitle>
          <AlertDescription>
            {props.selfExcluded.join('、')}
            <span className='block'>{t('qy_group_matrix_self_edge_hint')}</span>
          </AlertDescription>
        </Alert>
      )}

      {/*
        大小写近似：**只说，不折叠**。倍率侧是精确 map 查找且我们无权改，
        折叠成员资格会造出「管理端 0.3 / 实际按 1.0 扣」的新骗人数字。
      */}
      {props.caseNearMiss.length > 0 && (
        <Alert>
          <TriangleAlert />
          <AlertTitle>
            {t('qy_group_matrix_conflict_case_near_miss')}
          </AlertTitle>
          <AlertDescription>
            {props.caseNearMiss
              .map((pair) => `${pair.left} / ${pair.right}`)
              .join('、')}
          </AlertDescription>
        </Alert>
      )}

      {/*
        这里曾经有一条「影子期记录到的令牌写入拒绝」横幅。它随 shadow 档一起删掉：
        标题里的每一个词都在描述一个已经不存在的状态，而它的行动号召是「切
        enforce 之前先看看会挡掉谁」—— 现在没有 enforce 可切，勾完就生效，
        运营照着它去找的是一个不存在的开关。
      */}

      {/* 后端算出来的、保存前应当被看见但不拦截的问题（授权了空池子等）。 */}
      {props.warnings.length > 0 && (
        <Alert>
          <TriangleAlert />
          <AlertTitle>{t('qy_group_matrix_server_warnings')}</AlertTitle>
          <AlertDescription>
            <ul className='space-y-0.5 text-xs'>
              {props.warnings.map((warning) => (
                <li key={warning}>{warning}</li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}
    </div>
  )
}
