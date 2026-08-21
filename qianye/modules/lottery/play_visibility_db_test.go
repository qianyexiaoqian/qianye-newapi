package lottery

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// play_visibility_db_test.go —— 按玩法显示/隐藏。
//
// 守的是三件事,每一件都对应一种在这个仓库真实发生过的失败:
//
//  1. 零值。库里一行配置都没有时必须是**全部显示**。反过来(缺一段配置就整块
//     不可见)会让升级到本版本的站点在没人动过任何东西的情况下,整个娱乐功能
//     静默消失,而没有任何一处报错。
//  2. 过滤真的落在 SQL 上。"关掉双色球"落不到 kind 这一维上,所以只能在
//     WHERE 里过滤;断言"源码里写了这个开关"在过滤被忽略时同样为真。
//  3. 隐藏**不碰钱**。已参与的人查票、领奖,以及封盘/开奖/派奖/退款/收尾,
//     一行都不许读这个开关 —— 藏掉入口而活动还在跑,是把钱悬在半空。

// allPlaysShown 是"没有任何隐藏"的基线,等于零值口径。
//
// 刻意不从 baseSettings 回读:那是被测代码。这里手写四个 true,
// 于是把默认值从"显示"改成"隐藏"时,依赖它的大厅分区用例会立刻变红。
func allPlaysShown() opSettings {
	return opSettings{
		ShowPlayDrawRank: true,
		ShowPlayDrawProb: true,
		ShowPlayDrawBall: true,
		ShowPlayGuess:    true,
	}
}

// TestPlayOfClassifiesEveryActivityShape 锁住 (kind, draw_mode) → 玩法 的投影。
//
// 空串那一行是重点:normalizeDrawMode 把空串当 rank 收下,所以库里确实有
// draw_mode 为空串的抽奖行。少了它,老活动会在"按名次"开着的时候从大厅消失。
func TestPlayOfClassifiesEveryActivityShape(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		drawMode string
		want     string
	}{
		{name: "按名次", kind: KindDraw, drawMode: DrawModeRank, want: PlayDrawRank},
		{name: "存量行的空 draw_mode 也是按名次", kind: KindDraw, drawMode: "", want: PlayDrawRank},
		{name: "概率", kind: KindDraw, drawMode: DrawModeProb, want: PlayDrawProb},
		{name: "双色球", kind: KindDraw, drawMode: DrawModeBall, want: PlayDrawBall},
		{name: "竞猜", kind: KindGuess, drawMode: "", want: PlayGuess},
		{
			// 竞猜行上不该有 draw_mode,但真出现了也必须归到竞猜:
			// 归到抽奖会让"只关抽奖"把一场竞猜一起藏掉。
			name: "竞猜行上的脏 draw_mode 仍归竞猜",
			kind: KindGuess, drawMode: DrawModeBall, want: PlayGuess,
		},
		{
			// 日后新增的定档方式漏登记时,跟着 rank 走(可能多显示一格),
			// 而不是永久不可见。
			name: "未登记的定档方式回落按名次",
			kind: KindDraw, drawMode: "roulette", want: PlayDrawRank,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, playOf(tc.kind, tc.drawMode))
		})
	}
}

// TestPlayVisibilityDefaultsToShownAndFollowsOverrides 锁住零值与覆盖口径。
//
// 期望值全部手写,不从 baseSettings / mergeOverrides 回读。
func TestPlayVisibilityDefaultsToShownAndFollowsOverrides(t *testing.T) {
	cases := []struct {
		name string
		rows map[string]string
		want map[string]bool
	}{
		{
			name: "一行都没配:四种玩法全部显示",
			rows: map[string]string{},
			want: map[string]bool{
				PlayDrawRank: true, PlayDrawProb: true,
				PlayDrawBall: true, PlayGuess: true,
			},
		},
		{
			name: "只显示竞猜(项目方原话里的那种配法)",
			rows: map[string]string{
				keyShowPlayDrawRank: "0",
				keyShowPlayDrawProb: "0",
				keyShowPlayDrawBall: "0",
				keyShowPlayGuess:    "1",
			},
			want: map[string]bool{
				PlayDrawRank: false, PlayDrawProb: false,
				PlayDrawBall: false, PlayGuess: true,
			},
		},
		{
			name: "只关双色球:其余三种不受影响",
			rows: map[string]string{keyShowPlayDrawBall: "0"},
			want: map[string]bool{
				PlayDrawRank: true, PlayDrawProb: true,
				PlayDrawBall: false, PlayGuess: true,
			},
		},
		{
			name: "true/false 的历史写法同样认",
			rows: map[string]string{keyShowPlayGuess: "false"},
			want: map[string]bool{
				PlayDrawRank: true, PlayDrawProb: true,
				PlayDrawBall: true, PlayGuess: false,
			},
		},
		{
			// 读不懂的行丢弃并回落"显示"。一行手改坏的配置不该让一整种玩法
			// 从站点上消失 —— 那种消失没有任何一处会报错。
			name: "读不懂的取值回落到显示",
			rows: map[string]string{keyShowPlayDrawProb: "maybe"},
			want: map[string]bool{
				PlayDrawRank: true, PlayDrawProb: true,
				PlayDrawBall: true, PlayGuess: true,
			},
		},
		{
			name: "四个全关",
			rows: map[string]string{
				keyShowPlayDrawRank: "0", keyShowPlayDrawProb: "0",
				keyShowPlayDrawBall: "0", keyShowPlayGuess: "0",
			},
			want: map[string]bool{
				PlayDrawRank: false, PlayDrawProb: false,
				PlayDrawBall: false, PlayGuess: false,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withLotteryConfig(t, config.Lottery{
				MaxActiveActivities: 10, MaxGuessFeeBps: 500, MaxTotalPrizeQuota: 50_000_000,
			})
			got := mergeOverrides(baseSettings(config.Get().Lottery), tc.rows)

			for play, want := range tc.want {
				assert.Equalf(t, want, got.playShown(play), "玩法 %s 的可见性", play)
			}
			// anyPlayShown 决定前端还渲不渲染那一行导航。
			wantAny := false
			for _, v := range tc.want {
				wantAny = wantAny || v
			}
			assert.Equal(t, wantAny, got.anyPlayShown())
			assert.Equal(t, tc.want, got.playVisibilityMap())
		})
	}
}

// playAct 造一行"能出现在大厅里"的活动:已发布、未下架。
func playAct(actNo, kind, drawMode string) *Activity {
	return &Activity{
		ActNo: actNo, Kind: kind, DrawMode: drawMode,
		Status: StatusPublished, Title: actNo, StakeQuota: 1000,
		CloseAt: 100, DrawAt: 200, Algo: AlgoV1,
	}
}

// TestHallQueryDropsHiddenPlays 是这一整条改动的正面:开关必须真的落到 WHERE 上。
//
// 五行覆盖全部四种玩法 + 存量空 draw_mode,另加草稿与已下架各一行(证明原有
// 口径没被新条件用 OR 挤开)。期望活动号在用例里手写。
func TestHallQueryDropsHiddenPlays(t *testing.T) {
	seed := []*Activity{
		playAct("P-rank", KindDraw, DrawModeRank),
		playAct("P-legacy", KindDraw, ""),
		playAct("P-prob", KindDraw, DrawModeProb),
		playAct("P-ball", KindDraw, DrawModeBall),
		playAct("P-guess", KindGuess, ""),
	}

	cases := []struct {
		name string
		set  opSettings
		kind string
		want []string
	}{
		{
			name: "四个都开:全部下发",
			set:  allPlaysShown(),
			want: []string{"P-rank", "P-legacy", "P-prob", "P-ball", "P-guess"},
		},
		{
			name: "只显示竞猜",
			set:  opSettings{ShowPlayGuess: true},
			want: []string{"P-guess"},
		},
		{
			name: "只显示抽奖(三种定档方式全开)",
			set: opSettings{
				ShowPlayDrawRank: true, ShowPlayDrawProb: true, ShowPlayDrawBall: true,
			},
			want: []string{"P-rank", "P-legacy", "P-prob", "P-ball"},
		},
		{
			// 这一条是 kind 这一维过滤不出来的那种:双色球与普通抽奖同为 draw。
			name: "只关双色球",
			set: opSettings{
				ShowPlayDrawRank: true, ShowPlayDrawProb: true, ShowPlayGuess: true,
			},
			want: []string{"P-rank", "P-legacy", "P-prob", "P-guess"},
		},
		{
			// 空 draw_mode 的存量行必须跟着"按名次"走。
			name: "只显示按名次:存量空 draw_mode 一起留下",
			set:  opSettings{ShowPlayDrawRank: true},
			want: []string{"P-rank", "P-legacy"},
		},
		{
			name: "只显示概率:按名次与存量行一起消失",
			set:  opSettings{ShowPlayDrawProb: true},
			want: []string{"P-prob"},
		},
		{
			name: "只显示双色球",
			set:  opSettings{ShowPlayDrawBall: true},
			want: []string{"P-ball"},
		},
		{
			name: "四个全关:大厅为空,而不是回落成全量",
			set:  opSettings{},
			want: []string{},
		},
		{
			// 玩法过滤与 kind 参数是"且",不是互相覆盖。
			name: "kind=draw 与只开竞猜同时成立时为空",
			set:  opSettings{ShowPlayGuess: true},
			kind: KindDraw,
			want: []string{},
		},
		{
			name: "kind=draw 叠加只关双色球",
			set: opSettings{
				ShowPlayDrawRank: true, ShowPlayDrawProb: true, ShowPlayGuess: true,
			},
			kind: KindDraw,
			want: []string{"P-rank", "P-legacy", "P-prob"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newHallTestDB(t)
			for _, a := range seed {
				row := *a
				require.NoError(t, gdb.Create(&row).Error)
			}
			// 草稿与已下架的场次永不下发。它们与新条件是 AND 关系,
			// 少一层括号就会被 OR 漏出来 —— 所以每个用例都带着它们跑。
			draft := playAct("P-draft", KindDraw, DrawModeRank)
			draft.Status = StatusDraft
			require.NoError(t, gdb.Create(draft).Error)
			retired := playAct("P-hidden", KindGuess, "")
			retired.HiddenAt = 42
			require.NoError(t, gdb.Create(retired).Error)

			q, err := hallQuery(gdb, tc.kind, "", tc.set)
			require.NoError(t, err)
			got := actNos(t, q)
			sort.Strings(got)
			want := append([]string{}, tc.want...)
			sort.Strings(want)
			assert.Equal(t, want, got)
		})
	}
}

// TestEveryPlayHasAWritableSettingKey 守住"新增一种玩法就得有一个开关"。
//
// 管理端那一页整个由 editable_keys + bounds 驱动:少一个键,界面上就少一个
// 开关,而后端的 PUT 会以"包含不可在线修改的配置项"400 —— 两边同时哑掉。
func TestEveryPlayHasAWritableSettingKey(t *testing.T) {
	withLotteryConfig(t, config.Lottery{
		MaxActiveActivities: 10, MaxGuessFeeBps: 500, MaxTotalPrizeQuota: 50_000_000,
	})
	editable := make(map[string]bool, len(editableKeys))
	for _, k := range editableKeys {
		editable[k] = true
	}
	bounds := settingBounds()
	snapshot := settingsSnapshot(allPlaysShown())

	require.Len(t, Plays, 4, "玩法枚举变了,本测试的期望值要跟着重写")
	for _, play := range Plays {
		key := playSettingKey(play)
		require.NotEmptyf(t, key, "玩法 %s 没有登记 qy_settings 键名", play)
		assert.Truef(t, editable[key], "%s 不在 editableKeys 里,管理端渲染不出这个开关", key)

		b, ok := bounds[key]
		require.Truef(t, ok, "%s 没有取值区间,PUT 会被当成不可在线修改的键顶回来", key)
		assert.Equal(t, int64(0), b.Lo, key)
		assert.Equal(t, int64(1), b.Hi, key)

		_, ok = snapshot[key]
		assert.Truef(t, ok, "%s 不在生效值快照里,配置页会渲染成一格空白", key)
	}
}

// TestPutConfigRoundTripsEveryPlaySwitch 走一遍写入侧的赋值链。
//
// assignSetting 是 PUT 的唯一落点。漏掉一个 case 的表现是:保存返回 200、
// 审计里 before==after、界面刷新后开关自己弹回去 —— 没有任何一处报错。
func TestPutConfigRoundTripsEveryPlaySwitch(t *testing.T) {
	for _, play := range Plays {
		t.Run(play, func(t *testing.T) {
			s := allPlaysShown()
			assignSetting(&s, playSettingKey(play), 0)
			assert.Falsef(t, s.playShown(play), "%s 关掉之后仍然可见", play)

			// 只动这一个玩法,其余三个原样不变。
			for _, other := range Plays {
				if other == play {
					continue
				}
				assert.Truef(t, s.playShown(other), "关 %s 顺手把 %s 也关了", play, other)
			}

			assignSetting(&s, playSettingKey(play), 1)
			assert.Truef(t, s.playShown(play), "%s 再打开却没生效", play)
		})
	}
}

// playSwitchReaders 是**唯一**允许读玩法开关的函数。
//
// 这是这一整条改动里最重要的一条断言:隐藏一种玩法只挡"新参与"与"大厅可见性",
// 绝不许挡钱、挡查询、挡收尾。清单之外的每一个函数都必须对这个开关一无所知:
//
//   - handleListMyEntries / handleGetMyPrize —— 已参与的人查票、领文本奖;
//   - handleGetProof —— 匿名证据链;
//   - runLock / runReveal / runVoidExpired / DrivePayouts / runSettle ——
//     封盘、开奖、流局、派奖、收尾、退款;
//   - handleAdminList* / handleSetGuessResult / handleFulfillPrize / …
//     —— 整个管理端,包括给一场已经收了钱的活动收尾。
//
// 任何一处新增读取都会让上面某一句不再成立,而那种失败在接口上是 200,
// 在界面上是"我的奖去哪了"。
var playSwitchReaders = map[string]bool{
	// play.go 里的两个派生:一个回答"整行导航还渲不渲染",
	// 一个是引导端点的下发形状。
	"anyPlayShown":      true,
	"playVisibilityMap": true,
	// 详情页要据此置灰"参与"按钮并说明原因(页面本身照常可达)。
	"handleGetActivity": true,
	// 新参与的唯一执行点。
	"ChargeEntry": true,
	// 发布的唯一执行点。草稿不拦,发布才拦。
	"handlePublishActivity": true,
}

func TestPlaySwitchIsReadOnlyByEntryAndHallPaths(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.Greater(t, len(files), 10, "扫到的文件太少,遍历八成写错了")

	fset := token.NewFileSet()
	found := map[string]bool{}
	scanned := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		scanned++
		f, err := parser.ParseFile(fset, path, nil, 0)
		require.NoErrorf(t, err, "解析 %s 失败", path)

		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			// playShown 的**声明**天然提到自己。
			if fn.Name.Name == "playShown" {
				return true
			}
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				sel, ok := inner.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "playShown" {
					return true
				}
				found[fn.Name.Name] = true
				if !playSwitchReaders[fn.Name.Name] {
					assert.Failf(t, "玩法开关读到了不该读它的地方",
						"%s 的 %s 里读了 playShown。隐藏一种玩法只影响大厅可见性、"+
							"新参与与发布 —— 查票、领奖、证据链、封盘/开奖/派奖/退款/收尾"+
							"与整个管理端都必须对它一无所知,否则藏掉入口就等于把已经"+
							"收进来的参与费悬在半空。", path, fn.Name.Name)
				}
				return false
			})
			return true
		})
	}
	require.Greater(t, scanned, 10, "扫到的非测试文件太少,遍历八成写错了")

	// 反向:清单里每一项都必须真的还在读它。留一行没有对应实现的允许项,
	// 等于把闸门删掉之后这条护栏还照绿。
	for name := range playSwitchReaders {
		assert.Truef(t, found[name], "%s 已经不读玩法开关了 —— 闸门被删了,还是这一行该清掉?", name)
	}
}

// 大厅过滤只能有一个执行点。第二处过滤意味着两份口径,
// 而漂移的方向恰好是"某一条列表路径忘了过滤"。
func TestHallFilterHasASingleCallSite(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	fset := token.NewFileSet()
	callers := map[string]bool{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		require.NoErrorf(t, err, "解析 %s 失败", path)
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "playFilterClause" {
				return true
			}
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "playFilterClause" {
					callers[fn.Name.Name] = true
				}
				return true
			})
			return true
		})
	}
	assert.Equal(t, map[string]bool{"hallQuery": true}, callers)
}

// 编译期钉死 hallQuery 的签名:玩法配置必须由调用方显式传进来。
//
// 若哪天有人把它改回自己去读 effective(),测试里就再也无法构造"只开竞猜"
// 这种状态,上面那一整张表会退化成只测默认值 —— 而且不会有任何一处变红。
var _ func(*gorm.DB, string, string, opSettings) (*gorm.DB, error) = hallQuery
