# Steins Gate 主题设计规范

本文件是主题改造的**唯一口径来源**。参考稿:
`C:\Users\Administrator\Desktop\qianye\newapi-shujv\steins-gate-day-浅色.html`(昼)、
`steins-gate-night-深色.html`(夜)。

---

## 0. 这一版要解决的问题

上一版只做了两件事:换了一套颜色变量、给按钮加了 999px 圆角。项目方的原话是
**「所谓的增加 UI,只是在调色板里增加吗?我需要的是一个全新主题」** —— 这个判断是对的。
参考稿真正的东西是一整套视觉语言,而不是配色:

| 参考稿里有 | 上一版有没有 |
|---|---|
| 齿轮水印(120 秒转一圈,极淡) | ✗ |
| 区段头 = 等宽序号 `LAB MEMO — 01` + 衬线大标题 + 日文副标 | ✗(定义了工具类,零使用) |
| 编辑式编号行(`/ 01`,hover 整行右移) | ✗ |
| 竖排侧栏文字(`writing-mode: vertical-rl`) | ✗ |
| 变动率读数(等宽 + 宽字距 + 强调色数字) | ✗ |
| 发丝线分栏取代卡片阴影 | 部分 |
| 昼/夜**不同的**按钮语义 | ✗(两边都用强调色) |
| 胶片颗粒 | ✓ |
| 胶囊按钮 | ✓ |

**这一版的验收标准不是"颜色对了",而是"截图放在参考稿旁边,是同一套设计语言"。**

---

## 1. 色板

参考稿的十六进制原值。换算成 oklch 时必须**精确换算**,不许目测取近似值。

### 昼 (`[data-theme-preset='steins-gate']`)

```
--bg      #f6f1e6   纸面
--bg2     #efe8d9   次级纸面
--ink     #221c15   墨
--muted   #7a6f5f   次级文字
--faint   #a99e8b   三级文字/标注
--accent  #c2511f   锈橙(唯一强调色)
--accent-deep #9a3f16
--accent-soft rgba(194,81,31,.12)
--hair    rgba(34,28,21,.16)   发丝线
```

**混入 Claude 风格**(项目方要求)。Claude 的识别特征与本参考稿本来就同源(暖纸面 +
赭红强调 + 衬线标题),所以「混入」不是换色,而是借它三样东西:

1. **更松的垂直呼吸** —— 正文 `line-height: 1.9~2.05`,区段间距 `clamp(90px,11vw,140px)`。
   参考稿本身就是这个量级,照做即可,不要压缩成常规后台的紧凑排版。
2. **纸面而非卡片** —— 表面靠发丝线与极淡的渐变分层,不靠投影堆叠。
   昼间允许一层极轻的投影(`0 20px 50px rgba(34,28,21,.06)`),仅用于图表容器这类需要
   「浮起来」的块;其余一律 `box-shadow: none`。
3. **衬线标题 + 无衬线正文 + 等宽数据** 的三分工。

### 夜 (`.dark [data-theme-preset='steins-gate']`)

```
--bg      #0d0a09   暖近黑(不是纯黑 —— 纯黑会丢掉暖调)
--bg2     #151010
--ink     #f2ead9   米色墨
--muted   #a3968a
--faint   #655b51
--accent  #e5484d   红(锈橙在暗底上对比度不足)
--accent-soft rgba(229,72,77,.14)
--hair    rgba(242,234,217,.13)
```

### 与调色板定制器的关系(硬性)

项目方要求「这个主题也可以通过调色板调整颜色」。定制器(`config-drawer.tsx`)有五个轴:
preset / font / radius / scale / contentLayout,颜色通过 `[data-theme-preset]` 下的
CSS 变量生效。因此:

- **所有颜色一律从 `--background` / `--foreground` / `--primary` / `--border` /
  `--muted-foreground` 等标准变量派生**,派生用 `color-mix(in oklch, ...)`。
  绝不硬编码十六进制到组件规则里 —— 那会让定制器改了颜色而组件不跟随。
- 上面那张表定义的是这些标准变量的**取值**,不是组件规则里可以直接写的字面量。
- `--radius` 轴仍需生效于卡片、输入框、浮层。**唯一例外是按钮与徽章的胶囊形状** ——
  999px 是本主题的身份标识,不跟随 radius 轴,这一点要在代码注释里写明是刻意的。
- `--scale` 轴改的是根字号,本主题的所有尺寸用 `rem`/`em` 或 `clamp()`,不用 `px` 写死
  正文级尺寸(标注类的 10~12px 小字可以用 px,它们本就不该跟着缩放)。

---

## 2. 字体轴

```
--qy-sg-serif  衬线:标题、卡片标题、弹窗标题。走 var(--font-serif)(已含 CJK 回退栈)
--qy-sg-sans   无衬线:正文。走 var(--font-sans)
--qy-sg-mono   等宽:一切数据、标签、序号、读数。JetBrains Mono(已装 @fontsource-variable/jetbrains-mono)
```

`@fontsource-variable/jetbrains-mono` 已加入依赖,需要在 `index.css` 里 `@import`。

**分工铁律**:凡是「数字」「标识符」「状态标签」「列标题」,一律等宽 + 宽字距 + 大写。
这是参考稿最强的识别特征 —— 它让界面看起来像仪器面板而不是表格。

---

## 3. 组件语言

### 按钮 —— 昼夜**不同**,这是上一版最明显的错误

参考稿的 `.btn.solid`:

| | 昼 | 夜 |
|---|---|---|
| 背景 | `var(--ink)` **墨色**,不是强调色 | `var(--accent)` 红 |
| 文字 | `#fdfaf3` | `#fff` |
| 投影 | `0 10px 30px rgba(34,28,21,.22)` | `0 8px 30px rgba(229,72,77,.32)` |
| hover | `translateY(-2px)` + 背景转 `--accent-deep` + 投影转赭色 | `translateY(-2px)` + 投影加强 |

昼间主按钮是**墨色**的,强调色只在 hover 时才出现 —— 这是参考稿克制感的来源。
上一版两边都用强调色,把昼间的层次做没了。

共同点:`border-radius: 999px`、`padding: 14px 30px`、`letter-spacing: .08em`、
`transition: all .28s cubic-bezier(.16,1,.3,1)`。

`.btn.line`(对应 outline/ghost):发丝线边框 + 半透明底,hover 时边框加深、底色微亮,
**不做位移**(位移是实心按钮的特权)。

带箭头的按钮:`.arr { transition: transform .28s }`,hover 时 `translateX(4px)`。

### 表格 —— 仪表面板,不是电子表格

参考稿没有传统表格,它的等价物是**编辑式编号行**:

```
grid-template-columns: 90px 1fr 1.15fr;
每行 border-top: 1px solid var(--hair);  最后一行补 border-bottom
hover 时整行 padding-left: 14px(0.3s cubic-bezier(.16,1,.3,1))
序号列 .idx 等宽 + .2em 字距,hover 时从 --faint 变 --accent
```

把这套语言迁移到真表格上:

- `table-head`:等宽、11px、`letter-spacing: .18em`、大写、`--muted-foreground` 色,
  下边框用 `--hair-strong`
- `table-row`:只有下发丝线,**没有斑马纹、没有卡片包裹**
- `table-row:hover`:强调色 6% 叠加 + **左移一条 2px 强调色竖线**(用 `::before`),
  呼应参考稿的整行位移
- `table-cell`:`font-variant-numeric: tabular-nums`
- 首列若是序号/ID:等宽 + `--faint` 色

### 卡片 —— 发丝线 + 极淡顶部渐变,不堆阴影

```
border: 1px solid var(--hair);
border-radius: 14px;
box-shadow: none;                    /* 夜间 */
box-shadow: 0 20px 50px rgba(ink,.06); /* 昼间,且仅限需要浮起的容器 */
background-image: linear-gradient(180deg, color-mix(in oklch, var(--foreground) 2.5%, transparent), transparent);
```

`card-title` 用衬线 700,`letter-spacing: .03em`。

### 区段头(新增,参考稿的核心构图)

```
.qy-sg-sec-head {
  display:flex; align-items:baseline; justify-content:space-between;
  margin-bottom: clamp(44px,6vw,68px);
}
.no  等宽 12px / .3em 字距 / --accent 色     →  "LAB MEMO — 01"
h2   衬线 800 / clamp(26px,3.4vw,40px) / .04em
.jp  等宽 11px / .4em 字距 / --faint 色       →  "未来道具 · 機能一覧"
```

**这个必须真的被 qy 的页面用起来**,不能又变成一组零使用的工具类 ——
「定义了没有消费方」是本项目累计出现十几次的头号缺陷形状。

### 徽章 / 状态标签

胶囊、等宽、11.5px、`letter-spacing: .16em`、大写。
带状态点的(参考稿 `.badge i`):6px 圆点 + 强调色辉光 + `pulse 2s` 呼吸动画。

### 输入控件

`border-radius: 8px`(跟随 `--radius`)、发丝线边框、focus 时 `0 0 0 3px` 强调色柔光环。
数值输入用等宽。

### 浮层(dialog / popover / dropdown / sheet / drawer / tooltip)

发丝线边框 + `backdrop-filter: blur(16px) saturate(1.1)`,标题衬线 700。
遮罩层昼间 `rgba(34,28,21,.35)`、夜间 `rgba(13,10,9,.6)`,都带轻微模糊。

### 侧栏 / 顶栏

顶栏:半透明底 + `blur(16px) saturate(1.1)` + 下发丝线,**去掉投影**。
侧栏分组标题:等宽 10.5px / `.22em` 字距 / 大写。
选中项:左侧 2px 强调色竖线(`::before`),不做整块填充。

---

## 4. 装饰层

### 胶片/纸张颗粒

`body::after`,`position: fixed; inset: -50%; z-index: 70; pointer-events: none`,
昼 `opacity: .05`、夜 `.055`,内联 SVG `feTurbulence` 噪声,
`animation: grainShift 1.2s steps(3) infinite`(位移 ±2%)。
`prefers-reduced-motion: reduce` 下停掉动画但保留颗粒。

### 齿轮水印(缺失,必须补)

参考稿在左上角放了一个 520×520 的齿轮 SVG,`opacity: .045`,`animation: spin 120s linear infinite`。
用 `body::before` 实现(内联 SVG data-uri),不往 DOM 插节点 —— 插节点要改上游组件。
`z-index: 0`、`pointer-events: none`、`position: fixed`。
同样受 `prefers-reduced-motion` 约束。

齿轮路径(参考稿原值):
```
M50 8l4 8a34 34 0 0 1 12 5l9-3 5 9-6 7a34 34 0 0 1 3 13l9 3v10l-9 3a34 34 0 0 1-3 13l6 7-5 9-9-3a34 34 0 0 1-12 5l-4 8H46l-4-8a34 34 0 0 1-12-5l-9 3-5-9 6-7a34 34 0 0 1-3-13l-9-3V50l9-3a34 34 0 0 1 3-13l-6-7 5-9 9 3a34 34 0 0 1 12-5l4-8z M50 32a18 18 0 1 0 0 36 18 18 0 0 0 0-36z
```

### 选中文本

昼 `::selection { background: rgba(194,81,31,.25) }`,夜 `rgba(229,72,77,.35); color:#fff`。

### 滚动条

细(8px)、无按钮、滑块用 `--hair-strong`、hover 转强调色 40%。

---

## 5. 硬性约束

1. **全部规则限定在 `[data-theme-preset='steins-gate']` 作用域内。** 切到任何其他主题
   必须完全不受影响 —— 这是「主题可切换」的前提,也是不污染上游的前提。
2. **上游组件源码一行不改。** 全靠 shadcn 的 `data-slot` 锚点挂进去(全仓约 200 个 slot,
   见 `grep -rho 'data-slot=...' web/src/components/ui/`)。
3. **不引入外部网络资源。** 字体走已安装的 fontsource 包,图形用内联 SVG data-uri。
   参考稿里的 Google Fonts `<link>` 不能照搬。
4. **颜色一律从标准 CSS 变量派生**(见 §1 最后一段),否则调色板定制器会失效。
5. **`prefers-reduced-motion: reduce` 下停掉所有循环动画**(颗粒抖动、齿轮旋转、
   徽章呼吸),但保留静态效果。
6. i18n:新增的用户可见文案走 `useTranslation()`,key 放 `web/src/i18n/qy/{en,zh}.json`
   (**不是** `locales/` —— 见该目录 `index.ts` 顶部的说明,qy 的键独立存放且禁止跑
   `bun run i18n:sync`)。
7. 遵循 `web/AGENTS.md` 的全部前端约定。

---

## 6. 文件划分

```
web/src/styles/
  qy-sg-tokens.css      §1 色板 + §2 字体轴(昼/夜两套 CSS 变量)
  qy-sg-decor.css       §4 装饰层(颗粒、齿轮、选中、滚动条、焦点环)
  qy-sg-controls.css    按钮、输入、选择、开关、复选、单选、滑块、切换、field、kbd
  qy-sg-surfaces.css    卡片、弹窗、抽屉、浮层、菜单、提示、命令面板、折叠、警告条
  qy-sg-data.css        表格、分页、徽章、头像、标签页、进度、骨架、空态、图表、分隔
  qy-sg-nav.css         侧栏、顶栏、导航菜单、面包屑、按钮组、切换组
  qy-sg-editorial.css   §3 区段头、编号行、读数、竖排侧标等构图工具类
```

`qy-steins-gate.css` 保留为**汇总入口**(只做 `@import`),这样 `index.css` 那一行不用动。
`qy-theme-presets.css` 里 steins-gate 的颜色块迁到 `qy-sg-tokens.css`,该文件只留其余预设。

---

## 7. 验收

- 昼夜各截一张图,与参考稿并排 —— 是不是同一套设计语言
- 切到其他任意预设,页面必须与改造前逐像素一致(作用域隔离)
- 调色板五个轴逐个动一遍,主题跟随(按钮/徽章的胶囊形状除外,那是刻意的)
- `prefers-reduced-motion` 下无循环动画
- `bun run build` + `bun run typecheck` + oxlint 通过
