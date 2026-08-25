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
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

/**
 * 一格输入框底下的「能填多少 / 建议填多少 / 现在哪里不对」。
 *
 * ## 为什么这三件事必须同时在，而且是这个顺序
 *
 * 项目方原话：「创建活动，你不告诉我要怎么设置推荐值？一堆这种『固定奖级的
 * 预算（额度 × 份数）必须不小于全场参与上限，否则超募时会有中奖者被摊薄到 0
 * 而拿不到钱』很烦啊」
 *
 * 那句报错通篇在解释**为什么会出错**，而运营要的是**我该填多少**。两者都要有，
 * 顺序反过来：
 *
 *   1. `ranges` —— 这一格能填的范围。系统上界（代码写死，改不了）与策略上限
 *      （站点配的，可以改）**分成两行**，绝不合成一句：合成之后运营分不出
 *      是自己配错了还是系统不支持，于是会跑去配置页找一个不存在的开关。
 *   2. `advice` —— 推荐填多少。能由其它字段算出来的（`lib/advice.ts`）就顺手
 *      给一颗按钮直接填进去，而不是等他填错了再报错。
 *   3. `problem` —— 现在这一格哪里不对。它与提交时那份跨步校验**同源**
 *      （都走 `lib/advice.ts` 的判据函数），所以不会出现"字段旁边全绿、
 *      点提交被顶回来"。
 *
 * 前两条是常驻的静态文字，第三条只在真的不对时出现 —— 一格从头到尾都红着的
 * 输入框会让人学会无视它。
 */
export function QyLotFieldAdvice(props: {
  /** 允许范围，一行一条。系统上界与策略上限分行。 */
  ranges?: string[]
  /** 推荐值那一句。 */
  advice?: string
  /** 给了就多一颗按钮，点一下把推荐值填进去。 */
  onApply?: () => void
  applyLabel?: string
  /** 实时校验命中时的那一句。与提交校验同源。 */
  problem?: string
}) {
  const { t } = useTranslation()
  const ranges = props.ranges?.filter((line) => line !== '') ?? []

  return (
    <div className='space-y-0.5'>
      {ranges.map((line) => (
        <p key={line} className='text-muted-foreground text-xs'>
          {line}
        </p>
      ))}
      {props.advice != null && props.advice !== '' && (
        <p className='text-muted-foreground flex flex-wrap items-center gap-2 text-xs'>
          <span>{props.advice}</span>
          {props.onApply != null && (
            <Button
              type='button'
              variant='outline'
              size='sm'
              className='h-6 px-2 text-xs'
              onClick={props.onApply}
            >
              {props.applyLabel ?? t('qy_lot_advice_apply')}
            </Button>
          )}
        </p>
      )}
      {props.problem != null && props.problem !== '' && (
        <p className='text-destructive text-xs'>{props.problem}</p>
      )}
    </div>
  )
}
