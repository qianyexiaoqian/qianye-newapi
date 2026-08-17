package lottery

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// cover_upload_test.go —— 上传那一路的三道闸门:类型、大小、待用配额,
// 外加一条路径穿越的钉子。
//
// 落盘管线本身住在 qianye/service/imagestore(与工单截图、提现凭证同一份),
// 这里**不重测它的内部实现**,只钉住"本模块确实把每一道闸门接上了、
// 而且接的是自己那份配置" —— 那正是复用一份实现时唯一会漏的地方。

// uploadCover 发一次真实的 multipart 请求。
//
// 直接调 acceptCoverUpload 而不是走完整的 gin 路由:大小闸门(MaxBytesReader)
// 与表单解析都长在 *http.Request 上,用一个手工塞好字段的 gin.Context 测不到它们。
func uploadCover(t *testing.T, adminId int, filename string, content []byte) (*Cover, error) {
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
	c.Request = httptest.NewRequest(http.MethodPost, "/api/qy/admin/lottery/covers", &buf)
	c.Request.Header.Set("Content-Type", mw.FormDataContentType())
	return acceptCoverUpload(c, adminId)
}

var (
	coverJPEG = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("payload")...)
	coverWEBP = append([]byte("RIFF\x00\x00\x00\x00WEBP"), []byte("payload")...)
)

// TestAcceptCoverUploadType 锁住"判定的是**内容**,不是扩展名也不是请求头"。
//
// 只看扩展名的后果不是显示问题:落盘的可以是任意二进制,而扩展名由服务端
// 按客户端说法瞎猜 —— 一份伪装成 .png 的 HTML 就能被当成页面取回。
func TestAcceptCoverUploadType(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		content  []byte
		wantMime string
		wantExt  string
		bad      bool
		why      string
	}{
		{name: "PNG", filename: "a.png", content: coverPNG, wantMime: "image/png", wantExt: "png"},
		{name: "JPEG", filename: "a.jpg", content: coverJPEG, wantMime: "image/jpeg", wantExt: "jpg"},
		{name: "WebP", filename: "a.webp", content: coverWEBP, wantMime: "image/webp", wantExt: "webp"},
		{name: "扩展名说是 png,内容是 JPEG —— 按内容判", filename: "a.png", content: coverJPEG,
			wantMime: "image/jpeg", wantExt: "jpg",
			why: "落盘扩展名必须跟着魔数走,否则取回时的 Content-Type 就是客户端说了算"},
		{name: "扩展名说是 png,内容是 HTML —— 拒", filename: "a.png",
			content: []byte("<html><script>alert(1)</script></html>"), bad: true},
		{name: "SVG 拒", filename: "a.svg",
			content: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script/></svg>`), bad: true,
			why: "SVG 是可执行脚本的文档格式,不在白名单里"},
		{name: "GIF 拒", filename: "a.gif",
			content: append([]byte("GIF89a"), []byte("payload")...), bad: true,
			why: "白名单写死在代码里:每一种类型都要有一段魔数判定与一个扩展名"},
		{name: "空文件拒", filename: "a.png", content: nil, bad: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newCoverEnv(t, "")
			row, err := uploadCover(t, 7, tc.filename, tc.content)
			if tc.bad {
				require.Error(t, err, tc.why)
				var cnt int64
				require.NoError(t, gdb.Model(&Cover{}).Count(&cnt).Error)
				assert.Zero(t, cnt, "被拒的上传不该留下任何一行元数据")
				return
			}
			require.NoError(t, err, tc.why)
			assert.Equal(t, tc.wantMime, row.MimeType)
			// 落盘名必须整体是【服务端生成】的形状:32 位小写十六进制 + 白名单
			// 扩展名。断言整体形状而不是"不包含用户给的文件名" —— 后者是一条
			// 概率断言(随机名的末位恰好是 'a' 时,"a.png" 就是它的子串),
			// 十六次里会红一次,而那种用例只会被当成环境抖动重跑过去。
			assert.Regexp(t, `^[0-9a-f]{32}\.`+tc.wantExt+`$`, row.StoredName,
				"落盘名必须由服务端生成、扩展名跟着魔数走")
			assert.True(t, coverOnDisk(t, row.StoredName))
		})
	}
}

// TestAcceptCoverUploadTooLarge 锁住大小闸门读的是**本模块自己的配置**。
//
// 复用一份落盘实现时最容易漏的正是这一步:管线接上了,但传下去的上限用的是
// 另一个模块的旋钮,于是运维改 lottery.cover_max_bytes 毫无效果。
func TestAcceptCoverUploadTooLarge(t *testing.T) {
	gdb := newCoverEnv(t, "  cover_max_bytes: 1024\n")

	ok := append(append([]byte{}, coverPNG...), make([]byte, 1024-len(coverPNG))...)
	require.Len(t, ok, 1024)
	row, err := uploadCover(t, 7, "a.png", ok)
	require.NoError(t, err, "恰好等于上限是合法的")
	assert.NotEmpty(t, row.Ref)

	tooBig := append(append([]byte{}, coverPNG...), make([]byte, 1025-len(coverPNG))...)
	require.Len(t, tooBig, 1025)
	_, err = uploadCover(t, 7, "a.png", tooBig)
	assert.ErrorIs(t, err, errCoverTooLarge, "多一个字节就是超限")

	var cnt int64
	require.NoError(t, gdb.Model(&Cover{}).Count(&cnt).Error)
	assert.Equal(t, int64(1), cnt, "超限的那次不该留下元数据行")
}

// TestAcceptCoverUploadDisabled 锁住总开关只关**上传**这一条路。
func TestAcceptCoverUploadDisabled(t *testing.T) {
	newCoverEnv(t, "  cover_enabled: false\n")
	_, err := uploadCover(t, 7, "a.png", coverPNG)
	assert.ErrorIs(t, err, errCoverDisabled)

	// 外链不受它影响:关掉的是"往本站磁盘写字节",不是"卡片可以有背景图"。
	got, err := normalizeCoverURL("https://cdn.example.com/a.png")
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example.com/a.png", got)
}

// TestAcceptCoverUploadPendingCap 锁住"只传图、不保存活动"那条磁盘泄漏路径的闸门。
//
// 本模块其余全部闸门 —— 活动数上限、奖品总额、关键操作限流 —— 一个都拦不到它,
// 因为它压根没有创建活动。
func TestAcceptCoverUploadPendingCap(t *testing.T) {
	gdb := newCoverEnv(t, "")

	for i := 0; i < coverPendingMax; i++ {
		_, err := uploadCover(t, 7, "a.png", coverPNG)
		require.NoErrorf(t, err, "第 %d 张应当放行", i+1)
	}
	_, err := uploadCover(t, 7, "a.png", coverPNG)
	assert.ErrorIs(t, err, errCoverPending)

	// 闸门是**按上传者**分的:一个管理员把自己的名额用满,不该把别人一起锁死。
	_, err = uploadCover(t, 8, "a.png", coverPNG)
	assert.NoError(t, err)

	// 名额有出口:丢掉一张待用的就能再传一张。没有出口的话,管理员在 24 小时
	// 宽限期到期之前再也传不了图,而提示语让他"先保存活动"——一个他此刻
	// 无法执行的动作。
	var pending Cover
	require.NoError(t, gdb.Where("user_id = ? AND act_id = 0", 7).Take(&pending).Error)
	require.NoError(t, discardPendingCover(7, pending.Ref))
	_, err = uploadCover(t, 7, "a.png", coverPNG)
	assert.NoError(t, err)
}

// TestCoverPathTraversal 钉住"库里那一行被改坏时,读写删都跳不出落盘目录"。
//
// 文件名是服务端生成的,那为什么还要防?因为拼进 filepath.Join 的那一步不该
// 依赖"别处一定没被改坏":数据库行可以被 DBA 手工改、可以被一次 SQL 注入写坏,
// 而 filepath.Join("dir", "../../x") 会老老实实地跳出目录。一次廉价的形状校验
// 换掉一整类"只要别处出一个洞,这里就变成任意文件读写"。
func TestCoverPathTraversal(t *testing.T) {
	gdb := newCoverEnv(t, "")

	outside := filepath.Join(filepath.Dir(coverStore.Dir()), "victim.png")
	require.NoError(t, os.WriteFile(outside, []byte("do-not-touch"), 0o600))

	evil := []string{
		"../victim.png",
		"../../victim.png",
		"..\\victim.png",
		"/etc/passwd",
		"C:\\Windows\\win.ini",
		"AABBCCDDEEFF00112233445566778899.png/../../victim.png",
		// 大小写:生成侧只产小写十六进制,校验必须与它逐字符等价 ——
		// 在 NTFS/APFS 这类大小写不敏感的文件系统上,放宽一位就等于同一份磁盘
		// 文件对应两个都能过校验的库值,回收按其中一个删、取回按另一个找。
		"AABBCCDDEEFF00112233445566778899AABBCCDDEEFF001122334455.png",
		"短.png",
		"aabbccddeeff00112233445566778899aabbccddeeff001122334455.exe",
	}
	for _, name := range evil {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, coverStore.Write(name, []byte("x")),
				"形状非法的名字不该被拿去拼路径写入")
			_, err := coverStore.Locate(name)
			assert.Error(t, err, "形状非法的名字不该被拿去拼路径读取")
			// Remove 对形状非法的名字**什么都不删**并返回 nil:猜错的方向是
			// 删掉别的文件,而回收任务要的是"磁盘上没有它",不是"我亲手删了"。
			assert.NoError(t, coverStore.Remove(name))
			assert.FileExists(t, outside, "落盘目录之外的文件一个字节都不该被碰")
		})
	}

	// 同一批名字走一遍取回接口:它读的是库里的 stored_name,而这正是
	// "别处被改坏"之后唯一还剩的一道形状校验。
	row := &Cover{Ref: "EVIL", UserId: 7, ActId: 1, BoundAt: 1,
		StoredName: "../victim.png", MimeType: "image/png", CreatedAt: 1}
	require.NoError(t, gdb.Create(row).Error)
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/lottery/covers/EVIL", nil)
	serveCover(c, row)
	assert.NotEqual(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "do-not-touch",
		"绝不能把落盘目录之外的文件内容当封面回出去")
	assert.FileExists(t, outside)
}

// coversOfActivity 是几处断言共用的小取数,只为让用例读起来是一句话。
func coversOfActivity(t *testing.T, gdb *gorm.DB, actId int64) []Cover {
	t.Helper()
	rows := make([]Cover, 0, 4)
	require.NoError(t, gdb.Where("act_id = ?", actId).Order("id asc").Find(&rows).Error)
	return rows
}

// TestCoverStaysWithinDir 是一条正向钉子:合法名字落出来的路径必须在目录之内。
//
// 与上面那组反向用例配对 —— 只有反向用例时,一个"什么都拒绝"的实现也能全绿。
func TestCoverStaysWithinDir(t *testing.T) {
	gdb := newCoverEnv(t, "")
	row, err := uploadCover(t, 7, "a.png", coverPNG)
	require.NoError(t, err)

	full, err := coverStore.Locate(row.StoredName)
	require.NoError(t, err)
	dir, err := filepath.Abs(coverStore.Dir())
	require.NoError(t, err)
	abs, err := filepath.Abs(full)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(abs, dir+string(filepath.Separator)),
		"落盘路径 %q 必须在 %q 之内", abs, dir)

	require.NoError(t, bindCover(gdb, 100, "ACT1", "", row.Ref, 7))
	assert.Len(t, coversOfActivity(t, gdb, 100), 1)
}

// ── 变异验证(手工执行并已回滚)────────────────────────────────────────
//
//	acceptCoverUpload 把 coverMaxBytes() 换成 config.MaxLotteryCoverBytes
//	    → TestAcceptCoverUploadTooLarge 红(配置旋钮失效)
//	acceptCoverUpload 去掉 CoverOn() 判定
//	    → TestAcceptCoverUploadDisabled 红
//	acceptCoverUpload 把待用计数条件里的 user_id 去掉
//	    → PendingCap 里"不该把别人一起锁死"的断言 红
//	acceptCoverUpload 把 cnt >= max 写成 cnt > max
//	    → PendingCap 第一条断言 红(放行了第 11 张)
//	imagestore.Sniff 里给 default 分支回 (PNG, true)
//	    → HTML/SVG/GIF 三个子用例同时红
//	imagestore.RelPath 去掉 isLowerHex
//	    → TestCoverPathTraversal 的大写十六进制子用例 红
//	imagestore.RelPath 去掉长度判定
//	    → ../victim.png 子用例 红,且 victim.png 被覆盖
