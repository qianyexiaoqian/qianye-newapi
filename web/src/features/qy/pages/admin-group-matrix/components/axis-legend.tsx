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
import { Link } from '@tanstack/react-router'
import { ArrowDown, ArrowRight, Boxes, Info, Lock, Users } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import type { QyGmNewGroupPolicy } from '../types'

/**
 * 两个轴的图例 —— 「用户分组 ≠ 模型分组」在界面上的**主要**承载处。
 *
 * ── 为什么需要它 ──
 *
 * 底层这两件事共用同一个字符串命名空间（`options.GroupRatio` 的键集合同时是
 * 用户分组清单与模型分组清单），数据上分不开，也不打算分开 —— 那是既定方案的
 * 已知代价。既然分不开，界面就必须把它**说清楚**：
 *
 *   · 行 = 用户分组：一个**人**属于哪一档，由管理员分配到 `users.group`；
 *   · 列 = 模型分组：一次**请求**打到哪一批上游渠道。
 *
 * ── 三重编码，不押在颜色上 ──
 *
 * 每个轴都同时有 **图标 + 方向箭头 + 文字标签**，而不是靠两种色块区分。
 * 只用颜色的话，主题切换（本站有多套主题预设）与色觉差异都会让这个区分消失，
 * 而它消失的后果不是"不好看"，是运营把一个同名的模型分组当成用户分组去配倍率。
 *
 * ── 为什么这一页不提供「新建用户分组」按钮 ──
 *
 * 因为新建一个用户分组 = 往 `options.GroupRatio` 里加一个 key，而那张表的 key
 * **同时**是模型分组清单。在这一页上放一个「新建用户分组」按钮，等于在一个专门
 * 用来区分两个概念的界面上，提供一个会同时创造出一行和一列的操作 —— 那会让
 * 本组件说的每一句话当场失效。所以这里只给一条指路链接，把创建留在它真正的
 * 归属地（上游「系统设置 → 分组倍率」），并在那里承担它的全部语义。
 */
export function QyGmAxisLegend(props: { newGroupPolicy: QyGmNewGroupPolicy }) {
  const { t } = useTranslation()

  return (
    <div className='bg-muted/30 space-y-2 rounded-lg border p-3'>
      <div className='grid gap-2 sm:grid-cols-2'>
        <QyGmAxisCard
          icon={<Users aria-hidden='true' className='size-4' />}
          direction={<ArrowDown aria-hidden='true' className='size-3' />}
          title={t('qy_group_matrix_axis_row_title')}
          hint={t('qy_group_matrix_axis_row_hint')}
        />
        <QyGmAxisCard
          icon={<Boxes aria-hidden='true' className='size-4' />}
          direction={<ArrowRight aria-hidden='true' className='size-3' />}
          title={t('qy_group_matrix_axis_col_title')}
          hint={t('qy_group_matrix_axis_col_hint')}
        />
      </div>

      {/*
        同名警告。这一条不是锦上添花：两个轴上真的可能出现同名项（本站的
        `default` 就同时是一档用户和一批渠道），而它们是**两件不同的事**。
        不说的话，运营会以为矩阵的对角线是"自己对自己"这种恒真的东西。
      */}
      <p className='text-muted-foreground flex items-start gap-1.5 text-xs'>
        <Info aria-hidden='true' className='mt-0.5 size-3 shrink-0' />
        <span>
          {t('qy_group_matrix_axis_same_name_note')}{' '}
          {t('qy_group_matrix_axis_create_hint')}{' '}
          <Link
            to='/system-settings/billing/$section'
            params={{ section: 'group-pricing' }}
            className='text-primary underline underline-offset-2'
          >
            {t('qy_group_matrix_axis_create_link')}
          </Link>
        </span>
      </p>

      {/*
        「新建的用户分组默认全遮断」的常驻状态。

        ── 为什么它必须出现在**这一页**，而且必须两种状态都说 ──

        这个默认改变的是运营**下一次在另一个页面上**建分组时会发生什么：在分组
        倍率表单里加一个 key，那一页不会提到遮断这回事，回到令牌页却发现这一档的
        人一个模型分组都选不了。唯一能让他在加之前读到这句话的地方就是这里。

        关掉时同样说一句：一个以为自己开着这个默认的运营，会在新分组上线时误以为
        它已经被遮断而不去检查 —— 沉默在两个方向上都是误导，只是方向相反。

        放在图例里而不是顶部告警栏：它是一条**长期为真**的条件，不是一次待办。
        长期挂在告警栏里的东西两周之内会变成没人看的背景。真正的待办
        （有分组被遮断了还没配）走 status-banners。
      */}
      <p className='text-muted-foreground flex items-start gap-1.5 text-xs'>
        <Lock aria-hidden='true' className='mt-0.5 size-3 shrink-0' />
        <span>
          {props.newGroupPolicy.enabled
            ? t('qy_group_matrix_new_group_policy_on', {
                seconds: props.newGroupPolicy.scan_interval_seconds,
              })
            : t('qy_group_matrix_new_group_policy_off')}
        </span>
      </p>
    </div>
  )
}

type QyGmAxisCardProps = {
  icon: React.ReactNode
  direction: React.ReactNode
  title: string
  hint: string
}

function QyGmAxisCard(props: QyGmAxisCardProps) {
  return (
    <div className='bg-background flex items-start gap-2 rounded-md border p-2'>
      <span className='text-muted-foreground mt-0.5 flex shrink-0 items-center gap-0.5'>
        {props.icon}
        {props.direction}
      </span>
      <div className='min-w-0'>
        <div className='text-xs font-medium'>{props.title}</div>
        <p className='text-muted-foreground text-xs'>{props.hint}</p>
      </div>
    </div>
  )
}
