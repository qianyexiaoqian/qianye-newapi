package withdraw

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/modules/commission"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// review_response_fidelity_test.go —— 管理端单据视图必须把审核人**做决定时需要
// 知道的那几件事**如实说出来。
//
// 两条不变量:
//
//	R1  写接口的响应与读接口必须是同一份事实。mark-paid 的响应里 payout_note
//	    原先恒为空串而库里是对的 —— applyTransition 只把 status/updated_at 写回
//	    内存对象,四个兄弟字段各自手工补齐时漏了第五个。按写接口响应记账或回显
//	    的集成方会拿到一个假的空备注。
//	R2  收款人的冲正欠账状态必须出现在审核人正在看的那张单上。欠账只在【提交
//	    提现】那一刻拦一次,而冲正按设计只吃 available、吃不到已经冻住的 frozen;
//	    approve / mark-paid 是这笔钱最后一次还能被拦回来的地方(驳回与标记失败
//	    都会把 frozen 退回 available,退回后的 available 正是下一次结算能吃到的
//	    那一桶),而那一刻的界面上原先一个欠账字样都没有。

func adminViewOf(t *testing.T, res *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, common.Unmarshal(res.Body.Bytes(), &body))
	require.True(t, body.Success, "body=%s", res.Body.String())
	return body.Data
}

// R1:写接口的响应必须与库、与读接口逐字一致。
func TestMarkPaidResponseCarriesThePayoutNoteItJustStored(t *testing.T) {
	env := newReviewEnv(t)
	w := env.seedApproved(t, "WD-NOTE", config.WithdrawMethodQuota, 500000, 1000)

	const note = "qy 备注-A1"
	res := callAdmin(t, handleAdminMarkPaid, w.Id,
		`{"payout_ref":"MANUAL-LOG-1","confirm_quota":500000,"payout_note":"`+note+`"}`)
	require.Equal(t, http.StatusOK, res.Code, "body=%s", res.Body.String())

	assert.Equal(t, note, adminViewOf(t, res)["payout_note"],
		"写接口的响应不能把刚刚存进去的备注说成空串")
	assert.Equal(t, note, env.reload(t, w.Id).PayoutNote, "库列本来就该是对的")

	// 同一张单、同一个 toAdminView:读接口给什么,写接口就该给什么。
	read := callAdmin(t, handleAdminGet, w.Id, `{}`)
	require.Equal(t, http.StatusOK, read.Code)
	assert.Equal(t, note, adminViewOf(t, read)["payout_note"])
}

// R2:欠账状态必须出现在管理端单据上,读接口与四个决策接口都要有。
func TestAdminWithdrawalViewSurfacesTheDebtBlockedPayee(t *testing.T) {
	cases := []struct {
		name        string
		unsettled   string
		debtBlocked bool
		wantBlocked bool
	}{
		{"没有欠账", "0", false, false},
		{"冲正吃不到 frozen,差额挂成负的未结算余数", "-7000000", true, true},
		{"只置了 debt_blocked", "0", true, true},
		{"只有负余数也算欠账", "-1", false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newReviewEnv(t)
			w := env.seedApproved(t, "WD-DEBT", config.WithdrawMethodQuota, 500000, 1000)
			require.NoError(t, env.ext.Model(&commission.Balance{}).
				Where("user_id = ?", 7).
				Updates(map[string]any{
					"debt_blocked":     tc.debtBlocked,
					"unsettled_amount": decimal.RequireFromString(tc.unsettled),
				}).Error)

			read := adminViewOf(t, callAdmin(t, handleAdminGet, w.Id, `{}`))
			assert.Equal(t, tc.wantBlocked, read["debt_blocked"],
				"审核人就是在这一屏上决定要不要把钱发出去")
			assert.Equal(t, decimal.RequireFromString(tc.unsettled).String(), read["unsettled_amount"])

			// 决策接口的响应共用同一份视图,不能把标记洗成空。
			paid := adminViewOf(t, callAdmin(t, handleAdminMarkPaid, w.Id,
				`{"payout_ref":"MANUAL-LOG-2","confirm_quota":500000}`))
			assert.Equal(t, tc.wantBlocked, paid["debt_blocked"])
		})
	}
}
