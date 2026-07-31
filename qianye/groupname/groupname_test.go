package groupname

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNormalizeAndEffective 钉死分组名的唯一口径。
//
// 这张表是 commission 与 transfer 两个模块共同遵守的契约:两边各自的
// 归一化函数都只是转调本包,而各自的包内测试会断言这层转调没有被替换回
// 私有实现(见 commission/grouprate_test.go 与 transfer/grouprule_test.go 里的
// "口径一致" 用例)。改这张表等于同时改两个模块算钱与放行的口径。
func TestNormalizeAndEffective(t *testing.T) {
	cases := []struct {
		in            string
		wantNormalize string
		wantEffective string
	}{
		{"vip", "vip", "vip"},
		// 大小写必须折叠:存储层是 ci 排序规则,不折叠就会出现
		// "代码认为是两个分组、数据库认为是同一行"的中间态。
		{"VIP", "vip", "vip"},
		{"Vip", "vip", "vip"},
		{"vIp", "vip", "vip"},
		{"  VIP  ", "vip", "vip"},
		{"\tSVIP\n", "svip", "svip"},
		// 空串:比较键保持空(写入侧据此拒绝),判定口径折叠成 default。
		{"", "", Default},
		{"   ", "", Default},
		{"default", "default", "default"},
		{"DEFAULT", "default", "default"},
		// 兜底通配符与 @self 令牌必须原样穿过,否则规则表里的这两种特殊值
		// 会在归一化之后变成一个普通分组名。
		{"*", "*", "*"},
		{"@self", "@self", "@self"},
		{"@SELF", "@self", "@self"},
		// 非拉丁分组名没有大小写概念,必须原样保留。
		{"内部测试组", "内部测试组", "内部测试组"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.wantNormalize, Normalize(tc.in))
			assert.Equal(t, tc.wantEffective, Effective(tc.in))
		})
	}
}

// TestNormalizeIsIdempotent 归一化必须幂等。
//
// 调用链里同一个分组名会被反复归一(API 层归一一次、写库前再归一一次、
// 判定时又归一一次)。不幂等的话,取决于走了几层就会得到不同的键,
// 而那种缺陷只在某一条调用路径上出现,极难复现。
func TestNormalizeIsIdempotent(t *testing.T) {
	for _, in := range []string{"VIP", " Vip ", "", "  ", "*", "@Self", "内部测试组"} {
		assert.Equal(t, Normalize(in), Normalize(Normalize(in)))
		assert.Equal(t, Effective(in), Effective(Effective(in)))
	}
}
