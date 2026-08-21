package lottery

// play.go —— 玩法(用户视角的"这是哪一种游戏")与它的显示/隐藏开关。
//
// # 玩法不是 kind
//
// 库里只有两个 kind(draw / guess),但用户看到的是四种游戏:抽奖有三种定档方式
// (rank 按名次、prob 按公示概率、ball 双色球),竞猜一种。三种定档方式在规则、
// 概率口径与卡面含义上互不相同 —— 双色球的卡片上连"奖池"这个数的语义都换了。
// 运营说"这一期只上竞猜"或"双色球先不上"时,说的是这一层,不是 kind。
//
// 所以玩法 = (kind, draw_mode) 的合并投影,四个取值,住在这一个函数里。
// 判定点只此一处:大厅过滤、新参与闸门、发布闸门、引导端点四个消费方都读它,
// 各写一份 switch 的后果是其中一处漏掉 ball,而漏掉的方向恰好是"关了还看得见"。
//
// # 隐藏的语义:只挡新参与与大厅可见性,绝不挡钱
//
// 与本仓「下架」(hidden_at)一致,而且比它更克制:
//
//   - 大厅列表不再返回该玩法的活动;
//   - 该玩法的活动不再受理**新的**参与(ChargeEntry 拒绝);
//   - 该玩法的活动不再允许**发布**(草稿可以照常备着);
//   - 其余一律照旧:活动详情、我的参与、文本奖领取、匿名证据链、
//     封盘/开奖/流局/派奖/收尾/退款六个后台任务、以及**整个管理端**。
//
// 最后一行是硬约束。隐藏一个还在跑的玩法却让它停止结算,等于把已经收进来的
// 参与费悬在半空;而管理端若跟着一起消失,运营就再也没有地方能把它打开、
// 也没有地方能给一场已经收了钱的活动收尾。
//
// # 零值:没配过 = 全部显示
//
// qy_settings 里没有这几行时,四个玩法**全部可见**。反过来(缺一段配置就整块
// 不可见)是本仓栽过的形状:升级到带这个开关的版本时,库里一行都还没有,
// 默认隐藏会让整个娱乐功能在没人动过任何配置的情况下静默消失。
const (
	PlayDrawRank = "draw_rank"
	PlayDrawProb = "draw_prob"
	PlayDrawBall = "draw_ball"
	PlayGuess    = "guess"
)

// Plays 是全部玩法。顺序即管理端字段顺序与引导端点的下发顺序。
var Plays = []string{PlayDrawRank, PlayDrawProb, PlayDrawBall, PlayGuess}

// playOf 把一行活动归到它的玩法。
//
// draw_mode 为空串的存量抽奖行归到 rank:normalizeDrawMode 把空串当 rank 收下,
// 所以库里确实存在这种行。少了这一条,老活动会在"按名次"开着的时候从大厅消失,
// 而且没有任何一处报错 —— 正是本仓反复出现的静默死路。
//
// 未登记的 draw_mode 同样归到 rank(而不是一个"未知"档):日后有人加了第四种
// 定档方式却忘了在这里登记,后果是它跟着 rank 的开关走(可能多显示一格),
// 而不是永久不可见。
func playOf(kind, drawMode string) string {
	if kind == KindGuess {
		return PlayGuess
	}
	switch drawMode {
	case DrawModeProb:
		return PlayDrawProb
	case DrawModeBall:
		return PlayDrawBall
	default:
		return PlayDrawRank
	}
}

// playSettingKey 是某个玩法在 qy_settings(scope=lottery)里的行键。
func playSettingKey(play string) string {
	switch play {
	case PlayDrawRank:
		return keyShowPlayDrawRank
	case PlayDrawProb:
		return keyShowPlayDrawProb
	case PlayDrawBall:
		return keyShowPlayDrawBall
	case PlayGuess:
		return keyShowPlayGuess
	}
	return ""
}

// playShown 回答"这个玩法现在对用户可见吗"。
//
// 未登记的玩法一律 false:它只可能来自接口参数或一次拼错,而"拼错的键被当成
// 可见"会让一次笔误变成一个打不开的开关。playOf 永远不会产出未登记的值。
func (s opSettings) playShown(play string) bool {
	switch play {
	case PlayDrawRank:
		return s.ShowPlayDrawRank
	case PlayDrawProb:
		return s.ShowPlayDrawProb
	case PlayDrawBall:
		return s.ShowPlayDrawBall
	case PlayGuess:
		return s.ShowPlayGuess
	}
	return false
}

// anyPlayShown 回答"整个娱乐入口还要不要渲染"。
//
// 四个玩法全关时前端不再渲染那一行导航(与 show_entry=0 完全同一形状:
// 路由与接口照常可达,直达链接进去只剩「我的参与」)。
func (s opSettings) anyPlayShown() bool {
	for _, p := range Plays {
		if s.playShown(p) {
			return true
		}
	}
	return false
}

// playVisibilityMap 是引导端点下发的形状。
func (s opSettings) playVisibilityMap() map[string]bool {
	out := make(map[string]bool, len(Plays))
	for _, p := range Plays {
		out[p] = s.playShown(p)
	}
	return out
}

// playFilterClause 把"哪些玩法当前可见"翻译成大厅查询的 WHERE 片段。
//
// **恒返回一个非空片段**。全部隐藏时给的是一个恒假条件而不是"不加条件":
// 后者的表现正是"开关关掉了但列表照旧",而那种失败没有任何一处会报错。
//
// 括号自己写全,不依赖 GORM 对含 OR 的裸表达式的自动加括号 —— 那条规则只在
// 同一个 Where 里有多个表达式时才触发,而这段与 status/hidden_at 的条件恰好
// 就是分开写的。少一层括号 = 已下架与草稿活动跟着 OR 一起漏出来。
//
// kind / draw_mode 在三种数据库里都不是保留字,不需要引号包装。
func playFilterClause(s opSettings) (string, []any) {
	modes := make([]string, 0, 4)
	if s.ShowPlayDrawRank {
		// 空串是存量抽奖行的 draw_mode,语义等同 rank(见 playOf)。
		modes = append(modes, DrawModeRank, "")
	}
	if s.ShowPlayDrawProb {
		modes = append(modes, DrawModeProb)
	}
	if s.ShowPlayDrawBall {
		modes = append(modes, DrawModeBall)
	}
	switch {
	case len(modes) > 0 && s.ShowPlayGuess:
		return "((kind = ? AND draw_mode IN ?) OR kind = ?)",
			[]any{KindDraw, modes, KindGuess}
	case len(modes) > 0:
		return "(kind = ? AND draw_mode IN ?)", []any{KindDraw, modes}
	case s.ShowPlayGuess:
		return "(kind = ?)", []any{KindGuess}
	default:
		return "1 = 0", nil
	}
}
