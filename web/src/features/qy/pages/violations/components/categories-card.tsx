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

import { Progress } from '@/components/ui/progress'

import {
  qyEnforcementActionKey,
  qyWindowIsUnlimited,
} from '../../../lib/violation-thresholds'
import type { QyMyViolationCategories } from '../types'

/**
 * 违规类型公示卡片。
 *
 * ── 它公示什么 ──
 * 有哪些违规类型、每一类累计多少次会被处置、以及**自己当前每一类的计数**。
 * 加上账号总量线那一条，用户就能自己回答"我还剩几次"。
 *
 * ── 它绝不公示什么 ──
 * 类型的内部名与内部说明。后端的 `userCategoryView` 白名单根本不下发它们
 * （内部说明写的就是匹配判据，公示它等于把绕过方法印给用户），所以这里也
 * **没有任何字段可以渲染它们** —— 前端不要试图从别的接口把这些补回来。
 *
 * ── 为什么两条线都要摆出来 ──
 * 用户会撞的线有两条，任一越过即触发处置。只显示其中一条会让"到底几次"在另一
 * 条线上失真，而失真的方向是"我以为还剩 5 次，结果第 3 次就被限制了"——
 * 那比不给数字更糟。
 *
 * ── 为什么是一整句话，不是一排徽章 ──
 * 项目方原话：「你违规了什么 XX 类型多少次，到多少次封号」。三个数字拆成
 * 标题 + 徽章 + 右侧小字之后，读者要自己把它们拼回一句话才知道自己什么处境 ——
 * 而这一页的读者正处在"我是不是要被封了"的状态。所以每一类就是一整句：
 * 类型名、我的次数、门槛、以及**到了会怎样**。
 *
 * ── "会怎样"必须跟着策略档走 ──
 * 类型阈值只决定"几次"，越线之后是仅记录 / 限制账号 / 封号，一律由用户所在
 * 分组的处置策略档决定（后端 `policy_action`）。写死"封号"会在「仅记录」档下
 * 变成一句吓人的假话，而在「封号」档下写"会被处理"又太轻。
 */
export function QyMyViolationCategoriesCard(props: {
  data: QyMyViolationCategories | undefined
}) {
  const { t } = useTranslation()
  const data = props.data
  if (data == null) return null
  // 站点一个类型都没公示时，**类型列表**收起 —— 一张只有标题没有内容的卡片
  // 只会让人以为页面坏了。
  //
  // 但账号总量线不能跟着一起收：account_threshold / account_hit_count /
  // account_window_hours 与 items 一点关系都没有，它们来自 resolveBanPolicy
  // 与全局计数器，而**未公示的类型照样计数、照样触发处置**
  // (后端 userCategoryLines 刻意不看 published)。
  //
  // 原先这里一并 return null，于是「不公示任何类型 + 设了账号总量门槛」这一种
  // 合法配置下，一个已经 9/10 的用户在这一页上看不到任何倒计时 ——
  // 三块统计已被移除，唯一剩下的预警渠道又被这行 guard 关掉了，
  // 而管理员在取消公示时不会有任何提示说明连账号线也被一起关掉了。
  const hasCategories = data.items.length > 0
  const hasAccountLine = data.account_threshold > 0
  if (!hasCategories && !hasAccountLine) return null

  // 越线之后会被怎样：仅记录 / 限制账号 / 封号。这一句在整张卡片里出现四次
  // （账号线、每一类的门槛句、还差几次、已达门槛），必须只解析一次 ——
  // 同一屏里两处说法不一致，用户会按最轻的那一句理解。
  const actionLabel = t(qyEnforcementActionKey(data.policy_action))

  return (
    <section className='space-y-3 rounded-lg border p-4'>
      <div className='space-y-1'>
        <h2 className='text-sm font-medium'>{t('qy_vio_cat_title')}</h2>
        <p className='text-muted-foreground text-xs'>{t('qy_vio_cat_desc')}</p>
      </div>

      {/* 账号总量线。它跨全部类型，与下面每一类各自的线是并列关系。
          窗口是「不限期限」时整句换一句说：原句里的「{{hours}} 小时内」在
          哨兵下会渲染成「-1 小时内」，而更糟的是——就算它渲染成 24，那也是
          一句**错的承诺**：用户会以为等一天就清零，而实际那些次数永远算数。 */}
      <div className='bg-muted/40 rounded-md px-3 py-2 text-xs'>
        {qyWindowIsUnlimited(data.account_window_hours)
          ? data.account_threshold > 0
            ? t('qy_vio_cat_account_line_unlimited', {
                hit: data.account_hit_count,
                threshold: data.account_threshold,
                action: actionLabel,
              })
            : t('qy_vio_cat_account_line_off_unlimited', {
                hit: data.account_hit_count,
              })
          : data.account_threshold > 0
            ? t('qy_vio_cat_account_line', {
                hit: data.account_hit_count,
                threshold: data.account_threshold,
                hours: data.account_window_hours,
                action: actionLabel,
              })
            : t('qy_vio_cat_account_line_off', {
                hit: data.account_hit_count,
                hours: data.account_window_hours,
              })}
      </div>

      {hasCategories && (
        <ul className='space-y-3'>
          {data.items.map((item) => {
            const progress =
              item.threshold > 0
                ? Math.min(100, (item.hit_count / item.threshold) * 100)
                : 0
            return (
              <li key={item.id} className='space-y-1'>
                {/* 项目方要的那一句话。threshold === 0 时**绝不**显示成"到 0 次封号"——
                  0 在后端的语义是"这一类不单独计门槛"，写成 0 会让用户以为
                  下一次违规就被封。 */}
                <p className='text-sm'>
                  {item.threshold > 0
                    ? t(
                        // 「不限期限」下那句「（24 小时内累计）」必须整句换掉，
                        // 不能只把数字换成 -1：留着一个时间口径就是留着一句假话。
                        qyWindowIsUnlimited(item.window_hours)
                          ? 'qy_vio_cat_sentence_unlimited'
                          : 'qy_vio_cat_sentence',
                        {
                          title: item.title,
                          hit: item.hit_count,
                          threshold: item.threshold,
                          hours: item.window_hours,
                          action: actionLabel,
                        }
                      )
                    : t('qy_vio_cat_sentence_off', {
                        title: item.title,
                        hit: item.hit_count,
                      })}
                </p>
                {item.threshold > 0 && (
                  <p
                    className={
                      // 剩余 1 次或已达门槛时标红：这是用户主动收敛行为的最后提醒。
                      item.remaining <= 1
                        ? 'text-destructive text-xs'
                        : 'text-muted-foreground text-xs'
                    }
                  >
                    {item.remaining > 0
                      ? t('qy_vio_cat_remaining', {
                          remaining: item.remaining,
                          action: actionLabel,
                        })
                      : // 「已达门槛，下一次违规即会按 X 处理」在**已经被处置之后**
                        // 是一句过期的预告：判定用的是"已达"而不是"恰好跨越"，
                        // 所以 hit == threshold 的那一刻账号就已经被封了。
                        // 此刻这个人最需要知道的是自己已经被封、该去申诉。
                        t(
                          data.banned === true
                            ? 'qy_vio_cat_remaining_done'
                            : 'qy_vio_cat_remaining_none',
                          { action: actionLabel }
                        )}
                  </p>
                )}
                {item.description !== '' && (
                  <p className='text-muted-foreground text-xs'>
                    {item.description}
                  </p>
                )}
                {item.threshold > 0 && (
                  <Progress value={progress} aria-label={item.title} />
                )}
              </li>
            )
          })}
        </ul>
      )}

      {/* 一个类型都没公示时，把「未公示的类型照样计数」这句话说出来。
          不说的话用户看到一张只有账号总量线的卡片，会以为站上没有别的门槛。 */}
      {!hasCategories && (
        <p className='text-muted-foreground text-xs'>
          {t('qy_vio_cat_none_published_note')}
        </p>
      )}

      {hasCategories && (
        <p className='text-muted-foreground text-xs'>
          {t('qy_vio_cat_any_line_note')}
        </p>
      )}
    </section>
  )
}
