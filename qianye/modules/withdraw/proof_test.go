package withdraw

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// proof_test.go —— 凭证图片(裁决 3)的回归测试。
//
// 这里守的每一条都是"写对了但接错了/绕过了"就会静默塌掉的东西:
// 魔数判定、路径穿越、越权绑定、一图一单、随单据终结清理、以及 HasProof
// 这份冗余与凭证表之间的一致性。

// ─────────────────────────── 测试用图片字节 ───────────────────────────
//
// 只造出足以通过魔数判定的最小前缀:被测函数刻意不解码图片
// (解码才是解压炸弹的风险面),所以后面是什么内容无关紧要。

var (
	jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("payload")...)
	pngBytes  = append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, []byte("payload")...)
	webpBytes = append([]byte("RIFF\x20\x00\x00\x00WEBP"), []byte("VP8 payload")...)
)

// loadProofConfig 打开一份 fiat + 凭证可用的配置。
//
// 走真实的 config.Load(),于是 config.Path() 指向 t.TempDir() 下的临时文件,
// proofDir() 也就自然落在临时目录里 —— 测试之间天然隔离,不需要任何全局替身。
func loadProofConfig(t *testing.T, extra string) {
	t.Helper()
	// 保留期只在 extra 没给的时候才补默认值。
	//
	// config 的 YAML 解析是严格模式(KnownFields + 重复键报错),这是刻意的、
	// 另有测试钉着。所以夹具本身绝不能拼出重复键 —— 否则想覆盖保留期的用例
	// 拿到的是一个解析失败的错误,而不是它想测的那个配置。
	retention := "  pii_retention_days: 180\n"
	if strings.Contains(extra, "pii_retention_days:") {
		retention = ""
	}
	loadTestConfigYAML(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
withdraw:
  enabled: true
  methods: ["quota", "fiat"]
  pii_key: "`+testPIIKeyA+`"
  digest_key: "digest-secret"
`+retention+extra)
}

func proofFullPath(t *testing.T, storedName string) string {
	t.Helper()
	rel, ok := proofRelPath(storedName)
	require.True(t, ok, "被测数据本身就有一个非法文件名: %q", storedName)
	return filepath.Join(proofDir(), rel)
}

// seedProof 造一张已绑定到某张单据的凭证,并真的在磁盘上放一个文件。
//
// 文件必须真的存在:清理路径的核心断言是"磁盘上没有它了",
// 只建库行的话那条断言从一开始就成立,是一条永真测试。
func seedProof(t *testing.T, gdb *gorm.DB, w *Withdrawal, createdAt int64) *Proof {
	t.Helper()
	name, err := newProofStoredName(proofJPEG.Ext)
	require.NoError(t, err)
	require.NoError(t, writeProofFile(name, jpegBytes))

	p := &Proof{
		Ref:          "pf-" + name[:8],
		UserId:       w.UserId,
		WithdrawalId: w.Id,
		WithdrawNo:   w.WithdrawNo,
		StoredName:   name,
		MimeType:     proofJPEG.Mime,
		Size:         int64(len(jpegBytes)),
		CreatedAt:    createdAt,
		BoundAt:      createdAt,
	}
	require.NoError(t, gdb.Create(p).Error)
	forceCreatedAt(t, gdb, &Proof{}, p.Id, createdAt)
	p.CreatedAt = createdAt
	return p
}

// seedPendingProof 造一张还没被任何单据认领的凭证(孤儿候选)。
func seedPendingProof(t *testing.T, gdb *gorm.DB, userId int, ref string, createdAt int64) *Proof {
	t.Helper()
	name, err := newProofStoredName(proofPNG.Ext)
	require.NoError(t, err)
	require.NoError(t, writeProofFile(name, pngBytes))

	p := &Proof{
		Ref: ref, UserId: userId, StoredName: name,
		MimeType: proofPNG.Mime, Size: int64(len(pngBytes)), CreatedAt: createdAt,
	}
	require.NoError(t, gdb.Create(p).Error)
	forceCreatedAt(t, gdb, &Proof{}, p.Id, createdAt)
	p.CreatedAt = createdAt
	return p
}

func reloadProof(t *testing.T, gdb *gorm.DB, ref string) Proof {
	t.Helper()
	var p Proof
	require.NoError(t, gdb.Where("ref = ?", ref).Take(&p).Error)
	return p
}

// ─────────────────────────── 魔数判定 ───────────────────────────

// 类型判定必须只认魔数。扩展名与 Content-Type 都是调用方随手写的字符串,
// 按它们判定等于让上传者自己决定文件被存成什么。
func TestSniffProof(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string // "" 表示应当被拒绝
	}{
		{"jpeg", jpegBytes, "image/jpeg"},
		{"png", pngBytes, "image/png"},
		{"webp", webpBytes, "image/webp"},
		{"空内容", nil, ""},
		{"gif 不在白名单", []byte("GIF89a............"), ""},
		{"svg 是可执行脚本载体", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script/></svg>`), ""},
		{"html 伪装", []byte("<!doctype html><script>alert(1)</script>"), ""},
		{"zip / office 文档", []byte("PK\x03\x04............"), ""},
		{"jpeg 只有两个字节", []byte{0xFF, 0xD8}, ""},
		{"png 魔数被截断", []byte{0x89, 'P', 'N', 'G'}, ""},
		{"RIFF 但不是 WEBP(wav)", []byte("RIFF\x20\x00\x00\x00WAVEfmt "), ""},
		{"RIFF 头后不足 12 字节", []byte("RIFF\x20\x00\x00\x00"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, ok := sniffProof(tc.in)
			if tc.want == "" {
				assert.False(t, ok, "不该被当成图片接受,却判成了 %s", kind.Mime)
				return
			}
			require.True(t, ok)
			assert.Equal(t, tc.want, kind.Mime)
		})
	}
}

// ─────────────────────────── 路径穿越 ───────────────────────────

// 落盘文件名是服务端生成的,那为什么还要在拼路径前校验一次形状?
// 因为 filepath.Join("dir", "../../etc/passwd") 会老老实实地跳出目录 ——
// 只要别处出一个洞(SQL 注入、DBA 手工改行),这里就变成任意文件读写。
func TestProofRelPath_RejectsAnythingButServerGeneratedNames(t *testing.T) {
	valid := "0123456789abcdef0123456789abcdef.jpg"
	rel, ok := proofRelPath(valid)
	require.True(t, ok)
	assert.Equal(t, filepath.Join("01", valid), rel, "必须按前两位分片,否则单目录会堆满文件")

	for _, bad := range []string{
		"",
		"../../etc/passwd",
		"../0123456789abcdef0123456789abcdef.jpg",
		"0123456789abcdef0123456789abcdef.jpg/../../x",
		"..%2f0123456789abcdef0123456789abcdef.jpg",
		"0123456789abcdef0123456789abcdef",                  // 没有扩展名
		"0123456789abcdef0123456789abcdef.php",              // 扩展名不在白名单
		"0123456789abcdef0123456789abcdef.jpg.php",          // 双扩展名
		"0123456789abcdef0123456789abcde.jpg",               // 少一位
		"0123456789abcdef0123456789abcdeff.jpg",             // 多一位
		"0123456789ABCDEF0123456789ABCDEF.jpg",              // 大写(生成侧只产小写)
		"0123456789abcdef0123456789abcdeg.jpg",              // 非十六进制
		"/etc/0123456789abcdef0123456789abcdef.jpg",         // 绝对路径
		"sub/dir/0123456789abcdef0123456789abcdef.jpg",      // 带目录
		"0123456789abcdef0123456789abcdef.jpg\x00extra.php", // NUL 截断
	} {
		_, ok := proofRelPath(bad)
		assert.False(t, ok, "非法文件名被放行: %q", bad)
	}
}

// removeProofFile 拿到形状非法的行时必须【什么都不删】。
// 猜错的方向是删掉别人的文件 —— 那比留一个孤儿文件严重得多。
func TestRemoveProofFile_NeverGuessesAtMalformedNames(t *testing.T) {
	loadProofConfig(t, "")
	victim := filepath.Join(proofDir(), "keep-me.txt")
	require.NoError(t, os.MkdirAll(proofDir(), 0o700))
	require.NoError(t, os.WriteFile(victim, []byte("x"), 0o600))

	require.NoError(t, removeProofFile("../keep-me.txt"))
	require.NoError(t, removeProofFile("keep-me.txt"))
	assert.FileExists(t, victim, "形状非法的文件名不该被拿去拼路径删除")

	// 文件本来就不存在不算失败:清理要的是"磁盘上没有它"。
	name, err := newProofStoredName(proofJPEG.Ext)
	require.NoError(t, err)
	assert.NoError(t, removeProofFile(name))
}

// ─────────────────────────── 上传 ───────────────────────────

// uploadProof 发一次真实的 multipart 请求,拿到响应码与 body。
//
// 直接调 acceptProofUpload 而不是走 gin,是因为大小闸门(MaxBytesReader)与
// 表单解析都长在 *http.Request 上 —— 用一个手工构造的 gin.Context 测不到它们。
func uploadProof(t *testing.T, userId int, filename string, content []byte) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = fw.Write(content)
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/qy/withdraw/proofs", &buf)
	c.Request.Header.Set("Content-Type", mw.FormDataContentType())

	row, err := acceptProofUpload(c, userId)
	if err != nil {
		respondErr(c, err)
		return rec.Code, rec.Body.String()
	}
	return http.StatusOK, row.Ref
}

// newProofEnv 建库、接到 db.Get(),并返回那个句柄。
//
// 上传路径全程走 db.Get() 自取句柄(与本模块其余部分一致),所以测试必须
// 真的把测试库接上去,而不是把 *gorm.DB 传进被测函数 —— 后者测的是一条
// 生产代码里不存在的调用形态。
func newProofEnv(t *testing.T) *gorm.DB {
	t.Helper()
	gdb := newTestDB(t)
	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
	})
	return gdb
}

// 上传路径的四条闸门:类型、大小、开关、待用数量。
//
// 每一条都要单独有断言:合并成"上传失败"之后,用户能做的事完全不同
// (换一张图 / 压缩 / 找管理员 / 先把已传的用掉),而调用方无从区分。
func TestAcceptProofUpload(t *testing.T) {
	t.Run("接受 JPEG 并按服务端文件名落盘", func(t *testing.T) {
		loadProofConfig(t, "")
		gdb := newProofEnv(t)

		code, ref := uploadProof(t, 7, "我的身份证.jpg.php", jpegBytes)
		require.Equal(t, http.StatusOK, code, ref)

		p := reloadProof(t, gdb, ref)
		assert.Equal(t, proofJPEG.Mime, p.MimeType)
		assert.Equal(t, int64(len(jpegBytes)), p.Size)
		assert.Zero(t, p.WithdrawalId, "刚上传的凭证不该已经属于任何一张单")
		assert.FileExists(t, proofFullPath(t, p.StoredName))

		// 用户提供的文件名不能出现在落盘名里的任何位置,也不能留在库里。
		assert.NotContains(t, p.StoredName, "身份证")
		assert.NotContains(t, p.StoredName, "php")
		assert.True(t, strings.HasSuffix(p.StoredName, ".jpg"),
			"扩展名必须由魔数判定给出,而不是沿用请求里的 %q", "我的身份证.jpg.php")
	})

	t.Run("按魔数拒绝伪装成图片的内容", func(t *testing.T) {
		loadProofConfig(t, "")
		gdb := newProofEnv(t)

		code, body := uploadProof(t, 7, "photo.png", []byte("<?php system($_GET[0]); ?>"))
		assert.Equal(t, http.StatusBadRequest, code)
		assert.Contains(t, body, "qy_wd_proof_type")
		assert.Empty(t, listProofRefs(t, gdb), "被拒绝的上传不该留下任何元数据行")
		assert.Empty(t, proofFilesOnDisk(t), "被拒绝的上传不该在磁盘上留下任何文件")
	})

	// 两条超限路径分别由两道不同的闸门挡下,必须各测一次:
	// 小超限被 fh.Size 拦(请求体没到 MaxBytesReader 的阈值),
	// 大超限被请求体上界拦(那一道才是防"2 GiB 的 POST 吃光内存"的)。
	t.Run("略超 proof_max_bytes 被文件大小闸门拒绝", func(t *testing.T) {
		loadProofConfig(t, "  proof_max_bytes: 1024\n")
		gdb := newProofEnv(t)

		big := append(append([]byte{}, jpegBytes...), bytes.Repeat([]byte("A"), 2048)...)
		code, body := uploadProof(t, 7, "big.jpg", big)
		assert.Equal(t, http.StatusRequestEntityTooLarge, code)
		assert.Contains(t, body, "qy_wd_proof_too_large")
		assert.Empty(t, listProofRefs(t, gdb))
		assert.Empty(t, proofFilesOnDisk(t), "被拒绝的上传不该在磁盘上留下任何文件")
	})

	t.Run("远超上限的请求体被整体拒收", func(t *testing.T) {
		loadProofConfig(t, "  proof_max_bytes: 1024\n")
		gdb := newProofEnv(t)

		huge := append(append([]byte{}, jpegBytes...), bytes.Repeat([]byte("A"), 3<<20)...)
		code, body := uploadProof(t, 7, "huge.jpg", huge)
		assert.Equal(t, http.StatusRequestEntityTooLarge, code)
		assert.Contains(t, body, "qy_wd_proof_too_large")
		assert.Empty(t, listProofRefs(t, gdb))
		assert.Empty(t, proofFilesOnDisk(t))
	})

	t.Run("proof_enabled=false 时拒绝", func(t *testing.T) {
		loadProofConfig(t, "  proof_enabled: false\n")
		newProofEnv(t)

		code, body := uploadProof(t, 7, "a.jpg", jpegBytes)
		assert.Equal(t, http.StatusBadRequest, code)
		assert.Contains(t, body, "qy_wd_proof_disabled")
	})

	// 凭证只服务于法币打款:quota-only 的站点不该开始往磁盘上收 PII 图片。
	t.Run("站点没开 fiat 时拒绝", func(t *testing.T) {
		loadTestConfigYAML(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
withdraw:
  enabled: true
  methods: ["quota"]
`)
		newProofEnv(t)

		code, body := uploadProof(t, 7, "a.jpg", jpegBytes)
		assert.Equal(t, http.StatusBadRequest, code)
		assert.Contains(t, body, "qy_wd_proof_disabled")
	})

	// 没有这道闸门,一个登录用户可以"只上传、不提交"把磁盘打满 ——
	// 提现单自己的那几道闸门(daily_max_count / max_pending_orders)一个都拦不到,
	// 因为这条路径压根没有创建单据。
	t.Run("未使用的上传有数量上限", func(t *testing.T) {
		loadProofConfig(t, "")
		newProofEnv(t)

		for i := 0; i < proofPendingMax; i++ {
			code, ref := uploadProof(t, 7, "a.jpg", jpegBytes)
			require.Equal(t, http.StatusOK, code, ref)
		}
		code, body := uploadProof(t, 7, "a.jpg", jpegBytes)
		assert.Equal(t, http.StatusBadRequest, code)
		assert.Contains(t, body, "qy_wd_proof_pending_limit")

		// 上限是按人算的:另一个用户不该被前一个人的上传挡住。
		code, ref := uploadProof(t, 8, "a.jpg", jpegBytes)
		assert.Equal(t, http.StatusOK, code, ref)
	})
}

// ─────────────────────────── 绑定 ───────────────────────────

// bindProof 的四个 WHERE 条件各守一件事,少一个就是一个独立的缺陷:
// 越权、重复认领、引用已清理的凭证。
func TestBindProof(t *testing.T) {
	loadProofConfig(t, "")
	gdb := newTestDB(t)
	now := common.GetTimestamp()

	w := seedWithdrawal(t, gdb, "WD-bind", func(x *Withdrawal) { x.UserId = 7 })
	other := seedWithdrawal(t, gdb, "WD-other", func(x *Withdrawal) { x.UserId = 7 })

	mine := seedPendingProof(t, gdb, 7, "pf-mine", now)
	foreign := seedPendingProof(t, gdb, 99, "pf-foreign", now)
	purged := seedPendingProof(t, gdb, 7, "pf-purged", now)
	require.NoError(t, gdb.Model(&Proof{}).Where("id = ?", purged.Id).
		UpdateColumn("purged_at", now).Error)

	t.Run("本人的未使用凭证可以绑定", func(t *testing.T) {
		require.NoError(t, bindProof(gdb, w, mine.Ref))
		got := reloadProof(t, gdb, mine.Ref)
		assert.Equal(t, w.Id, got.WithdrawalId)
		assert.Equal(t, w.WithdrawNo, got.WithdrawNo)
		assert.NotZero(t, got.BoundAt)
	})

	t.Run("一张凭证只能被一张单认领", func(t *testing.T) {
		assert.ErrorIs(t, bindProof(gdb, other, mine.Ref), errProofNotFound)
		assert.Equal(t, w.Id, reloadProof(t, gdb, mine.Ref).WithdrawalId, "原绑定被改写了")
	})

	t.Run("拒绝绑定别人的凭证", func(t *testing.T) {
		assert.ErrorIs(t, bindProof(gdb, w, foreign.Ref), errProofNotFound)
		assert.Zero(t, reloadProof(t, gdb, foreign.Ref).WithdrawalId, "越权绑定成功了")
	})

	t.Run("拒绝引用已清理的凭证", func(t *testing.T) {
		assert.ErrorIs(t, bindProof(gdb, other, purged.Ref), errProofNotFound)
	})

	t.Run("不存在的 ref", func(t *testing.T) {
		assert.ErrorIs(t, bindProof(gdb, other, "pf-nope"), errProofNotFound)
	})
}

// HasProof 是一份冗余,冗余就有漂移风险。它唯一的安全依据是:
// 绑定 CAS 失败会让【整笔申请】回滚,于是"HasProof=true 却没有凭证"不可能落库。
// 这条测试钉的就是那个回滚 —— 它是 has_proof 这个字段能被信任的全部理由。
func TestSubmitInTx_BadProofRefRollsBackTheWholeOrder(t *testing.T) {
	loadProofConfig(t, "")
	gdb := newTestDB(t)

	w := &Withdrawal{
		WithdrawNo: "WD-rollback", IdemScope: idemScope, IdemKey: idemKeyOf(7, "cli-rollback"),
		UserId: 7, Method: config.WithdrawMethodFiat, Status: StatusPending,
		Quota: 500000, HasProof: true,
		CreatedAt: common.GetTimestamp(), UpdatedAt: common.GetTimestamp(),
	}
	acc := acceptedRequest{IdemKey: "cli-rollback", Method: config.WithdrawMethodFiat,
		Quota: 500000, ProofRef: "pf-does-not-exist"}

	err := gdb.Transaction(func(tx *gorm.DB) error {
		_, txErr := submitInTx(tx, w, nil, acc, config.Get().Withdraw, "u7")
		return txErr
	})
	require.ErrorIs(t, err, errProofNotFound)

	var cnt int64
	require.NoError(t, gdb.Model(&Withdrawal{}).Where("withdraw_no = ?", "WD-rollback").
		Count(&cnt).Error)
	assert.Zero(t, cnt, "凭证绑定失败,单据却落库了 —— has_proof 从此与凭证表脱节")
}

// ─────────────────────────── 受理校验 ───────────────────────────

func TestAcceptCreate_ProofRef(t *testing.T) {
	base := createRequest{
		ClientRequestId: "client-request-0001",
		Quota:           500000,
		PayeeChannel:    ChannelAlipay,
		Payee:           map[string]string{"real_name": "张三", "account": "13800000000"},
	}

	t.Run("fiat 带合法 ref 通过", func(t *testing.T) {
		loadProofConfig(t, "")
		req := base
		req.Method = config.WithdrawMethodFiat
		req.ProofRef = "  pf-abc  "
		acc, err := acceptCreate(req, config.Get().Withdraw)
		require.NoError(t, err)
		assert.Equal(t, "pf-abc", acc.ProofRef, "ref 必须去掉首尾空白后再进 WHERE")
	})

	t.Run("不带 ref 也通过 —— 需求写的就是「可选」", func(t *testing.T) {
		loadProofConfig(t, "")
		req := base
		req.Method = config.WithdrawMethodFiat
		acc, err := acceptCreate(req, config.Get().Withdraw)
		require.NoError(t, err)
		assert.Empty(t, acc.ProofRef)
	})

	// 静默忽略的话那张图会永远停在"未绑定",用户以为交了、审核看不到,
	// 最后被孤儿清理悄悄删掉 —— 没有任何一方会发现。
	t.Run("quota 单带 ref 必须报错而不是静默忽略", func(t *testing.T) {
		loadProofConfig(t, "")
		req := base
		req.Method = config.WithdrawMethodQuota
		req.ProofRef = "pf-abc"
		_, err := acceptCreate(req, config.Get().Withdraw)
		assert.ErrorIs(t, err, errProofDisabled)
	})

	t.Run("proof_enabled=false 时带 ref 报错", func(t *testing.T) {
		loadProofConfig(t, "  proof_enabled: false\n")
		req := base
		req.Method = config.WithdrawMethodFiat
		req.ProofRef = "pf-abc"
		_, err := acceptCreate(req, config.Get().Withdraw)
		assert.ErrorIs(t, err, errProofDisabled)
	})

	// 不设上界意味着一个 1 MiB 的字符串会被原样发给数据库做比较。
	t.Run("超长 ref 在进数据库之前就被拒", func(t *testing.T) {
		loadProofConfig(t, "")
		req := base
		req.Method = config.WithdrawMethodFiat
		req.ProofRef = strings.Repeat("a", 65)
		_, err := acceptCreate(req, config.Get().Withdraw)
		assert.ErrorIs(t, err, errProofNotFound)
	})
}

// ─────────────────────────── 清理 ───────────────────────────

// 三条到期口径必须各自成立,而且都要真的把【磁盘文件】删掉 ——
// 只把 purged_at 置位的话,PII 一张不少地留在盘上,而日志显示一切正常。
func TestPruneProofs(t *testing.T) {
	loadProofConfig(t, "")
	gdb := newTestDB(t)
	now := common.GetTimestamp()

	rejected := seedWithdrawal(t, gdb, "WD-rej", func(w *Withdrawal) { w.Status = StatusRejected })
	cancelled := seedWithdrawal(t, gdb, "WD-can", func(w *Withdrawal) { w.Status = StatusCancelled })
	failed := seedWithdrawal(t, gdb, "WD-fail", func(w *Withdrawal) { w.Status = StatusFailed })
	paidOld := seedWithdrawal(t, gdb, "WD-paid-old", func(w *Withdrawal) { w.Status = StatusPaid })
	paidNew := seedWithdrawal(t, gdb, "WD-paid-new", func(w *Withdrawal) { w.Status = StatusPaid })
	pending := seedWithdrawal(t, gdb, "WD-pending", nil)

	proofs := map[string]*Proof{
		"rejected":  seedProof(t, gdb, rejected, now-3600),
		"cancelled": seedProof(t, gdb, cancelled, now-3600),
		"failed":    seedProof(t, gdb, failed, now-3600),
		"paid-old":  seedProof(t, gdb, paidOld, now-181*86400),
		"paid-new":  seedProof(t, gdb, paidNew, now-10*86400),
		"pending":   seedProof(t, gdb, pending, now-181*86400),
		"orphan":    seedPendingProof(t, gdb, 7, "pf-orphan", now-25*3600),
		"fresh":     seedPendingProof(t, gdb, 7, "pf-fresh", now-3600),
	}

	pruneProofs(context.Background(), gdb, 100)

	purged := []string{"rejected", "cancelled", "failed", "paid-old", "orphan"}
	kept := []string{"paid-new", "pending", "fresh"}
	for _, k := range purged {
		p := proofs[k]
		assert.NotZero(t, reloadProof(t, gdb, p.Ref).PurgedAt, "%s 应被清理", k)
		assert.NoFileExists(t, proofFullPath(t, p.StoredName), "%s 的文件仍在磁盘上", k)
	}
	for _, k := range kept {
		p := proofs[k]
		assert.Zero(t, reloadProof(t, gdb, p.Ref).PurgedAt, "%s 不该被清理", k)
		assert.FileExists(t, proofFullPath(t, p.StoredName), "%s 的文件被误删了", k)
	}
}

// 保留期(pii_retention_days)只管【已打款】那一条。
// 孤儿与失败单的凭证不受它控制 —— 挂在同一个开关上的话,
// 运维填一个负数关掉保留期的同时会打开一个磁盘泄漏。
func TestPruneProofs_RetentionSwitchOnlyGovernsPaidOrders(t *testing.T) {
	loadProofConfig(t, "  pii_retention_days: -1\n")
	gdb := newTestDB(t)
	now := common.GetTimestamp()

	paid := seedWithdrawal(t, gdb, "WD-paid", func(w *Withdrawal) { w.Status = StatusPaid })
	rejected := seedWithdrawal(t, gdb, "WD-rej", func(w *Withdrawal) { w.Status = StatusRejected })
	paidProof := seedProof(t, gdb, paid, now-400*86400)
	rejProof := seedProof(t, gdb, rejected, now-3600)
	orphan := seedPendingProof(t, gdb, 7, "pf-orphan", now-25*3600)

	pruneProofs(context.Background(), gdb, 100)

	assert.Zero(t, reloadProof(t, gdb, paidProof.Ref).PurgedAt,
		"保留期已关闭,已打款单的凭证不该被清")
	assert.NotZero(t, reloadProof(t, gdb, rejProof.Ref).PurgedAt,
		"被拒绝单据的凭证与保留期开关无关,必须清")
	assert.NotZero(t, reloadProof(t, gdb, orphan.Ref).PurgedAt,
		"孤儿文件与保留期开关无关,必须清")
}

// 与 prunePii / prunePayeeAccounts 同一个坑:不可清的行如果被捞进 batch 再丢掉,
// 一批 id 更小的行(比如挂了半年的待审单的凭证)就能把每一轮 batch 占满,
// 后面所有到期行从此再也轮不到 —— 清理任务看起来在跑,实际一张都清不掉。
func TestPruneProofs_LiveOrdersDoNotBlockTheBatch(t *testing.T) {
	loadProofConfig(t, "")
	gdb := newTestDB(t)
	now := common.GetTimestamp()

	// 三张待审单的凭证先落库(id 更小,排在最前),再放一张该清的。
	for i := 0; i < 3; i++ {
		w := seedWithdrawal(t, gdb, "WD-live-"+idOf(i), nil)
		seedProof(t, gdb, w, now-400*86400)
	}
	rejected := seedWithdrawal(t, gdb, "WD-rej", func(w *Withdrawal) { w.Status = StatusRejected })
	target := seedProof(t, gdb, rejected, now-3600)

	pruneProofs(context.Background(), gdb, 2)

	assert.NotZero(t, reloadProof(t, gdb, target.Ref).PurgedAt,
		"到期凭证被前面一批永远不可清的行挤出了 batch")
}

// 失去租约后必须立刻停手,否则会与接管节点双跑。
func TestPruneProofs_SkipsWhenCancelled(t *testing.T) {
	loadProofConfig(t, "")
	gdb := newTestDB(t)

	orphan := seedPendingProof(t, gdb, 7, "pf-orphan", common.GetTimestamp()-25*3600)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pruneProofs(ctx, gdb, 100)

	assert.Zero(t, reloadProof(t, gdb, orphan.Ref).PurgedAt, "失去租约后不该继续清理")
	assert.FileExists(t, proofFullPath(t, orphan.StoredName))
}

// 先删文件、再标记 purged。反过来的话,中途崩溃会留下一个 purged_at 已置位却
// 仍躺在磁盘上的文件 —— 它从此不会再被任何一轮扫到,PII 永远留在盘上。
func TestPurgeProofBatch_MarksOnlyWhatItActuallyDeleted(t *testing.T) {
	loadProofConfig(t, "")
	gdb := newTestDB(t)
	now := common.GetTimestamp()

	good := seedPendingProof(t, gdb, 7, "pf-good", now-25*3600)
	broken := seedPendingProof(t, gdb, 7, "pf-broken", now-25*3600)
	// 把文件名改成形状非法的值,模拟被手工改坏的行:removeProofFile 会拒绝去猜,
	// 但整批清理不能因此卡死,其余行照常收掉。
	require.NoError(t, gdb.Model(&Proof{}).Where("id = ?", broken.Id).
		UpdateColumn("stored_name", "../escape.jpg").Error)

	pruneProofs(context.Background(), gdb, 100)

	assert.NotZero(t, reloadProof(t, gdb, good.Ref).PurgedAt)
	assert.NoFileExists(t, proofFullPath(t, good.StoredName))
	assert.NotZero(t, reloadProof(t, gdb, broken.Ref).PurgedAt,
		"形状非法的行删不出文件,但也不该让它永远卡在扫描集合里")
}

// ─────────────────────────── 路由 ───────────────────────────

// 三条新路由缺一不可:少了上传,凭证功能只是后端的一堆死代码;
// 少了下载,图片存进去就再也拿不出来。
func TestProofRoutesAreMounted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Mod{}.RegisterUserRoutes(engine.Group("/api/qy"))
	Mod{}.RegisterAdminRoutes(engine.Group("/api/qy/admin"))

	registered := map[string]bool{}
	for _, r := range engine.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, want := range []string{
		"POST /api/qy/withdraw/proofs",
		"GET /api/qy/withdraw/:id/proof",
		"GET /api/qy/admin/withdraw/:id/proof",
	} {
		assert.True(t, registered[want], "缺少路由 %s", want)
	}
}

// Proof 必须进 Tables():漏了它,AutoMigrate 不会建表,
// 而全部上传会在运行时才炸 —— 而且只在真正开了 fiat 的站点上炸。
func TestProofTableIsMigrated(t *testing.T) {
	names := make([]string, 0, 6)
	for _, m := range (Mod{}).Tables() {
		if t, ok := m.(interface{ TableName() string }); ok {
			names = append(names, t.TableName())
		}
	}
	assert.Contains(t, names, "qy_withdrawal_proofs")
}

// ─────────────────────────── 测试辅助 ───────────────────────────

func listProofRefs(t *testing.T, gdb *gorm.DB) []string {
	t.Helper()
	var refs []string
	require.NoError(t, gdb.Model(&Proof{}).Order("id asc").Pluck("ref", &refs).Error)
	return refs
}

// proofFilesOnDisk 列出凭证目录下的全部文件(含分片子目录)。
func proofFilesOnDisk(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(proofDir(), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// 目录还不存在就是"一个文件都没有"。
			if os.IsNotExist(err) {
				return filepath.SkipAll
			}
			return err
		}
		if !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		require.NoError(t, err)
	}
	return out
}
