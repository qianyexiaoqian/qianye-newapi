package common

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudflareSnapshotPassesItsOwnValidator 守「内置快照不是随手粘的」。
//
// 快照是 `TRUSTED_PROXIES=cloudflare` 这一档的默认信任面。它同时必须过
// validateCloudflarePrefixes —— 否则一份合法的外部文件反而比内置默认更严,
// 而那说明校验器与快照对不上,两边总有一个是错的。
func TestCloudflareSnapshotPassesItsOwnValidator(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES_CLOUDFLARE_FILE", "")
	prefixes, err := CloudflarePrefixes()
	require.NoError(t, err)
	require.NoError(t, validateCloudflarePrefixes(prefixes))
	assert.Equal(t, len(cloudflareSnapshotV4)+len(cloudflareSnapshotV6), len(prefixes))

	// 快照必须同时含 v4 与 v6:只有 v4 的话,走 IPv6 回源的 CF 边缘节点
	// 会被当成不受信对端,那部分流量的客户端 IP 会全部退化成边缘地址 ——
	// 而这件事在监控上看起来一切正常。
	var v4, v6 int
	for _, raw := range prefixes {
		prefix, err := netip.ParsePrefix(raw)
		require.NoError(t, err)
		if prefix.Addr().Is4() {
			v4++
			continue
		}
		v6++
	}
	assert.Positive(t, v4)
	assert.Positive(t, v6)
}

// TestParseCloudflarePrefixFileAcceptsTheVendorFormat 守文件档能直接吃
// `curl https://www.cloudflare.com/ips-v4` 的原样输出。
//
// 这一点是文件档存在的全部理由:运维 cron 一条 curl 落地成文件就完事,
// 不需要再写一个转换脚本 —— 多一步转换就多一个会写坏的地方。
func TestParseCloudflarePrefixFileAcceptsTheVendorFormat(t *testing.T) {
	prefixes, err := ParseCloudflarePrefixFile(
		"# fetched 2026-08-22\n173.245.48.0/20\n\n104.16.0.0/13\n2400:cb00::/32\n")
	require.NoError(t, err)
	assert.Equal(t, []string{"173.245.48.0/20", "104.16.0.0/13", "2400:cb00::/32"}, prefixes)
}

// TestValidateCloudflarePrefixesRejectsWidenedTrust 是这一档的 blocker。
//
// 文件档把一条安全判据的取值放到了进程外的一个文件上。它被写坏(或被换掉)
// 之后的失败方向必须是**启动失败**,而不是"信任面悄悄变宽了但服务照常在跑" ——
// 后者不会让任何一处报错,而它意味着任何人都能对任意令牌伪造 allow_ips
// 通过的来源 IP。
func TestValidateCloudflarePrefixesRejectsWidenedTrust(t *testing.T) {
	testCases := []struct {
		name     string
		prefixes []string
	}{
		{name: "empty list", prefixes: nil},
		{name: "everything", prefixes: []string{"0.0.0.0/0"}},
		{name: "everything IPv6", prefixes: []string{"::/0"}},
		{name: "loopback", prefixes: []string{"127.0.0.0/8"}},
		{name: "IPv6 loopback", prefixes: []string{"::1/128"}},
		{name: "RFC 1918", prefixes: []string{"10.0.0.0/8"}},
		{name: "IPv6 unique local", prefixes: []string{"fc00::/7"}},
		// 窄到宽度校验管不着的私网段。这两条单独存在,是因为上面那两条
		// 同时会被"过宽"那一档拦下 —— 只有它们能证明私网判据本身还在。
		{name: "narrow RFC 1918", prefixes: []string{"10.1.2.0/24"}},
		{name: "narrow 192.168 range", prefixes: []string{"192.168.5.0/24"}},
		{name: "narrow IPv6 unique local", prefixes: []string{"fd00:1234::/32"}},
		// 同理:回环那一档也要一条宽度校验管不着的。
		{name: "narrow loopback", prefixes: []string{"127.0.0.1/32"}},
		{name: "link local", prefixes: []string{"169.254.0.0/16"}},
		{name: "multicast", prefixes: []string{"224.0.0.0/4"}},
		{name: "wider than a /12", prefixes: []string{"104.0.0.0/8"}},
		{name: "IPv6 wider than a /24", prefixes: []string{"2000::/16"}},
		{name: "not a CIDR", prefixes: []string{"104.16.0.0"}},
		{name: "garbage", prefixes: []string{"not-a-range"}},
		{
			// 一条坏的就整份拒绝。部分接受会得到一个"看起来在跑、
			// 实际信任面已经变了"的进程 —— 那是最坏的一种结局。
			name:     "one bad entry among good ones",
			prefixes: []string{"104.16.0.0/13", "10.0.0.0/8"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Error(t, validateCloudflarePrefixes(testCase.prefixes))
		})
	}
}

// TestCloudflarePrefixesFileOverride 走完整通路:文件 → 策略 → 取值。
//
// 只测解析器是不够的:这一档真正的问题是"文件配了但没接上",而那正是
// 本仓反复出现的形状(能填、能存、能回读,线上永不生效)。
func TestCloudflarePrefixesFileOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cf-ips.txt")
	require.NoError(t, os.WriteFile(path, []byte("# fetched by cron\n192.0.2.0/24\n"), 0o600))

	t.Setenv("TRUSTED_PROXIES", "cloudflare")
	t.Setenv("CLIENT_IP_HEADERS", "")
	t.Setenv("BIND_ADDRESS", "")
	t.Setenv("TRUSTED_PROXIES_CLOUDFLARE_FILE", path)

	policy, err := BuildClientIPPolicy()
	require.NoError(t, err)
	require.Len(t, policy.Sources, 1)
	assert.Equal(t, []string{"192.0.2.0/24"}, policy.Sources[0].CIDRStrings())

	// 文件里的网段生效了。
	resolution := ResolveClientIP(policy, newClientIPRequest("192.0.2.9:41000",
		map[string][]string{"CF-Connecting-IP": {"203.0.113.5"}}))
	assert.Equal(t, "203.0.113.5", resolution.IP)
	assert.Equal(t, "cloudflare", resolution.TrustSource)

	// 而内置快照的网段**不再**受信 —— 文件是替代,不是叠加。
	// 叠加语义会让"把清单收窄"这个动作变成不可能。
	fallback := ResolveClientIP(policy, newClientIPRequest("172.68.1.1:41000",
		map[string][]string{"CF-Connecting-IP": {"203.0.113.5"}}))
	assert.Equal(t, "172.68.1.1", fallback.IP)
	assert.False(t, fallback.PeerTrusted)
}

// TestCloudflarePrefixesFileFailuresStopStartup 守失败方向。
//
// 文件不存在 / 内容非法都必须让 BuildClientIPPolicy 报错(main.go 据此
// FatalLog 停机)。静默回退到内置快照是更糟的选择:运维以为自己在用最新清单,
// 实际用的是编译进去的旧快照,而这件事没有任何一处会说。
func TestCloudflarePrefixesFileFailuresStopStartup(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad.txt")
	require.NoError(t, os.WriteFile(badPath, []byte("10.0.0.0/8\n"), 0o600))

	for _, testCase := range []struct {
		name string
		path string
	}{
		{name: "missing file", path: filepath.Join(dir, "does-not-exist.txt")},
		{name: "private range inside the file", path: badPath},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("TRUSTED_PROXIES", "cloudflare")
			t.Setenv("CLIENT_IP_HEADERS", "")
			t.Setenv("BIND_ADDRESS", "")
			t.Setenv("TRUSTED_PROXIES_CLOUDFLARE_FILE", testCase.path)
			_, err := BuildClientIPPolicy()
			assert.Error(t, err)
		})
	}
}
