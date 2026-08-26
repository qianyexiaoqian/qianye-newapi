package transfer

import (
	"testing"

	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 划转幂等键的客户端那一段必须被折成小写并收紧字符集。
//
// 键"相不相等"最终由 idem_key 列的排序规则说了算,而三方言口径不同:
// MySQL 的库默认排序规则大小写不敏感(8.0 还重音不敏感),PostgreSQL 与
// SQLite 按字节比较。不折叠的话,同一个人先发 client_request_id="tr-a"
// 再发 "TR-A"(两笔全新划转),MySQL 上第二笔被静默吞掉、PostgreSQL 上真的
// 扣两次 —— 同一份代码在两种官方支持的方言上给出相反的资金结果。
func TestBuildIdemKeyFoldsCase(t *testing.T) {
	lower, err := buildIdemKey(42, "tr-case-a")
	require.NoError(t, err)
	upper, err := buildIdemKey(42, "TR-CASE-A")
	require.NoError(t, err)
	mixed, err := buildIdemKey(42, "Tr-Case-A")
	require.NoError(t, err)

	assert.Equal(t, "42:tr-case-a", lower)
	assert.Equal(t, lower, upper, "只差大小写的两个请求必须落到同一个键")
	assert.Equal(t, lower, mixed)
	assert.True(t, qymodel.IsCollationNeutralIdemKey(lower))
}

// 用户 id 前缀仍然必须在:两个用户碰巧用了同一个 UUID 不许互相顶掉。
func TestBuildIdemKeyKeepsUserPrefix(t *testing.T) {
	a, err := buildIdemKey(1, "same-uuid")
	require.NoError(t, err)
	b, err := buildIdemKey(2, "same-uuid")
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
}

func TestBuildIdemKeyRejectsUnsafeCharsets(t *testing.T) {
	for _, raw := range []string{"café", "订单一", "a b", "a:b", "a#1", "a.b", ""} {
		_, err := buildIdemKey(7, raw)
		assert.Errorf(t, err, "%q 不该被当成合法幂等键", raw)
	}
}
