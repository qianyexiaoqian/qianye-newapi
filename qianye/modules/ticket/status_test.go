package ticket

import (
	"testing"

	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
)

// nextStatus 决定"这张单还在等谁",而客服队列完全建立在这一个值上。
//
// 这里钉死三条契约,每一条都对应一个具体的坏结局:
//
//   - **内部备注永远不改状态**。改了的话,一张还没有人回应的单会从 open 队列里
//     消失,而用户一个字都没收到 —— 一张被静默丢弃的工单。
//   - **同一方连续追加不改状态**。用户在等客服的时候补一句话,不该让这张单
//     看起来像"客服已回复"。
//   - **跨方向才翻面**,且 closed 不产生任何跃迁(关单后的追加由调用方拒绝)。
func TestNextStatus(t *testing.T) {
	admin := func(internal bool) replyInput {
		return replyInput{AuthorType: qymodel.ActorAdmin, Internal: internal}
	}
	user := replyInput{AuthorType: qymodel.ActorUser}

	cases := []struct {
		name    string
		current string
		in      replyInput
		want    string
		why     string
	}{
		{"客服首次回复把 open 推进到 replied", StatusOpen, admin(false), StatusReplied,
			"这是队列里最常见的一步"},
		{"客服回复 user_replied 回到 replied", StatusUserReplied, admin(false), StatusReplied, ""},
		{"客服连续回复不改状态", StatusReplied, admin(false), "",
			"已经在等用户了,再补一句不该产生一次状态写"},
		{"用户在 replied 上追加翻成 user_replied", StatusReplied, user, StatusUserReplied, ""},
		{"用户在 open 上追加不改状态", StatusOpen, user, "",
			"本来就在等客服,补一句话不该让它看起来变了"},
		{"用户在 user_replied 上追加不改状态", StatusUserReplied, user, "", ""},

		// 内部备注:三个状态各测一次,因为它们各自有一条对外回复会走的边。
		{"内部备注不动 open", StatusOpen, admin(true), "",
			"用户什么都没收到,这张单仍然没有人回应过"},
		{"内部备注不动 user_replied", StatusUserReplied, admin(true), "", ""},
		{"内部备注不动 replied", StatusReplied, admin(true), "", ""},

		{"closed 上不产生任何跃迁", StatusClosed, admin(false), "",
			"关单后的追加由调用方直接拒绝,状态机不该替它开一条边"},
		{"closed 上用户追加同样不跃迁", StatusClosed, user, "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, nextStatus(tc.current, tc.in), tc.why)
		})
	}
}

// 状态机的边必须与 nextStatus 相容:nextStatus 给出的每一个目标状态,
// canTransit 都要放行。两者不一致的表现是 appendMessage 在事务里返回
// errIllegalTransition —— 一次用户看得见的"当前状态不允许该操作",
// 而他只是回了一句话。
func TestNextStatusIsAlwaysATransitionTheMachineAllows(t *testing.T) {
	for _, current := range []string{StatusOpen, StatusReplied, StatusUserReplied, StatusClosed} {
		for _, in := range []replyInput{
			{AuthorType: qymodel.ActorUser},
			{AuthorType: qymodel.ActorAdmin},
			{AuthorType: qymodel.ActorAdmin, Internal: true},
		} {
			to := nextStatus(current, in)
			if to == "" {
				continue
			}
			assert.True(t, canTransit(current, to),
				"nextStatus(%q, %+v) = %q,但 allowedTransitions 不认这条边", current, in, to)
		}
	}
}

// openStatuses 必须恰好等于"未关闭"。
//
// 它同时被三处消费:max_open_per_user 那道闸、/ticket/config 的剩余额度回显、
// 管理端角标。漏掉一个状态的三种表现分别是"上限算少了""用户看到的余量不对"
// "队列里少了一批单",而三处都不会报错。
func TestOpenStatusesCoversEveryNonClosedStatus(t *testing.T) {
	all := []string{StatusOpen, StatusReplied, StatusUserReplied, StatusClosed}
	inOpen := map[string]bool{}
	for _, s := range openStatuses {
		inOpen[s] = true
	}
	for _, s := range all {
		assert.Equal(t, s != StatusClosed, inOpen[s],
			"%q 是否属于 openStatuses 与它是否为终态不一致", s)
	}
}
