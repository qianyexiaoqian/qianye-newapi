package withdraw

import (
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func idemCaseCfg() config.Withdraw {
	return config.Withdraw{
		Methods:  []string{config.WithdrawMethodQuota},
		MinQuota: 1,
	}
}

// 提现幂等键同样必须折叠大小写并收紧字符集,理由与划转逐字相同:
// 不折叠的话同一个人用 "wd-order-a" 与 "WD-ORDER-A" 提两张全新申请,
// MySQL 上第二张被当成重放返回第一张的单据,PostgreSQL 上冻结两次佣金。
func TestAcceptCreateFoldsIdemKeyCase(t *testing.T) {
	base := createRequest{Method: config.WithdrawMethodQuota, Quota: 1000}

	lower := base
	lower.ClientRequestId = "wd-order-a"
	a, err := acceptCreate(lower, idemCaseCfg())
	require.NoError(t, err)

	upper := base
	upper.ClientRequestId = "WD-ORDER-A"
	b, err := acceptCreate(upper, idemCaseCfg())
	require.NoError(t, err)

	assert.Equal(t, "wd-order-a", a.IdemKey)
	assert.Equal(t, a.IdemKey, b.IdemKey, "只差大小写的两次申请必须落到同一个键")
	assert.True(t, qymodel.IsCollationNeutralIdemKey(a.IdemKey))
}

func TestAcceptCreateRejectsUnsafeIdemCharsets(t *testing.T) {
	for _, raw := range []string{"café-order", "订单一二三四五六七八", "wd order a", "wd:order:a"} {
		req := createRequest{ClientRequestId: raw, Method: config.WithdrawMethodQuota, Quota: 1000}
		_, err := acceptCreate(req, idemCaseCfg())
		assert.Errorf(t, err, "%q 不该被当成合法幂等键", raw)
	}
}
