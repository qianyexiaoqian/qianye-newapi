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
import { ArrowDown, ArrowRight, Boxes, Globe, Info, Users } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

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
 * ── 「未设定范围」是一条长期条件，所以它归图例，不归告警栏 ──
 *
 * 长期挂在告警栏里的东西两周之内会变成没人看的背景。真正的待办（设了范围却
 * 一个模型分组都没勾）走 status-banners，那一条是会消失的。
 *
 * ── 为什么这一页不提供「新建用户分组」按钮 ──
 *
 * 因为新建一个用户分组 = 往 `options.GroupRatio` 里加一个 key，而那张表的 key
 * **同时**是模型分组清单。在这一页上放一个「新建用户分组」按钮，等于在一个专门
 * 用来区分两个概念的界面上，提供一个会同时创造出一行和一列的操作 —— 那会让
 * 本组件说的每一句话当场失效。所以这里只给一条指路链接，把创建留在它真正的
 * 归属地（上游「系统设置 → 计费与支付 → 模型分组定价」），并在那里承担它的全部语义。
 */
export function QyGmAxisLegend(props: { unscopedGroups: string[] }) {
  const { t } = useTranslation()
  // 见 `user-group-list.tsx` 同一处：`/system-settings` 的前端守卫要求超管，
  // 而本组件也渲染给普通管理员（role=10）。够不着的人不能拿到一条 403 链接。
  const canOpenGroupPricing =
    useAuthStore((state) => state.auth.user?.role) === ROLE.SUPER_ADMIN

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
          {canOpenGroupPricing ? (
            <Link
              to='/system-settings/billing/$section'
              params={{ section: 'group-pricing' }}
              className='text-primary underline underline-offset-2'
            >
              {t('qy_group_matrix_axis_create_link')}
            </Link>
          ) : (
            t('qy_group_matrix_axis_create_need_super')
          )}
        </span>
      </p>

      {/*
        「未设定范围的用户分组」常驻一栏。

        ── 为什么它必须常驻，而不是一次性通知 ──

        这一页的默认口径是「未设定范围 = 全部模型分组可用，各按兜底倍率」。它是
        正确的默认，但它同时意味着：运营在别的页面（上游「系统设置 → 计费与支付 → 模型分组定价」）
        新加一个用户分组时，那一档的人**立刻就能用全部模型分组**，而那一页不会
        提到这回事。

        一次性通知解决不了这个：新分组是在别处、在别的时刻被创建的。常驻列表则
        让「现在还有哪些分组没设范围」在他每次打开这一页时都摆在眼前，而且不需要
        任何后台任务去发现新分组。

        为空时同样渲染一句：一个以为"还有几个分组没配"的运营，需要看到"全部都配过了"
        这句话，而不是一片空白 —— 空白与"这一栏坏了"长得一样。
      */}
      <p className='text-muted-foreground flex items-start gap-1.5 text-xs'>
        <Globe aria-hidden='true' className='mt-0.5 size-3 shrink-0' />
        <span>
          {props.unscopedGroups.length === 0
            ? t('qy_group_scope_all_scoped_note')
            : t('qy_group_scope_unscoped_note', {
                groups: props.unscopedGroups.join('、'),
                count: props.unscopedGroups.length,
              })}
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
