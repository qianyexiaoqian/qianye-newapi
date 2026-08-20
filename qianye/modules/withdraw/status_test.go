package withdraw

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// 状态机是提现的资金安全边界:一次非法跃迁就可能让佣金被既核销又退回。
// 这里把每一条边(以及每一条不该存在的边)都固定下来。
func TestCanTransit(t *testing.T) {
	legal := []struct{ from, to string }{
		{StatusPending, StatusApproved},
		{StatusPending, StatusRejected},
		{StatusPending, StatusCancelled},
		{StatusApproved, StatusPaid},
		{StatusApproved, StatusFailed},
	}
	for _, tc := range legal {
		assert.True(t, canTransit(tc.from, tc.to), "%s → %s 应当合法", tc.from, tc.to)
	}

	illegal := []struct {
		name     string
		from, to string
	}{
		{"跳过审核直接标记已发放", StatusPending, StatusPaid},
		{"跳过审核直接标记发放失败", StatusPending, StatusFailed},
		{"已发放不可回退", StatusPaid, StatusFailed},
		{"已发放不可重复发放", StatusPaid, StatusPaid},
		{"已驳回不可复活", StatusRejected, StatusApproved},
		{"已撤销不可复活", StatusCancelled, StatusPending},
		{"发放失败不可自动重试成已发放", StatusFailed, StatusPaid},
		{"审核通过后不可再被撤销", StatusApproved, StatusCancelled},
		{"审核通过后不可再被驳回", StatusApproved, StatusRejected},
		{"待发放不可退回待审", StatusApproved, StatusPending},
		{"未知状态一律拒绝", "whatever", StatusPaid},
	}
	for _, tc := range illegal {
		t.Run(tc.name, func(t *testing.T) {
			assert.False(t, canTransit(tc.from, tc.to))
		})
	}
}

// "终态"有两处定义:isTerminal 的判断,与转移表里有没有出边。
// 两者必须永远一致 —— 任何一侧被单独改动(给终态加了一条出边,
// 或新增状态时忘了登记),都会让提现单在终结之后还能被推着走。
func TestTerminalDefinitionMatchesTransitionTable(t *testing.T) {
	for status := range knownStatuses {
		if isTerminal(status) {
			assert.Empty(t, allowedTransitions[status], "%s 是终态,不应有出边", status)
			continue
		}
		assert.NotEmpty(t, allowedTransitions[status], "%s 非终态,必须有出边", status)
	}
}
