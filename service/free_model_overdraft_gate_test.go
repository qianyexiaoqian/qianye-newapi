package service

import (
	goast "go/ast"
	goparser "go/parser"
	gotoken "go/token"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 「免费模型」跳过的是【预扣】，不是【余额闸】。
//
// 关掉「免费模型预消耗」之后，价格/倍率为 0 的模型整段跳过 PreConsumeBilling，
// relayInfo.Billing 为 nil，于是既不过 tryWallet 的 wallet_overdrawn 判据，也不过
// model.TryReserveUserQuota 的原子预留；而跑完之后 SettleBilling 的回退分支直接
// PostConsumeQuota 裸扣钱包。免费模型并不等于这次调用不产生金额：内置工具调用的
// 附加费与 ModelRatio / ModelPrice 完全解耦（web_search_preview 单次 5000 quota），
// 于是一个已经欠费的账号可以无限次调用并无限次记账，全站唯一那道余额闸一次都不会
// 被问到。
//
// 只拦 userQuota < 0：余额刚好用完的人还能用免费模型，那正是这个开关的本意。
func TestRejectOverdrawnFreeModelCall(t *testing.T) {
	cases := []struct {
		name       string
		quota      int
		wantReject bool
	}{
		{"已欠费:必须拒绝,否则可以无限刷附加费", -1000, true},
		{"欠得很深", -26000, true},
		{"余额刚好用完:免费模型仍然可用", 0, false},
		{"有余额", 5000, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			require.NoError(t, model.DB.Create(&model.User{
				Id: 7701, Username: "qy-free-gate", Group: "default",
				Quota: tc.quota, Status: common.UserStatusEnabled,
			}).Error)

			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{UserId: 7701, OriginModelName: "qy-free-model"}

			apiErr := RejectOverdrawnFreeModelCall(c, info)
			if !tc.wantReject {
				assert.Nil(t, apiErr)
				return
			}
			require.NotNil(t, apiErr, "欠费账号必须被拦下")
			// 错误码与 tryWallet 的 wallet_overdrawn 那一档逐字相同:客户端不该
			// 因为管理员拨了一个开关就看到另一种拒绝。
			assert.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())
			assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
			assert.Contains(t, apiErr.Error(), "账户已透支")
		})
	}
}

// 按次定价的音频 / 实时模型必须真的收到钱。
//
// calculateAudioQuota 的 UsePrice 分支算的是 ModelPrice × QuotaPerUnit ×
// GroupRatio，而三个 QuotaInfo 字面量原先都没给 ModelPrice —— 零值 0 乘什么都是 0。
// 于是一个配了 ModelPrice 的音频模型每次调用都先预扣、再全额退回，站方一分钱收不到，
// 而日志里还写着「模型价格 0.50」。这条路在 /v1/chat/completions（只需
// AudioTokens>0）、/v1/responses（gpt-4o-audio* 前缀，连 token 门都没有）与实时会话
// 收尾三处都可达，本站的 ModelPrice 配置里有几十个模型。
func TestCalculateAudioQuotaChargesPerCallPrice(t *testing.T) {
	cases := []struct {
		name string
		info QuotaInfo
		want int
	}{
		{
			name: "按次定价:金额只由单价与分组倍率决定",
			info: QuotaInfo{
				UsePrice: true, ModelPrice: 0.5, GroupRatio: 1,
				InputDetails:  TokenDetails{TextTokens: 7, AudioTokens: 11},
				OutputDetails: TokenDetails{TextTokens: 3},
			},
			want: int(0.5 * common.QuotaPerUnit),
		},
		{
			name: "按次定价:分组倍率照乘",
			info: QuotaInfo{
				UsePrice: true, ModelPrice: 0.5, GroupRatio: 2,
			},
			want: int(0.5 * 2 * common.QuotaPerUnit),
		},
		{
			name: "单价配成 0 的模型才是 0",
			info: QuotaInfo{UsePrice: true, ModelPrice: 0, GroupRatio: 1},
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			quota, clamp := calculateAudioQuota(tc.info)
			assert.Nil(t, clamp)
			assert.Equal(t, tc.want, quota)
		})
	}
}

// 两条结算路径都必须真的把 PriceData.ModelPrice 交给 calculateAudioQuota。
//
// 上面那条测的是算式，这条测接线：字面量里漏掉这一位，算式再对也恒收 0，而
// 「纯函数改对了、调度层没接上」正是本项目反复出现的形状。
func TestAudioSettlementPassesModelPriceIntoQuotaInfo(t *testing.T) {
	fset := gotoken.NewFileSet()
	file, err := goparser.ParseFile(fset, "quota.go", nil, 0)
	require.NoError(t, err)

	for _, fn := range []string{"PostWssConsumeQuota", "PostAudioConsumeQuota"} {
		t.Run(fn, func(t *testing.T) {
			var target *goast.FuncDecl
			for _, d := range file.Decls {
				decl, ok := d.(*goast.FuncDecl)
				if ok && decl.Name.Name == fn {
					target = decl
				}
			}
			require.NotNil(t, target, "quota.go 里没有 %s,这份守卫已经过期", fn)

			found := false
			goast.Inspect(target, func(n goast.Node) bool {
				lit, ok := n.(*goast.CompositeLit)
				if !ok {
					return true
				}
				if id, ok := lit.Type.(*goast.Ident); !ok || id.Name != "QuotaInfo" {
					return true
				}
				for _, elt := range lit.Elts {
					kv, ok := elt.(*goast.KeyValueExpr)
					if !ok {
						continue
					}
					if key, ok := kv.Key.(*goast.Ident); ok && key.Name == "ModelPrice" {
						found = true
					}
				}
				return true
			})
			assert.True(t, found,
				"%s 构造的 QuotaInfo 少了 ModelPrice,按次定价的音频/实时模型会恒收 0", fn)
		})
	}
}
