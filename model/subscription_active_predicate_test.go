package model

// subscription_active_predicate_test.go —— 「这条订阅此刻仍然有效」只能有一处定义。
//
// ═══════════════════════ 它防的是什么 ═══════════════════════
//
// 永久档订阅的 end_time 是 0。任何手写成 `end_time > ?` 的有效性判据都会把它判成
// **已过期**,而每一处的表现都不一样:
//
//	升级订阅探测   → 到期回退时以为"没有别的有效升级订阅了",把用户的组降回去
//	可用额度       → 永久订阅的额度用不了
//	套餐解锁       → 它解锁的模型分组突然从下拉里消失
//	名额闸门       → 名额没被占住,超卖
//	持有人清单     → 管理端看不到这批人
//
// 这类缺陷不会一起暴露,只会一个一个地被发现,而每一个都长得像独立的 bug。
// 本仓当初有 12 个这样的调用点,一次收敛到 SubscriptionActiveEndTimeSQL。
//
// 到期扫描是**例外**:它要找的恰恰是"有限期且已经到点"的行,写成
// `end_time > 0 AND end_time <= ?` 是对的 —— 那个 `> 0` 正是在排除永久档。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// scanRoots 是要扫的目录。订阅的有效性判据散落在主模型与两个扩展模块里。
var scanRoots = []string{
	".",
	"../qianye/modules/planentitlement",
	"../qianye/modules/subscription",
}

// allowedRawPredicates 是允许手写 end_time 判据的地方:只有到期扫描。
//
// 判据用整行文本而不是行号:行号会随着上下的编辑漂移,而这两行的内容本身
// 就带着 `> 0`(排除永久档)这个特征,改动它等于改变语义,那时候本测试变红是对的。
var allowedRawPredicates = []string{
	`DB.Where("status = ? AND end_time > 0 AND end_time <= ?", "active", now)`,
	`Where("user_id = ? AND status = ? AND end_time > 0 AND end_time <= ?", userId, "active", now)`,
}

func TestSubscriptionActivePredicateHasSingleDefinition(t *testing.T) {
	var offenders []string

	for _, root := range scanRoots {
		entries, err := os.ReadDir(root)
		require.NoError(t, err, "读取目录 %s", root)
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(root, name)
			raw, err := os.ReadFile(path)
			require.NoError(t, err, "读取 %s", path)

			for i, line := range strings.Split(string(raw), "\n") {
				trimmed := strings.TrimSpace(line)
				// 只看真正的查询,跳过注释 —— 本文件与常量的说明里都会提到这个片段。
				if strings.HasPrefix(trimmed, "//") {
					continue
				}
				if !strings.Contains(trimmed, "end_time >") {
					continue
				}
				if strings.Contains(trimmed, "SubscriptionActiveEndTimeSQL") {
					continue
				}
				allowed := false
				for _, ok := range allowedRawPredicates {
					if strings.Contains(trimmed, ok) {
						allowed = true
						break
					}
				}
				if !allowed {
					offenders = append(offenders,
						filepath.ToSlash(path)+":"+itoa(i+1)+"  "+trimmed)
				}
			}
		}
	}

	require.Emptyf(t, offenders,
		"以下位置手写了 end_time 的有效性判据,永久档(end_time = 0)会被它们判成已过期:\n%s\n\n"+
			"请改用 model.SubscriptionActiveEndTimeSQL。到期扫描是唯一例外 —— "+
			"它要找的就是「有限期且已到点」,那个 `> 0` 正是在排除永久档。",
		strings.Join(offenders, "\n"))
}

// itoa 避免为一个行号引入 strconv —— 本文件只需要这一处。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestCalcPlanEndTimePermanentIsZero 永久档必须算出 0,而不是报错或一个未来时间。
//
// 0 是 SubscriptionActiveEndTimeSQL 与到期扫描共同认定的「永不过期」哨兵;
// 返回别的值会让这条订阅在某一天悄悄到期,而运营卖的是"永久"。
func TestCalcPlanEndTimePermanentIsZero(t *testing.T) {
	plan := &SubscriptionPlan{DurationUnit: SubscriptionDurationPermanent}
	// 刻意不填 DurationValue:永久档不该要求运营为它编一个时长。
	end, err := calcPlanEndTime(time.Now(), plan)
	require.NoError(t, err, "永久档不该要求 duration_value")
	require.Zero(t, end, "永久档的 end_time 必须是 0")
}
