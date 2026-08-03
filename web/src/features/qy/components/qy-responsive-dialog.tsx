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
import type { ReactNode } from 'react'

import {
  Dialog as DialogRoot,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { useIsMobile } from '@/hooks/use-mobile'
import { cn } from '@/lib/utils'

/**
 * 三条布局类，桌面与移动两条分支共用 —— 「头尾固定、只有中间滚」这条规则
 * 在本文件里只有一份写法。
 *
 * `flex-1 min-h-0` 是这次修复的核心：正文占满头尾之外的**剩余**高度，
 * 剩多少算多少。改造前正文用的是 `max-h-[calc(100vh-14rem)]` —— 一个
 * 猜出来的 14rem「头 + 尾大概有这么高」。标题换行、说明变长、底部按钮换成
 * 两行，实际头尾就超过 14rem，正文的上限却纹丝不动，于是窗口整体撑破视口，
 * 下半截（截图里的动作 / 扣费 / 作用域）被裁掉且滚不到。
 * 没有 `min-h-0`，flex 子项的最小高度是内容高度，`overflow-y-auto` 永远
 * 不会触发，同样会把窗口顶破 —— 两个类少一个都不成立。
 */
const QY_DIALOG_STATIC_CLASS = 'shrink-0'
const QY_DIALOG_BODY_CLASS =
  'min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain'

export type QyResponsiveDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: ReactNode
  description?: ReactNode
  /**
   * 底部操作区。桌面端落在 `DialogFooter`、移动端落在 `SheetFooter`，
   * 两边都**不随正文滚动**。
   *
   * 表单的提交按钮要放在这里而不是正文末尾：正文是滚动区，按钮跟着滚出屏幕
   * 之后，用户在长表单里找不到"保存"。用 `<Button form={FORM_ID} type='submit'>`
   * 跨出 `<form>` 提交（HTML 原生的 form 属性），不需要把 form 包到外面。
   */
  footer?: ReactNode
  children: ReactNode
  /**
   * 桌面端窗口宽度上限，例如 `sm:max-w-xl`，缺省 `sm:max-w-2xl`。
   *
   * 只给宽度。**不要**在这里塞 `max-h-*` / `h-*`：高度由 `100dvh - 2rem`
   * 与「正文吃剩余空间」两条规则一起决定，写死一个高度会把这次修好的
   * 「头尾固定、中间滚」重新拆掉。
   */
  contentClassName?: string
  /** 移动端抽屉宽度上限，例如 `sm:max-w-md`。缺省用上游 Sheet 的 `sm:max-w-sm`。 */
  sheetClassName?: string
}

/**
 * qy 所有"内容/表单浮层"的统一外壳（需求 1）。
 *
 * ## 项目方要的口径
 *
 * 原话：「当前的弹窗你的方案是从底部伸出来，缺失了很多信息，你需要让他居中
 * 显示出一个完整的窗口，展示完整的内容，对于手机端的原项目从左边伸出这不挺好
 * 的？」拆成两条可执行的规则：
 *
 *   · **桌面端 = 居中的完整窗口**。宽度按内容给（默认 `sm:max-w-2xl`），
 *     高度顶到 `100dvh - 2rem`，正文自己滚。不是贴边的窄条。
 *   · **移动端 = 从侧边伸出的抽屉**，与上游移动端侧栏同一方向（左）。
 *     窄屏上居中窗口两侧的留白是浪费，而侧边抽屉是这个项目里用户已经熟悉的
 *     交互 —— 项目方点名说那样"挺好"。
 *
 * ## 三段式：头固定 / 正文滚 / 尾固定
 *
 * 项目方第二次反馈：「你看我发给你的截图，很多内容都想显示不出来不完全」——
 * 窗口已经居中了，但整体高于视口，动作 / 扣费 / 作用域那几段被裁在屏幕外。
 * 所以窗口自己 `flex flex-col` + `max-h`，头尾 `shrink-0`，正文
 * `flex-1 min-h-0 overflow-y-auto`：滚动条长在正文上，标题始终看得见，
 * 「保存 / 取消」始终够得着（长表单里按钮跟着正文滚走，用户会以为没有保存键）。
 *
 * 这里直接拼上游 `components/ui/dialog` 的原子件，而不是套 `components/dialog`
 * 的成品壳：后者的正文用 `max-h-[calc(100vh-14rem)]` 猜死了头尾高度，
 * 猜不准就是这次被投诉的裁切。那个壳是全站共用的，qy 不去改它，
 * 而是在 qy 自己的外壳里把高度算法换成 flex 剩余空间。
 *
 * ## 为什么是一个外壳而不是每个页面自己判
 *
 * 改造前 qy 有两种浮层各自为政：联系人簿走居中 `Dialog`，四张管理端表单走
 * `Sheet side='right'`（桌面上是一条 `max-w-xl` 的贴边窄板）。同一个产品里
 * "打开一个表单"出现两种形态，正是本仓反复出现的「同一概念的第 N 份拷贝」。
 * 收成一个外壳之后，"桌面居中 / 移动侧出 / 头尾固定"这套规则只有一处实现，
 * `__tests__/qy-responsive-dialog.test.tsx` 反向扫描 `features/qy` 下不再有
 * 第二处直接用 `SheetContent`、也不再有第二处直接开 `Dialog` 的地方。
 *
 * ## 断点取上游的 `useIsMobile`（768px）
 *
 * 不自己写 `matchMedia`：上游侧栏、日志筛选抽屉用的都是它，多一份阈值就会出现
 * "侧栏已经收起了、弹窗还当自己在桌面"的错位。它在首帧返回 `false`
 * （`useState<boolean|undefined>` → `!!undefined`），也就是说 SSR/首屏那一帧
 * 按桌面渲染。对本组件无影响：浮层只在用户点击后才 `open`，那时 effect 早已跑过。
 */
export function QyResponsiveDialog(props: QyResponsiveDialogProps) {
  const isMobile = useIsMobile()

  if (isMobile) {
    return (
      <Sheet open={props.open} onOpenChange={props.onOpenChange}>
        <SheetContent side='left' className={props.sheetClassName}>
          <SheetHeader className={QY_DIALOG_STATIC_CLASS}>
            {/* pr-8 给右上角的关闭按钮让位，标题不会压在它下面。 */}
            <SheetTitle className='pr-8'>{props.title}</SheetTitle>
            {props.description ? (
              <SheetDescription>{props.description}</SheetDescription>
            ) : null}
          </SheetHeader>
          <div
            data-slot='qy-dialog-body'
            className={cn(QY_DIALOG_BODY_CLASS, 'px-4 pb-4')}
          >
            {props.children}
          </div>
          {props.footer ? (
            <SheetFooter
              className={cn(
                QY_DIALOG_STATIC_CLASS,
                'flex-row justify-end gap-2 border-t'
              )}
            >
              {props.footer}
            </SheetFooter>
          ) : null}
        </SheetContent>
      </Sheet>
    )
  }

  return (
    <DialogRoot open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent
        className={cn(
          'flex max-h-[calc(100dvh-2rem)] w-full flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl',
          props.contentClassName
        )}
      >
        <DialogHeader
          className={cn(
            QY_DIALOG_STATIC_CLASS,
            // pr-12 给右上角的关闭按钮让位；窗口自身 p-0，内边距由头/正文/尾各自出。
            'px-4 pt-4 pr-12 pb-3 text-start sm:px-6 sm:pt-6 sm:pr-12'
          )}
        >
          <DialogTitle>{props.title}</DialogTitle>
          {props.description ? (
            <DialogDescription>{props.description}</DialogDescription>
          ) : null}
        </DialogHeader>

        <div
          data-slot='qy-dialog-body'
          className={cn(QY_DIALOG_BODY_CLASS, 'px-4 pb-4 sm:px-6')}
        >
          {props.children}
        </div>

        {props.footer ? (
          <DialogFooter
            className={cn(
              QY_DIALOG_STATIC_CLASS,
              // 抵消上游 footer 的 -mx-4/-mb-4：那是给 p-4 的窗口做出血用的，
              // 这里窗口是 p-0，再出血就会顶到圆角外面。
              'mx-0 mb-0 gap-2 p-4 sm:flex-row sm:justify-end sm:px-6'
            )}
          >
            {props.footer}
          </DialogFooter>
        ) : null}
      </DialogContent>
    </DialogRoot>
  )
}
