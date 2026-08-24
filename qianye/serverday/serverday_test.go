package serverday

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serverday_test.go —— 「服务器本地自然日」的边界。
//
// 这一层错起来没有任何症状:窗口偏一小时,密钥页的「今日消耗」照样是一个
// 像模像样的金额,提现日限额照样放行或拦截,只是把昨晚最后一小时算成了今天。
// 所以下面每一个期望值都是**独立算出来的**:用一段线性扫描(从该本地日期的
// UTC 午夜起,逐分钟往两侧找第一个本地日期等于目标的瞬间)得到,与被测代码
// 的二分查找没有共用一行逻辑。真实时区数据由 Go 自带的 tzdata 提供。
//
// 挑这几个时区不是为了凑数,每一行都是一类会让朴素实现出错的日子:
//
//	America/Sao_Paulo  2018-11-04  本地午夜**不存在**(23:00 直接跳到 01:00)
//	America/Havana     2018-11-04  本地午夜**出现两次**(00:00 -04,一小时后又 00:00 -05)
//	America/Araguaina  1995-10-14  次日午夜不存在 —— 「当天 + 86400」与
//	                               「AddDate(0,0,1) 再取起点」两种写法都会错
//	America/Los_Angeles 2026-03-08 / 2026-11-01  23 小时 / 25 小时的日子
//	Asia/Kolkata       半小时偏移,专治"偏移一定是整小时"的假设
//	Pacific/Kiritimati / Etc/GMT+12  tzdata 的偏移上下界(+14:00 / -12:00),
//	                   钉住二分查找那个 ±15 小时的搜索半径 —— 半径缩到 13 小时
//	                   时 +14 那一档就找不到起点了

// dayCase 是一条完整的日子:输入时刻、该本地日的起点与下一日的起点。
type dayCase struct {
	name      string
	zone      string
	at        int64 // 输入(取当地正午,离任何边界都远)
	wantStart int64
	wantEnd   int64
}

func dayCases() []dayCase {
	return []dayCase{
		{"Asia/Shanghai 普通日", "Asia/Shanghai", 1787371200, 1787328000, 1787414400},
		{"America/Los_Angeles 普通日", "America/Los_Angeles", 1787425200, 1787382000, 1787468400},
		{"UTC 普通日", "UTC", 1787400000, 1787356800, 1787443200},
		{"Asia/Kolkata 半小时偏移", "Asia/Kolkata", 1787380200, 1787337000, 1787423400},
		{"Pacific/Kiritimati 偏移上界 +14", "Pacific/Kiritimati", 1787349600, 1787306400, 1787392800},
		{"Etc/GMT+12 偏移下界 -12", "Etc/GMT+12", 1787443200, 1787400000, 1787486400},
		{"America/Sao_Paulo 本地午夜不存在", "America/Sao_Paulo", 1541340000, 1541300400, 1541383200},
		{"America/Havana 本地午夜出现两次", "America/Havana", 1541350800, 1541304000, 1541394000},
		{"America/Araguaina 次日午夜不存在", "America/Araguaina", 813682800, 813639600, 813726000},
		{"America/Araguaina 起点就在跳变上", "America/Araguaina", 813765600, 813726000, 813808800},
		{"America/Los_Angeles 春季 23 小时", "America/Los_Angeles", 1772996400, 1772956800, 1773039600},
		{"America/Los_Angeles 秋季 25 小时", "America/Los_Angeles", 1793563200, 1793516400, 1793606400},
	}
}

func mustLoad(t *testing.T, zone string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	require.NoError(t, err, "时区 %s 加载失败:没有 tzdata 的话整个用例在测空气", zone)
	return loc
}

// TestRangeInIsTheLocalNaturalDay 钉住区间本身。
//
// 变异验证见文件末尾。
func TestRangeInIsTheLocalNaturalDay(t *testing.T) {
	for _, tc := range dayCases() {
		t.Run(tc.name, func(t *testing.T) {
			loc := mustLoad(t, tc.zone)

			start, end := RangeIn(tc.at, loc)
			assert.Equal(t, tc.wantStart, start, "起点错了:%s",
				time.Unix(start, 0).In(loc).Format(time.RFC3339))
			assert.Equal(t, tc.wantEnd, end, "终点错了:%s",
				time.Unix(end, 0).In(loc).Format(time.RFC3339))
			assert.Equal(t, tc.wantStart, StartIn(tc.at, loc), "StartIn 必须与 RangeIn 的左端一致")

			// 区间必须真的罩住输入时刻,而且必须是正长度。
			// 长度为 0 是本实现踩过的坑(America/Araguaina:次日午夜落在空洞里,
			// 被 Go 归一化回前一天 23:00,窗口塌成空集,界面上全站显示 0)。
			assert.Greater(t, end, start, "窗口不能是空集")
			assert.LessOrEqual(t, start, tc.at)
			assert.Greater(t, end, tc.at)
		})
	}
}

// TestStartInAtTheSeamsOfTheDay 钉住三条缝。
//
// 「0 点到 23 点 59 分 59 秒是今日」这句话的全部内容就在这三条缝上:
// 起点前一秒必须属于昨天,终点前一秒必须仍属今天,终点那一秒必须属于明天。
func TestStartInAtTheSeamsOfTheDay(t *testing.T) {
	for _, tc := range dayCases() {
		t.Run(tc.name, func(t *testing.T) {
			loc := mustLoad(t, tc.zone)

			assert.Equal(t, tc.wantStart, StartIn(tc.wantStart, loc),
				"起点自己必须属于这一天")
			assert.NotEqual(t, tc.wantStart, StartIn(tc.wantStart-1, loc),
				"起点前一秒必须落回昨天")
			assert.Equal(t, tc.wantStart, StartIn(tc.wantEnd-1, loc),
				"当天最后一秒(本地 23:59:59)必须仍算今天")
			assert.Equal(t, tc.wantEnd, StartIn(tc.wantEnd, loc),
				"终点那一秒已经是明天的第一秒")

			// 起点本身必须是这一天在本地日历上的第一个瞬间:
			// 前一秒的本地日期严格早于起点的本地日期。
			sy, sm, sd := time.Unix(tc.wantStart, 0).In(loc).Date()
			py, pm, pd := time.Unix(tc.wantStart-1, 0).In(loc).Date()
			assert.NotEqual(t, [3]any{sy, sm, sd}, [3]any{py, pm, pd},
				"起点前一秒与起点同属一天 —— 说明起点不是这一天最早的那一秒")
		})
	}
}

// TestStartInSplitsTheSameInstantByTimezone 是「服务器时区决定一切」这句话
// 的实测形态。
//
// 同一个 unix 秒,在不同时区里属于不同的自然日 —— 这正是本轮把日界从
// commission.day_offset_minutes 换成服务器本地时区之后,界面必须把时区
// 写给用户看的原因。
func TestStartInSplitsTheSameInstantByTimezone(t *testing.T) {
	// 2026-08-22 06:00 UTC:上海已经是 22 日下午,洛杉矶还是 21 日夜里。
	const at = int64(1787378400)

	sh := mustLoad(t, "Asia/Shanghai")
	la := mustLoad(t, "America/Los_Angeles")

	assert.Equal(t, "2026-08-22", time.Unix(StartIn(at, sh), 0).In(sh).Format("2006-01-02"))
	assert.Equal(t, "2026-08-21", time.Unix(StartIn(at, la), 0).In(la).Format("2006-01-02"))
	assert.NotEqual(t, StartIn(at, sh), StartIn(at, la),
		"两地的「今天」不是同一段时间 —— 这条差异必须一路显示到界面上")
}

// TestZoneInReportsWhatIsActuallyInUse 钉住下发给界面的时区标签。
//
// 界面上只写「今日 00:00 → 23:59:59」是不够的:容器里 TZ 常常没设、tzdata
// 常常没装,进程认的「本地」就是 UTC,而运营以为是他自己所在地。所以缩写与
// 偏移必须一起下发,并且必须来自**真正在用**的那个 Location。
func TestZoneInReportsWhatIsActuallyInUse(t *testing.T) {
	cases := []struct {
		zone       string
		at         int64
		wantName   string
		wantOffset int
	}{
		{"UTC", 1787400000, "UTC", 0},
		{"Asia/Shanghai", 1787371200, "CST", 480},
		{"Asia/Kolkata", 1787380200, "IST", 330},
		{"America/Los_Angeles", 1787425200, "PDT", -420},
		{"America/Los_Angeles", 1793563200, "PST", -480}, // 同一时区,冬令时
		{"America/Sao_Paulo", 1541340000, "-02", -120},   // 没有字母缩写的时区
		{"Pacific/Kiritimati", 1787349600, "+14", 840},
	}
	for _, tc := range cases {
		t.Run(tc.zone+"@"+time.Unix(tc.at, 0).UTC().Format("2006-01-02"), func(t *testing.T) {
			name, offset := ZoneIn(tc.at, mustLoad(t, tc.zone))
			assert.Equal(t, tc.wantName, name)
			assert.Equal(t, tc.wantOffset, offset)
		})
	}
}

// TestLocalWrappersUseTimeLocal 钉住"没有第二个时区来源"。
//
// Start / Range / Zone 是给业务代码用的入口,它们必须逐字等于在 time.Local
// 上调有参版本。写成别的(比如读一个配置项、或退回 UTC)会让界面显示的时区
// 与实际用的窗口分家。
func TestLocalWrappersUseTimeLocal(t *testing.T) {
	const at = int64(1787378400)

	assert.Equal(t, StartIn(at, time.Local), Start(at))

	wantStart, wantEnd := RangeIn(at, time.Local)
	gotStart, gotEnd := Range(at)
	assert.Equal(t, wantStart, gotStart)
	assert.Equal(t, wantEnd, gotEnd)

	wantName, wantOffset := ZoneIn(at, time.Local)
	gotName, gotOffset := Zone(at)
	assert.Equal(t, wantName, gotName)
	assert.Equal(t, wantOffset, gotOffset)
}

// 变异验证(每一条都实际跑过):
//
//	StartIn 退回朴素的 time.Date(y, m, d, 0, 0, 0, 0, loc)
//	  → Sao_Paulo / Araguaina 两档 KILLED(起点落到前一天 23:00)
//	RangeIn 的 end 改成 start + 86400
//	  → 洛杉矶春秋两档与 Araguaina 三档 KILLED
//	RangeIn 的 end 改成 StartIn(当地起点 AddDate(0,0,1))
//	  → Araguaina「次日午夜不存在」档 KILLED(end == start,窗口塌成空集)
//	startOfDate 的判据 ld >= d 改成 ld > d
//	  → 全部档 KILLED(整体后移一天)
//	searchSpanSeconds 由 15 小时缩到 13 小时
//	  → Pacific/Kiritimati(UTC+14)KILLED;缩到 12 小时以内 Etc/GMT+12 也 KILLED。
//	  (先按 12 小时试过一轮,SURVIVED —— UTC+8 在 12 小时里仍然找得到起点,
//	   于是把偏移上下界那两个时区补进表里,再试才红。)
//	ZoneIn 的偏移改成 offsetSeconds(不除 60)
//	  → TestZoneInReportsWhatIsActuallyInUse KILLED
