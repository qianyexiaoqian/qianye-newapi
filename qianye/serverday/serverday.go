package serverday

import "time"

// serverday.go —— 「服务器本地时区的自然日」唯一的一份实现。
//
// 项目方口径:「以服务器的时间为准,即 0 点到 23 点 59 分 59 秒是今日。」
//
// 消费方目前两处,此前各写各的:
//
//	qianye/modules/withdraw/create.go   提现/划转的日限额窗口(本实现的原始出处)
//	qianye/controller/token_today_usage.go   密钥页「今日消耗」那一列
//
// # 这与返佣的「消费日」不是同一个东西,而且刻意不统一
//
// qianye/modules/commission/dayline.go 那一个受 commission.day_offset_minutes
// 管辖,是一个**写死在配置里的固定偏移**:计佣挂在 relay 上、结算由租约选主,
// 两者会落在不同节点上,只有固定偏移才能保证同一笔消费不进两个桶。
// 本文件这一个跟着机器所在地走,面向的是运营与用户对「今天」的直觉。
//
// 两者在演示机上差 7 小时(day_offset_minutes=0 即 UTC,而机器本地是 PST)。
// 所以凡是用了本文件的界面,都必须把「这是服务器本地时间」以及当前实际
// 在用的时区写给用户看 —— 否则密钥页的「今日」与日消费明细的「今日」是两段
// 不同的时间,而两个数都是对的、两边都不会报错。
//
// # 时区从哪来:time.Local,没有第二个来源,也没有配置项
//
//	Unix     TZ 环境变量非空 → 按它加载;TZ 未设 → /etc/localtime;都没有 → UTC
//	Windows  取操作系统的时区设置
//
// **容器里的实情**:官方 golang / alpine / distroless 镜像默认既不设 TZ、
// 也不装 tzdata,于是 time.Local 就是 UTC。运营心里的「服务器本地」与进程认的
// 「本地」可以差半个地球,而这件事不会有任何报错。因此 Zone 把当前真正在用的
// 时区(缩写 + 相对 UTC 的偏移)一路带到界面上,让它看得见 —— 修法是给容器
// 设 TZ(并装 tzdata),不是在这里加一个"时区配置项"去替 time.Local 撒谎。
//
// # 为什么不能只写 time.Date(y, m, d, 0, 0, 0, 0, loc)
//
// 那是本实现在 withdraw 里的原样,它在夏令时跳变日会给出**别的日子**的瞬间。
// Go 1.25.1 实测(America/Sao_Paulo,2018-11-04 当地 23:00 直接跳到 01:00,
// 本地午夜根本不存在):
//
//	time.Date(2018, 11, 4, 0, 0, 0, 0, loc) → 2018-11-03 23:00:00 -0300(unix 1541296800)
//	该日真正的第一个瞬间                      → 2018-11-04 01:00:00 -0200(unix 1541300400)
//
// 差的这一小时属于**前一天**:提现日限额会把昨晚 23:00 之后的单算进今天的
// 额度,而"今日消耗"会把昨天最后一小时的钱算成今天的。一年两次、只在跳变日
// 出现、数字看起来完全正常。
//
// 所以本文件不采信 time.Date 的归一化结果,而是直接在时间轴上找边界:
// 一天的第一个瞬间 = 本地日期第一次变成 (y, m, d) 的那一秒。这条定义对
// 三种日子给出的答案分别是(均为 Go 实测):
//
//	普通日   本地 00:00:00
//	午夜空洞 跳变发生的那一刻(Sao_Paulo 2018-11-04 01:00 -02)
//	午夜重复 **第一次**出现的那个 00:00(America/Havana 2018-11-04 00:00 -04,
//	         一小时后还会再出现一次 00:00 -05)
//
// 与 java.time 的 LocalDate.atStartOfDay(zone) 同一语义。

// searchSpanSeconds 是二分查找的半径。
//
// 取 15 小时:tzdata 的偏移范围是 UTC-12..UTC+14,所以任何一个本地日期的
// 起点都落在「该日期的 UTC 午夜」± 14 小时之内,再留一小时余量。
const searchSpanSeconds = int64(15 * 3600)

// StartIn 返回 ts 所在那个本地自然日的第一个瞬间(unix 秒)。
func StartIn(ts int64, loc *time.Location) int64 {
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := time.Unix(ts, 0).In(loc).Date()
	return startOfDate(y, m, d, loc)
}

// RangeIn 返回 ts 所在本地自然日的半开区间 [start, end)。
//
// end 是**下一个本地自然日的起点**。两处都不能图省事:
//
//	end = start + 86400            跳变日只有 23(或 25)小时,会落进隔壁那一天;
//	end = StartIn(start+一天)       "加一天"若用 AddDate 落在午夜空洞上会被 Go
//	                               归一化回**前一天** 23:00,于是 end == start,
//	                               窗口塌成空集 —— 密钥页当天全站显示 0,不报错。
//	                               (实测:America/Araguaina 1995-10-14,len=0)
//
// 所以下一天是在**日历**上加的,与时区无关,再各自去找那一天的起点。
func RangeIn(ts int64, loc *time.Location) (int64, int64) {
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := time.Unix(ts, 0).In(loc).Date()
	// time.Date 会把 d+1 规整成合法日期(12 月 32 日 → 次年 1 月 1 日)。
	// 这里用 UTC 只是借它做日历加法,不涉及任何偏移。
	ny, nm, nd := time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC).Date()
	return startOfDate(y, m, d, loc), startOfDate(ny, nm, nd, loc)
}

// startOfDate 返回本地日期 (y, m, d) 的第一个瞬间。
//
// 二分「本地日期是否已经到了 (y, m, d)」这个判据的边界:
// lo 一定还没到(它比该日期最早可能的起点还早一小时),hi 一定已经到了。
func startOfDate(y int, m time.Month, d int, loc *time.Location) int64 {
	base := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix()
	lo, hi := base-searchSpanSeconds, base+searchSpanSeconds
	for lo+1 < hi {
		mid := lo + (hi-lo)/2
		ly, lm, ld := time.Unix(mid, 0).In(loc).Date()
		reached := ly > y || (ly == y && (lm > m || (lm == m && ld >= d)))
		if reached {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi
}

// ZoneIn 返回 ts 那一刻本地时区的缩写与相对 UTC 的偏移(分钟)。
//
// 缩写可能是 "PDT"、"UTC",也可能是 "+08"、"-03" 这样的数字形式(tzdata 里
// 没有字母缩写的时区就长这样)。它单独看是有歧义的("CST" 既是中国标准时间
// 也是美国中部标准时间),所以界面上必须与偏移一起显示。
func ZoneIn(ts int64, loc *time.Location) (string, int) {
	if loc == nil {
		loc = time.UTC
	}
	name, offsetSeconds := time.Unix(ts, 0).In(loc).Zone()
	return name, offsetSeconds / 60
}

// Start 是 StartIn 在服务器本地时区(time.Local)上的形态。
func Start(ts int64) int64 { return StartIn(ts, time.Local) }

// Range 是 RangeIn 在服务器本地时区(time.Local)上的形态。
func Range(ts int64) (int64, int64) { return RangeIn(ts, time.Local) }

// Zone 是 ZoneIn 在服务器本地时区(time.Local)上的形态。
func Zone(ts int64) (string, int) { return ZoneIn(ts, time.Local) }
