# Midnight Signal 主题设计规范

本文件是全站 UI 的**唯一现行口径**。参考稿已拷进仓库：`qianye/docs/ui-reference/`
（`DESIGN.md` 是文字规范，`tokens.json` / `variables.css` / `theme.css` 是抽取出来的
取值，`网站UI地址.txt` 是原始来源）。参考稿抽取自 dope.security，
一句话概括是 **midnight terminal with violet beacons**：近黑画布、一支高饱和色做信号灯、
无投影、发丝线分层、极宽字距的大写戳记做区段标识。

`design-10-steins-gate-theme.md` 与 `design-11-sg-hud-layer.md` **已作废**，
不要照它们改代码。

---

## 0. 三条已拍板的前提

1. **就地改写 `steins-gate` 预设，不新建。** 预设 slug `steins-gate`、CSS 类名前缀
   `qy-sg-`、三个文件名（`qy-sg-tokens.css` / `qy-sg-shape.css` / `qy-sg-apply.css`）
   全部沿用。它们是承重标识符：三个 CSS 的每一条选择器、`FIXED_THEME_PRESET`、
   `features/qy/hooks/use-qy-theme-preset.ts` 的 hook、以及四个测试都钉在这个名字上，
   改名的收益是零、成本是一次全仓改名。主题**对外的名字**改成 Midnight Signal
   （`THEME_PRESETS` 里那条的 `name`）。
2. **暗色为主 + 派生亮色。** 参考稿本身是 dark-only；顶栏的明暗开关保留，
   昼间那一套由夜间按 §1.3 的两条规则算出来，不引入第二套品牌色。
3. **全站。** 后台 + 未登录落地页都在范围内。落地页做到「去色 + 版式重做」这一层。

## 0.1 为什么这一版不是「换个调色板」

项目历史上因为「只换了颜色变量」被打回过两次（见已作废的 design-10 §0）。
参考稿给的是一整套视觉语言，它与默认主题的差别集中在**六件与颜色无关的事**上：

| 参考稿的东西 | 默认主题 |
|---|---|
| 极宽字距（0.2em）的大写等宽戳记做一切标注 | 普通无衬线小字 |
| 19.2px 圆角 + 发丝线 + 半透明洗色的票面，**零投影** | 8px 圆角 + 投影 |
| 表格无外框、无斑马纹、无单元格盒，只有横发丝线 | 整块框 + 分隔线 |
| 发丝线 + 节点圆点的航线带 | 一条普通 border |
| 品牌色只出现在「一处填充 + 两处辉光」 | 强调色遍地 |
| 坐标戳记式的收尾行（`+ FIELD …`） | 无 |

**验收标准不是「颜色对了」，而是「把颜色全部抹成灰度，只看轮廓，仍然认得出这不是
默认主题」。** 承载这条标准的就是 §2 的六条形状。

---

## 1. 色板

### 1.0 ⚠ 与参考稿刻意不同的一处：那一支是**浅蓝**不是紫

参考稿的 Signal Violet 是 `#af50ff`（色相角 305.1）。项目方看过实装之后判定
「这紫色有点丑」，改成**浅蓝，色相角 236**。

**这是审美裁定，不是漂移** —— 下一个读代码的人不要把它「修」回 305.1。
参考稿真正承重的是 §1.1 那条结构（一支高饱和色 + 95% 中性），不是那一支具体
是什么颜色；色相角一改，整套色板与 §2 的六条形状都不受影响，这恰恰是这套架构
值钱的地方。换色时连带挪了 `--info`（原来是 245 的蓝，与新品牌色只差 9°，
两支会糊在一起），见 §1.4。

### 1.1 一支色相

参考稿的原话是 *the palette is 95% achromatic and one violet*。本主题把它做成硬规则：

> **除了色相角 236 那一支蓝，全站中性档的彩度一律为 0。**

例外只有四个功能性语义色（destructive / success / warning / info）与借用它们的
两档图表色 —— 后台必须能把「失败 / 成功 / 警告 / 提示」四种状态区分开，
这是功能不是装饰，在规范里显式登记为**族外**，由测试按名单放行。

参考稿九个颜色的 oklch 换算（精确换算，不许目测取近似）：

| 名称 | 十六进制 | oklch | 角色 |
|---|---|---|---|
| Near Black | `#090909` | `oklch(0.14 0 305.1)` | 画布 / 夜间正文背景 / 昼间墨色 |
| Almost White | `#f7f9fa` | `oklch(0.981 0.003 228.8)` | 正文、图标、1px 边框 |
| Soft White | `#f0f0f0` | `oklch(0.955 0 305.1)` | 戳记文字，比正文暗一档做层次 |
| Steel | `#828384` | `oklch(0.609 0.002 247.9)` | 次级文字 |
| Graphite | `#474747` | `oklch(0.398 0 305.1)` | 分隔线、不透明边框 |
| Ash | `#6b6b6b` | `oklch(0.528 0 305.1)` | 低强度正文 |
| Iron | `#423738` | `oklch(0.349 0.016 11.7)` | 参考稿的洗色底，**本主题未采用**（它带 0.016 彩度，破坏「一支色相」） |
| Signal Violet | `#af50ff` | `oklch(0.634 0.248 305.1)` | 唯一的有彩声部 —— **本主题换成了浅蓝，见 §1.0** |
| Lavender Mist | `#e1bdff` | `oklch(0.851 0.098 305.1)` | 第二档，专门用来当**文字色** —— 同样换成蓝 |

品牌色换蓝之后，那两支实际取值是：

| 角色 | 夜 | 昼 |
|---|---|---|
| `--primary` | `oklch(0.76 0.14 236)` `#43befd` | `oklch(0.527 0.114 236)` `#0074a3` |
| `--qy-sg-mist` | `oklch(0.86 0.075 236)` `#a2d9fc` | `oklch(0.52 0.075 236)` `#3a708e` |

昼间那支是**深海蓝**而不是浅蓝，这是白底决定的：见 §1.3。

Almost White 的 `0.003 228.8` 是一丝冷调，肉眼不可辨；本主题把中性档统一到
彩度 0（因此纸面落在 `#f9f9f9` 而不是 `#f7f9fa`，单通道差 2/255），
换来的是「彩度为 0 即中性」这条一行就能说清、也一行就能测的规则。

**彩度全部贴着 sRGB 色域上界取。** 越界会被浏览器裁色，一旦裁色，离线算出来的
对比度就不作数了（算的是越界值，渲染的是裁过的值）。蓝在中等明度下的可用彩度
比紫低不少，所以每一支有彩色的彩度都是「该明度下 sRGB 允许的最大值再留 0.002
余量」反解出来的，不是随手写的整数。

### 1.2 面 = 墨色洗在画布上

参考稿的分层机制是原话里的 *elevation comes from hairline strokes and translucent
washes, never shadows*。所以本主题**不给每个面单独挑颜色**，而是同一支墨色的六档浓度：

```
面 = mix(墨色, 画布, p)      夜间墨色 = 纸白，昼间墨色 = 近黑
  sidebar   1.5%      card      3.0%      popover   4.5%
  muted     5.5%      secondary 7.0%      accent    9.0%
```

由此得到的两条契约：

- **方向**：洗色永远朝墨色走，所以六个面**夜间全部比画布亮、昼间全部比画布暗**。
  昼间的卡片比纸面暗一点点，这是刻意的 —— 它是同一条洗色公式的镜像，
  不是「卡片浮起来」那套投影语言。
- **单调**：`sidebar ≤ card < popover < muted < secondary < accent`，
  离画布越远表示层级越高。搞乱这个次序，hover 态就会往错误的方向走。

九档的浓度不是拍脑袋定的：`accent` 是 hover 底，`--muted-foreground` 压在它上面
必须仍有 4.5:1。9% 是**满足这条的上界**（12% 那一档实测昼间只有 4.12:1）。

### 1.3 昼间由夜间派生

两条规则，与旧版一致，只是换了色相：

- **规则一 · 色相角昼夜共用。** 蓝在两边都是 236，昼间只压明度（0.76 → 0.527），
  彩度不上涨。中性档两边都是彩度 0。谁再塞一个族外的强调色进来，测试直接红。
- **规则二 · 洗色方向昼夜镜像。** 见 §1.2。

昼间的蓝压到 `L=0.527`（`#0074a3`）不是挑好看的，是**反解出来的**：
`--primary` 会被当文字色用（上游一堆 `text-primary`），压在纸面上要 ≥4.5:1，
同时纸面色压在它上面（`bg-primary text-primary-foreground`）也要 ≥4.5:1 ——
这两条在同一个明度上同时取到 4.96:1。

所以昼间是一支**深海蓝**而不是浅蓝：浅蓝在白纸上根本读不出来，这是白底决定的，
不是选色失误。那支浅蓝在夜间（`#43befd`，压在虚空上 9.47:1）。

### 1.4 语义色

明度全部按「压在各自画布上 ≥4.5:1」反解。近黑画布余量很大，夜间统一取到 **7:1**：

| | 夜 | 昼 |
|---|---|---|
| destructive | `oklch(0.706 0.186 27)` `#ff685d` | `oklch(0.572 0.19 27)` `#d03833` |
| success | `oklch(0.664 0.175 152)` `#00b05a` | `oklch(0.533 0.141 152)` `#008341` |
| warning | `oklch(0.69 0.146 75)` `#cf8c00` | `oklch(0.554 0.116 75)` `#9a6703` |
| info | `oklch(0.671 0.112 200)` `#10aab1` | `oklch(0.538 0.092 200)` `#007e83` |

**`--info` 是随品牌换蓝一起挪走的。** 它原来在 245，与新品牌色 236 只差 9°，
两支放在一起完全分不出来。挪到 200（青）之后，四支语义色与品牌色两两相隔
36° 以上：destructive 27 → warning 75 → success 152 → info 200 → 品牌 236。

同一个理由，`--chart-3` 从 info 改取 warning 的橙：info 挪位之后离品牌蓝只有
36°，折线图上三条蓝绿色的线是分不出来的。

它们的 `-foreground` 一律取**画布色**（夜=近黑、昼=纸白），因此压在语义色上的字
夜间 7:1、昼间 4.6:1。

### 1.5 品牌色的配额（硬性）

参考稿：*Use Signal Violet only for one feature card glow, one filled action,
and one accent stroke per page — treat it as signal lighting, not theme color*，
以及 *Don't use Signal Violet for borders, text, or body backgrounds*。

落到本主题：

- **一处填充**：主按钮（`bg-primary`）。
- **两处辉光**：焦点环（`--qy-sg-glow`）与页头/落地页的径向辉光（`--qy-sg-bloom`），
  **`--qy-sg-bloom` 在挂载层的引用次数上限是 2**，由测试钉住。
- **一处强调笔画**：表格行 hover 时首格左侧那条 2px 竖线。
- **文字**用的是第二档（`--qy-sg-mist`），不是品牌色本身 ——
  参考稿给 Mist 的角色就是 *contrast-safe text*，换成蓝之后这条纪律照旧。

`--primary` 仍然保留 ≥4.5:1 的对比度下限，因为上游组件里有大量我们控制不了的
`text-primary`，那是兜底不是许可。

---

## 2. 六条签名形状

形状层（`qy-sg-shape.css`）只声明自定义属性、不写选择器；挂载层（`qy-sg-apply.css`）
一条形状一条规则、只写 `var()`。这条分层纪律与上一版相同，理由也相同：CSS 没有
mixin，属性集合抄第二遍就会各自漂移。

### ① 戳记排版

参考稿最强、也最便宜的一条：*the letterspaced breath is the heading style* /
*the tracking IS the design*。三档：

| 档 | 字体 | 字号 | 字距 | 用在 |
|---|---|---|---|---|
| stamp（区段大标题） | 无衬线 | `clamp(1.125rem, 2.2vw, 1.625rem)` | `.2em` | `.qy-sg-sec-title` |
| label（标注） | 等宽 | `10.5px` | `.2em` | 列头、徽章、侧栏分组、表单标签、键值行的键 |
| micro（丝印） | 等宽 | `10px` | `.3em` | 页面代号、统计副标 |

全部 `text-transform: uppercase`。

> ⚠ **CJK 陷阱**：区段大标题装的是中文页面标题，**不能用等宽轴**。
> `--qy-sg-mono` 的回退栈末端是 `monospace`，中文字形会掉进浏览器的通用等宽回退
> （Windows 上通常是 Courier New → 再回退到 SimSun），字形不受控。
> 规则是：**只有确定是纯拉丁的字符串才用等宽轴**（序号、页面代号、数字读数），
> 任何可能含中文的位置一律走 `--qy-sg-sans`，靠字距与大写拿效果。

数值另有一档：等宽 + `tabular-nums` + `.02em`，**不大写、不拉宽字距** ——
拉字距会破坏列对齐。

**参考稿的第三支字体刻意没有采用。** GrandSlang（意大利斜体衬线）在参考稿里只
出现在 88–146px 的 hero 大字上，替代品是 Lora Italic。但本站每一处展示级字串都是
中文：Lora 没有 CJK 字形，回退到 Noto Serif SC 之后 `font-style: italic` 只能由
浏览器合成倾斜，中文合成斜体是公认的坏排版。与其留一个没有消费方的字体轴
（本仓库最高频的缺陷形状），不如显式不采用并把理由写进 `qy-sg-tokens.css`。
剩下两支（几何无衬线 + 戳记等宽）已经足以承担识别度。

### ② 票面（Pass panel）

参考稿的 Hero Boarding Pass 与所有卡片共用的形态：

```
border-radius: 19.2px
border: 1px solid var(--qy-sg-hair-strong)
background: var(--qy-sg-wash)             /* 墨色 4% 的半透明洗色 */
box-shadow: none                          /* 全主题唯一允许的 box-shadow 取值 */
padding: clamp(1.25rem, 2.4vw, 1.875rem)  /* 参考稿是 40px，见下 */
```

内边距的上界取 30px 而不是参考稿的 40px：参考稿是落地页，一屏三张卡、每张只装
一段话；后台一张卡里可能是一张十列的表，40px 的左右留白会把可视区吃光。
30px 比上游的 16px 明显松，又不至于把表格挤出横向滚动条。

**纵横要分两条规则。** 上游 `Card` 只有 `py-4`，横向内边距在
`card-header / card-content / card-footer` 三个子块的 `px-4` 上（这样 `<img>`
作为直接子元素才能通铺到卡片边缘）。直接给 `Card` 写 `padding` 会与子块叠成双层
内边距，并把通铺图片顶出圆角。所以纵向给宿主、横向给子块，并用
`:is(div, header, footer)` 把 `<img>` 排除在外。

hover 的反馈是**线变亮**，不是块浮起 —— 同时要显式掐掉上游 `index.css` 给卡片的
`translateY(-1px)` 与投影，两套反馈叠在一起会抖。

消费方：`card` 与六种浮层（dialog / alert-dialog / sheet / drawer / popover /
dropdown-menu）。浮层另加 `backdrop-filter: blur(10px) saturate(1.05)`
（参考稿 Frosted Nav Bar 的那一档）。

> ⚠ **不要给自带 `fixed` 的浮层写 `position`**。dialog / alert-dialog / sheet /
> drawer 的定位是上游 Tailwind 的 `.fixed`（特异度 0,1,0），主题规则带 `body` 前缀
> 是 (0,2,1)，会赢下来把浮层打回文档流、掉到页面最下方。
> `__tests__/qy-sg-overlay-position.test.ts` 专门钉这一条，别绕过它。

### ③ 时刻表（Departure board）

参考稿没有表格，它的等价物是航班信息板的排版逻辑：无外框、无斑马纹、无单元格盒，
只有横向发丝线与戳记式列头。

```
table-container   无边框、无圆角、无底色
table             border-collapse: collapse
thead 行          上下各一条 hair-strong，本身不填色
table-head        戳记排版，Soft White（--secondary-foreground）
table-row         只有下发丝线
table-cell        等宽 + tabular-nums，纵向 padding 放松
row:hover         --qy-sg-wash 底 + 首格左侧 2px 品牌色竖线
```

那条竖线用 `background-image` 的线性渐变画，**不用 `border-left` 也不用
`box-shadow: inset`** —— 前者会让整行位移 2px，后者违反 §2②「零投影」。

### ④ 航线（Route line）

参考稿的比较区把四张卡片用一条横线 + 圆形节点连起来，原话是 *like a flight route*。
本主题把它做成一条可平铺的发丝线带：主线画在瓦片垂直中心，两个实心节点 + 一个空心节点。

用**内联 SVG 当遮罩**、颜色由 `background-color` 提供 —— `background-image` 里的
SVG 是独立文档，读不到宿主的自定义属性，把 stroke 写死成十六进制会让昼夜切换时
走线不跟随。

消费方：侧栏头部下沿、qy 页头下沿。做下边框时把整条带子的垂直中心对准元素下沿
（`bottom: calc(h / -2)`），主线就正好落在原 `border-bottom` 的位置。

### ⑤ 辉光（Signal bloom）

见 §1.5 的配额。径向辉光的配方放在 `--qy-sg-bloom`，挂载层最多引用两次。

### ⑥ 坐标戳记（Coordinate stamp）

参考稿的收尾签名：*End pages with a coordinate stamp footer* ——
左边一个 `+` 号，右边一行等宽小字。本主题两个消费方：

- `table-caption`：表格自己的铭牌。保持 `display: table-caption`
  （**不要改成 `inline-block`**：实测会让 `<caption>` 脱离 caption 盒、被塞进匿名
  表格行，铭牌渲染到表头与首行之间），戳记排版 + Mist 色 + 一条下发丝线，
  `::before` 出 `+`。
- `.qy-sg-readout`：分页条与概览栏那一行读数，等宽 `.12em`，同样带 `+`。

---

## 3. 控件形态

参考稿只给了两种按钮圆角，原话是 *the system uses 8px for control buttons and
1584px for pill CTAs — those are the only two button radii*，另有
smallControls 6px。落到本主题：

| | 圆角 | 其他 |
|---|---|---|
| 按钮 | **8px** | `letter-spacing: .02em`、`box-shadow: none` |
| 徽章 / 状态标签 | **胶囊** | 戳记排版 + 1px 边框 |
| 输入 / 文本域 / 选择器 | **6px** | hair-strong 边框 |
| 票面（卡片与浮层） | **19.2px** | 见 §2② |

注意这与上一版**正好相反**（上一版按钮胶囊、徽章直角）。这是刻意的：参考稿把胶囊
留给小标签，把方角控件留给按钮。

焦点环用 `outline`（`3px solid var(--qy-sg-glow)`）而不是 `box-shadow`，
并顺手把上游的 Tailwind ring 清零 —— 主题 CSS 是 `index.css` 顶层的无层导入，
无层恒胜 `@layer utilities`，两者同时渲染会出现双环。

---

## 3.5 落地页

未登录首页（`features/home`）是**上游文件**，一个 `data-slot` 都没有，通篇 Tailwind
工具类。改造仍然是 **CSS-only**、作用域是
`.min-h-svh:has(> section .landing-animate-fade-up)`，失效模式是 fail-open：
上游哪天改了类名，本节不再命中，落地页退回上游形态，不会白屏也不会错位。

能用**语义元素**（`section` / `h1` / `h2` / `footer`）的地方一律优先用语义元素，
它们比类名字符串稳得多。做的四件事：

1. **通铺画布** —— 去掉统计条那圈上下边框与底色，区段之间只靠 `--qy-sg-sec-gap`
   分隔（参考稿：*edge-to-edge dark bands with no visual dividers between them*）。
2. **区段头戳记化** —— 眉标转戳记档（等宽 0.2em 大写 + Mist 色），
   `h1`/`h2` 转参考稿的 display 排版：字重 300、字距收紧、字号放大。
3. **零投影 + 大圆角** —— 所有 `shadow-*` 清零，`rounded-xl/2xl` 统一到票面档。
4. **去色** —— 见下。

### 去色为什么分三档而不是一律压成一个颜色

首屏右侧的终端演示是**代码高亮**：Command / Flag / Key / String / Number 五个角色
靠颜色区分。全压成同一个颜色会让那一整块变成一坨看不出结构的字。所以文字分三档，
正好落在本主题已有的三级文字上：

| 档 | 取值 | 覆盖的色族 |
|---|---|---|
| 最亮（命令、成功态） | `--foreground` | green / emerald / teal / lime |
| 蓝味（标识符、键、标志） | `--qy-sg-mist` | blue / sky / cyan / indigo |
| 次级（字面量、强调） | `--muted-foreground` | red / orange / amber / yellow / violet / purple / fuchsia / pink / rose |

抹成灰度看仍然是「一段有层次的代码」，但整块只剩白 / 蓝 / 灰三个调子。
上游的蓝族落在带品牌味的那一档：换成蓝主题之后再让 `text-blue-*` 变灰，
读代码的人会以为是写错了。

**色族要列全 17 支**，不能只列页面上现在用到的那几支：上一版只列了
blue/violet/purple/emerald/amber，结果终端演示里的 `Key`（`text-sky-*`）漏网，
整屏留下一处孤零零的蓝。底色与边框同理，广谱那条排在实色块那条**之前**，
靠源序让少数几个实色块提回强调色。

验收用一段脚本反扫整棵 DOM：把每个元素的 `color` / `background-color` /
`border-color` 转成 HSL，色度 ≥26/255 且色相不在品牌族附近的一律算漏网。
昼夜两边都必须是 0。

---

## 4. 硬性约束

1. **全部规则限定在 `[data-theme-preset='steins-gate']` 作用域内。**
2. **上游组件源码一行不改。** 全靠 shadcn 的 `data-slot` 锚点挂进去，
   挂载面冻结在 **24 个 slot**，加一个都要先改测试。
3. **零投影。** 三个 CSS 文件里出现的每一条 `box-shadow` 声明，取值只能是 `none`。
   参考稿唯一允许的那一处阴影是导航条的 `rgba(16,24,40,.05) 0 1px 2px`，
   本主题没有接管导航条，因此实际用不到。
4. **不引入外部网络资源。** 字体走已安装的 fontsource 包（Public Sans 与
   JetBrains Mono，分别对应参考稿点名的替代字体 Inter·General Sans 与
   JetBrains Mono；第三支 Lora Italic 刻意不采用，见 §2①），
   图形用内联 SVG data-uri。
5. **颜色一律从标准 CSS 变量派生**，组件规则里不出现十六进制。
6. **`prefers-reduced-motion: reduce` 下停掉位移与过渡**，静态形态全部保留。
7. i18n：新增文案走 `useTranslation()`，key 放 `web/src/i18n/qy/{en,zh}.json`
   （**不是** `locales/`，且禁止跑 `bun run i18n:sync`）。
8. 遵循 `web/AGENTS.md` 的全部前端约定。

---

## 5. 文件划分与测试

```
web/src/styles/
  qy-steins-gate.css   汇总入口，只做三行 @import
  qy-sg-tokens.css     §1 色板 + 字体轴 + 主题私有派生量（取值的唯一来源）
  qy-sg-shape.css      §2 六条形状的配方，只声明自定义属性
  qy-sg-apply.css      把配方挂到 24 个 slot 与 qy 页面骨架上
```

三个契约测试：

- `__tests__/qy-sg-tokens.test.ts` —— 一支色相（彩度为 0 或色相角 236）、
  面的阶梯（方向 + 单调 + 昼夜对称）、取材锚点、昼夜覆写完整性、声明唯一性、
  **每个 `--qy-sg-*` 都要有 `var()` 消费方**、对比度 ≥4.5:1
- `__tests__/qy-sg-apply.test.ts` —— 三文件结构、24 个 slot 冻结、
  类名双向对账（≤20 个且零孤儿）、六条形状的落点、**零投影**、**辉光配额 ≤2**
- `__tests__/qy-sg-overlay-position.test.ts` —— 主题不得改写自带定位的浮层

---

## 6. 装饰文案

上一版的两处装饰文案是 Steins;Gate 口径（`LAB MEMO — 07` + 37 条日文副标），
本版按参考稿的登机牌语言整体换成大写英文戳记：

- 序号：`qy_sg_stamp_no` → `GATE 07`
- 页面代号：`qy_sg_code_*`（37 条）→ 该页功能的大写英文代号，如
  `WITHDRAW · SETTLEMENT`。两份语言内容相同 —— 它本来就是拉丁装饰字形，
  不是被翻译的对象。

代码侧的连带改名：`QyPageMeta.jpKey` → `codeKey`、`.qy-sg-jp` → `.qy-sg-code`。

---

## 7. 验收

- 昼夜各截一张图，与 `ui-reference/DESIGN.md` 的描述逐条对照
- **把截图抹成灰度**，六条形状仍要认得出来（这是 §0.1 的验收标准）
- 未登录落地页首屏：近黑通铺、辉光不超过两处、无投影
- `prefers-reduced-motion` 下无循环动画与位移
- `bun run typecheck`、受影响文件的 oxlint、`bun test`（对基线）、`bun run build`
