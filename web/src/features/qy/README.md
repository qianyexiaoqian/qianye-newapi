# features/qy — 扩展前端骨架约定

本目录是 qy 扩展（推广与结算）的前端公共基建。**13 个功能页面由不同人并行开发，
本文件是它们之间的强制契约。**

## 1. 新增一个页面

| 动作         | 文件                                                                                   |
| ------------ | -------------------------------------------------------------------------------------- |
| 新建路由     | `src/routes/_authenticated/qy/<name>/index.tsx`（管理端 `qy/admin/<name>/index.tsx`）  |
| 新建业务目录 | `src/features/qy/<name>/{index.tsx,api.ts,types.ts,constants.ts,components/}`          |
| 加翻译       | `src/i18n/qy/en.json` + `zh.json`（**不碰 `src/i18n/locales/`**）                      |
| 加菜单       | `src/features/qy/nav.ts` 的 `WORKSPACE_PAGES` / `ADMIN_PAGES` 已预置常用页，缺的补一行 |
| 自动重写     | `src/routeTree.gen.ts`（`bun run build` 或 `bun run dev` 触发，禁止手改）              |

路由文件模板：

```tsx
import { createFileRoute } from '@tanstack/react-router'

import { QyTransferLogs } from '@/features/qy/transfer-logs'

export const Route = createFileRoute('/_authenticated/qy/transfer-logs/')({
  component: QyTransferLogs,
})
```

**叶子路由永远不写守卫。** 登录由 `_authenticated/route.tsx` 保证，扩展启用由
`qy/route.tsx` 保证，管理员由 `qy/admin/route.tsx` 保证，各一处、零重复。
需要超管门槛的页面（例如佣金费率）在自己的路由里加
`beforeLoad: requireQySuperAdmin`。

## 2. 取数

```ts
import { qyGet, qyPost } from '@/features/qy/lib/api'
import { qyKeys } from '@/features/qy/lib/query-keys'
import type { QyPage } from '@/features/qy/lib/types'

export function getTransferRecords(params: { p: number; page_size: number }) {
  // 路径不带 /api/qy 前缀，api.ts 会补
  return qyGet<QyPage<TransferRecord>>('/transfer/records', params)
}
```

- **禁止新建 axios 实例**，`lib/api.ts` 已复用 `@/lib/http-client` 的实例
  （withCredentials / Bearer 注入 / 401 刷新重试 / GET 在途去重全都继承）。
- 失败一律是 `QyError`，用 `error.kind` 分流、`qyErrorMessage(error, t)` 出文案。
  新增后端 code 时往 `QY_ERROR_CODE_I18N` 加一行并补两份 JSON。
- 所有 queryKey 必须来自 `qyKeys`，即以 `['qy', ...]` 开头。
- 资金类 mutation 成功后**必须**调用 `useQyAfterMoneyChange()`。

## 3. 显隐

任何扩展入口的渲染都要过 `useQyConfig()`：

```ts
const { enabled, available, features } = useQyConfig()
if (!enabled || !features.transfer) return null
```

扩展未启用时前端必须**零痕迹**：不出菜单、不弹 toast、不显示红色报错。

## 4. 共享组件

| 组件              | 路径                           | 用途                                    |
| ----------------- | ------------------------------ | --------------------------------------- |
| `QyPageBoundary`  | `components/qy-page-boundary`  | 加载 / 未启用 / 降级 / 错误 / 空态五态  |
| `QyStatusBadge`   | `components/qy-status-badge`   | 统一状态色板，`uncertain` 用告警色      |
| `QyAmountText`    | `components/qy-amount-text`    | 站内额度展示（ledger / hero 两种口径）  |
| `QyAmountInput`   | `components/qy-amount-input`   | 额度输入，自动回显换算后的整数 quota    |
| `QyMaskedUser`    | `components/qy-masked-user`    | 脱敏用户展示（脱敏由后端完成）          |
| `QyConfirmDialog` | `components/qy-confirm-dialog` | 二次确认，`irreversible` 强制勾选       |
| `QyTimeline`      | `components/qy-timeline`       | 单据状态时间线                          |
| `QyRouteError`    | `components/qy-route-error`    | 路由级错误边界（已挂在 `qy/route.tsx`） |

## 5. 红线

- **i18n**：键名 `qy_<domain>_<name>`，全小写下划线，**禁止点号**
  （i18next 默认 `keySeparator: '.'`，带点的键会被当成嵌套路径查不到）。
  domain 白名单：`nav common err tr aff wd cm vio avl cfg log plan`。
  传 `count` 的键不要自带 `_one` / `_other` 后缀。
- **禁止运行 `bun run i18n:sync`**：它只扫 `src/i18n/locales`，跑了只会给上游
  7 个大 JSON 制造噪声 diff。
- **法币金额**（提现）后端返回 string，前端只做展示与字符串比较，
  禁止 `parseFloat` 后再运算，更禁止用 `formatCurrencyFromUSD` /
  `formatQuotaWithCurrency` 渲染 —— 那是按冻结汇率产生的绝对值，会双重换算。
  站内额度才走 `lib/format.ts`。
- **前端永不本地加减余额**，提交后无条件 invalidate 重取。
- 审核类接口的请求体必须携带 `version`（乐观锁），409 时提示
  `qy_err_conflict` 并刷新列表。
- 任何 `.tsx` 只导出组件；工具函数 / 常量 / hook 放同目录的 `.ts`
  （`.oxlintrc.json` 的 `react/only-export-components` 有写死的白名单，
  改它意味着多动一个上游文件）。
