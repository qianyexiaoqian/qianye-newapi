package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// log_shortfall_filter_test.go —— 「只看预扣不足」这条筛选的取数契约。
//
// 本站刻意接受结算把余额扣成负数(拍板与代价见 qianye/docs/decisions.md 的 D-01)。
// 接受的前提是**事后捞得出来**:每一笔预扣没兜住的请求都会在消费日志的
// other.admin_info 下留 pre_consume_shortfall,而这条筛选是把它们捞出来的唯一手段。
//
// 要守的两件事,都属于"错了但看起来完全正常"的那一类:
//
//	① 开了筛选只返回带标记的笔 —— 漏筛的表现是运营看到一整页正常请求,
//	   以为透支面比实际大得多。
//	② **不开筛选时一条都不许少** —— 这是更危险的一侧:把 LIKE 无条件挂上去
//	   会让日志页默认只显示透支的那几笔,而页面上没有任何地方指出它被筛过。
//
// 顺带钉死 total 与 items 同源:两者由同一个 tx 派生,分家的表现是"共 3 条"
// 下面列出 17 行。

func seedShortfallLog(t *testing.T, id int, username string, other string) {
	t.Helper()
	require.NoError(t, DB.Create(&Log{
		Id:        id,
		UserId:    1,
		CreatedAt: common.GetTimestamp(),
		Type:      LogTypeConsume,
		Username:  username,
		ModelName: "gpt-test",
		Other:     other,
	}).Error)
}

func TestGetAllLogsFiltersByPreConsumeShortfallMarker(t *testing.T) {
	require.NoError(t, DB.Exec("DELETE FROM logs").Error)
	t.Cleanup(func() { DB.Exec("DELETE FROM logs") })

	// 两笔带标记(一笔标记与别的 admin_info 键共存),两笔不带。
	seedShortfallLog(t, 9001, "qy-plain", `{"admin_info":{"use_channel":[1]}}`)
	seedShortfallLog(t, 9002, "qy-short-a",
		`{"admin_info":{"pre_consume_shortfall":{"reserved":100,"charged":900,"shortfall":800}}}`)
	seedShortfallLog(t, 9003, "qy-empty-other", ``)
	seedShortfallLog(t, 9004, "qy-short-b",
		`{"admin_info":{"use_channel":[7],"pre_consume_shortfall":{"reserved":1,"charged":2,"shortfall":1}}}`)

	t.Run("开了筛选只回带标记的笔", func(t *testing.T) {
		logs, total, err := GetAllLogs(LogTypeConsume, 0, 0, "", "", "", 0, 100, 0, "", "", "", true)
		require.NoError(t, err)

		assert.EqualValues(t, 2, total, "共 4 笔,其中 2 笔带标记")
		require.Len(t, logs, 2, "total 与 items 必须同源")
		got := []string{logs[0].Username, logs[1].Username}
		assert.ElementsMatch(t, []string{"qy-short-a", "qy-short-b"}, got)
	})

	t.Run("不开筛选一条都不少", func(t *testing.T) {
		logs, total, err := GetAllLogs(LogTypeConsume, 0, 0, "", "", "", 0, 100, 0, "", "", "", false)
		require.NoError(t, err)

		assert.EqualValues(t, 4, total,
			"关掉筛选时 LIKE 一个字都不许进 WHERE —— 否则日志页默认就是被筛过的")
		assert.Len(t, logs, 4)
	})

	t.Run("筛选与其它条件叠加", func(t *testing.T) {
		// 与用户名条件同时生效:两个条件是 AND,不是互相覆盖。
		logs, total, err := GetAllLogs(LogTypeConsume, 0, 0, "", "qy-short-a", "", 0, 100, 0, "", "", "", true)
		require.NoError(t, err)

		assert.EqualValues(t, 1, total)
		require.Len(t, logs, 1)
		assert.Equal(t, "qy-short-a", logs[0].Username)
	})
}
