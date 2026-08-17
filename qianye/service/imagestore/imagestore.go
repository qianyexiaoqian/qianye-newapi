// Package imagestore 是扩展里**唯一**的"用户上传图片"落盘实现。
//
// # 为什么必须只有一份
//
// 提现凭证(modules/withdraw/proof.go)先有了这套东西:魔数判定、服务端生成
// 文件名、分片目录、MaxBytesReader 前置截断、鉴权下载、到期清理。工单要收
// 截图,需要的是逐条相同的防线 —— 而本仓反复栽的正是"同一概念的第 N 份拷贝
// 各自漂移":两份拷贝在被复制的当天都是对的,后来给其中一份补的东西另一份
// 不会跟上。上传这条路上的每一条防线漏掉一条的后果都不是显示问题:
//
//   - 少了 MaxBytesReader   → 一个 POST 吃光内存与磁盘临时空间
//   - 少了魔数判定          → 落盘的可以是任意二进制,扩展名由服务端瞎猜
//   - 少了 isLowerHex       → 大小写不敏感的文件系统上同一份文件对应两个
//     都能过校验的库值,清理按其中一个删、下载按另一个取
//   - 少了 O_EXCL           → 撞名时静默覆盖别人的图
//   - 少了 nosniff          → 伪装成 JPEG 的 HTML 被浏览器当页面执行
//
// 所以这些机制收进本包,调用方只负责三件本包无从知道的事:落在哪个目录、
// 元数据存在哪张表、谁有权下载。
//
// # 本包刻意不做的事
//
//   - **不解码图片**。解码才是解压炸弹(1 KB 的 PNG 可以声明 60000×60000)
//     真正的风险面;不解码就没有这个面。代价是无法保证文件能渲染,那由浏览器
//     承担 —— 而下载响应带着 nosniff + 精确 Content-Type,伪装成图片的 HTML
//     不会被当作 HTML 执行。
//   - **不碰数据库**。元数据行的形状(谁上传的、绑在哪张单/哪条消息上、
//     什么时候清)各模块不同,收进来只会长成一个到处是可选字段的万能表。
//   - **不决定目录可配**。目录锚在配置文件旁边,不给运维一个能把它指到 Web
//     根目录下的旋钮 —— 本项目的静态资源只有 embed.FS 一个来源,磁盘上的
//     任何目录都不可达,这条约束因此天然成立,而"目录可配"会让它失效。
package imagestore

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
)

// MaxBytes 是任何一处上传上限配置的**硬上界**。
//
// 存在的理由是上传缓冲:校验魔数需要把整张图读进内存,配得越大,一次
// CriticalRateLimit 放行的并发上传能吃掉的堆就越多。用户传的是手机截图,
// 8 MiB 足够,再大只说明配错了。
const MaxBytes = 8 << 20

// randomBytes 是文件名的随机位数(16 字节 → 32 位十六进制)。
//
// 必须用 crypto/rand:文件名可预测意味着可以被枚举,而下载接口的鉴权一旦
// 哪天被写错,可预测的文件名就是直接的批量拖库入口。
const randomBytes = 16

// Kind 是一种被接受的图片类型。
//
// 白名单写死在代码里而不是配置里:每一种类型都对应一段魔数判定与一个扩展名,
// 配置里加一个 "gif" 而代码不认识它,结果是文件被存成一个谁都认不出的扩展名。
type Kind struct {
	Mime string
	Ext  string
}

var (
	JPEG = Kind{Mime: "image/jpeg", Ext: "jpg"}
	PNG  = Kind{Mime: "image/png", Ext: "png"}
	WEBP = Kind{Mime: "image/webp", Ext: "webp"}
)

// exts 是允许出现在磁盘文件名里的扩展名集合。
// 它是 Kind 的投影,而不是另写一份:两处各写一遍就是"同一概念的第二份拷贝"。
var exts = map[string]bool{JPEG.Ext: true, PNG.Ext: true, WEBP.Ext: true}

// AcceptMimes 供各模块的 config 接口下发给前端做 accept 属性。
// 前端的 accept 只是体验,真正的判定在 Sniff。
func AcceptMimes() []string { return []string{JPEG.Mime, PNG.Mime, WEBP.Mime} }

// 上传阶段的哨兵错误。各模块把它们翻译成自己的业务 code ——
// 本包不知道该回 400 还是 413,也不知道前端拿什么 key 去查文案。
var (
	// ErrEmpty 表示没带文件、文件为空,或表单解析失败。
	ErrEmpty = errors.New("imagestore: 没有可用的上传文件")
	// ErrTooLarge 表示超过调用方给的 maxBytes。
	ErrTooLarge = errors.New("imagestore: 上传文件超出大小上限")
	// ErrType 表示魔数不在白名单内。
	ErrType = errors.New("imagestore: 不支持的图片类型")
	// ErrMalformedName 表示库里的文件名形状非法(不可能由本包生成)。
	ErrMalformedName = errors.New("imagestore: 非法的落盘文件名")
	// ErrMissing 表示库里有行、磁盘上没有文件。
	ErrMissing = errors.New("imagestore: 文件不在磁盘上")
)

// Sniff 按【魔数】判定图片类型。
//
// 三条都不信:
//   - 不信扩展名 —— 用户提供的文件名根本不落盘,更不参与判定
//   - 不信 Content-Type 请求头 —— 那是客户端随便写的一个字符串
//   - 不信 http.DetectContentType —— 它会对认不出的内容回退成 "text/plain"
//     或 "application/octet-stream",而我们需要的是"不认识就拒绝"
func Sniff(head []byte) (Kind, bool) {
	switch {
	case len(head) >= 3 && bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return JPEG, true
	case bytes.HasPrefix(head, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return PNG, true
	case len(head) >= 12 && bytes.HasPrefix(head, []byte("RIFF")) &&
		bytes.Equal(head[8:12], []byte("WEBP")):
		return WEBP, true
	default:
		return Kind{}, false
	}
}

// NewStoredName 生成落盘文件名。ext 必须是白名单里的扩展名。
func NewStoredName(ext string) (string, error) {
	if !exts[ext] {
		return "", ErrType
	}
	buf := make([]byte, randomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf) + "." + ext, nil
}

// RelPath 把落盘文件名解析成相对目录内的路径,并顺便校验它的形状。
//
// 名字是服务端生成的,那为什么还要校验?因为拼进 filepath.Join 的这一步不该
// 依赖"别处一定没被改坏":数据库行可以被 DBA 手工改、可以被一次 SQL 注入写坏,
// 而 filepath.Join("dir", "../../etc/passwd") 会老老实实地跳出目录。
// 一次廉价的形状校验换掉一整类"只要别处出一个洞,这里就变成任意文件读写"。
//
// 前两位十六进制做分片子目录:一个目录里堆几十万个文件,ext4 与 NTFS 都会
// 明显变慢,而分片之后单目录期望只有 1/256。
func RelPath(storedName string) (string, bool) {
	base, ext, ok := strings.Cut(storedName, ".")
	if !ok || !exts[ext] || len(base) != randomBytes*2 || !isLowerHex(base) {
		return "", false
	}
	return filepath.Join(base[:2], storedName), true
}

// isLowerHex 只认小写十六进制。
//
// 刻意不用 hex.DecodeString:它大小写通吃,而生成侧的 hex.EncodeToString 只产
// 小写 —— 校验一旦比生成器松,就凭空多出一批"服务端永远不会生成、校验却放行"
// 的名字。在大小写不敏感的文件系统上(NTFS、默认的 APFS/HFS+),ABC.jpg 与
// abc.jpg 指向同一个文件,于是同一份磁盘文件对应两个都能过校验的数据库值 ——
// 清理任务按其中一个删、下载接口按另一个取,两边就对不上了。
// 校验必须与生成器逐字符等价,不多不少。
func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Store 是一个落盘目录。各模块各持一个,互不干扰。
type Store struct{ dirName string }

// New 返回一个以 dirName 命名的落盘目录,位置与配置文件同级
// (即 Docker/本地下的 data/,整个 /data/ 已在 .gitignore 内)。
//
// 跟着 config.Path() 走而不是新增一个 dir 配置项,是刻意的:目录可配意味着
// 运维可以把它指到 web 根目录下,而"落盘目录不得在静态资源路由可达范围内"
// 这条约束没有任何机制能替他守住。锚在配置文件旁边则天然满足。
func New(dirName string) *Store { return &Store{dirName: dirName} }

// Dir 是该 Store 的绝对目录。每次现算而不缓存:配置可以热载。
func (s *Store) Dir() string {
	return filepath.Join(filepath.Dir(config.Path()), s.dirName)
}

// Accept 从 multipart 表单里读一张图并校验,返回内容与判定出的类型。
//
// 只读进内存、**不落盘** —— 调用方通常要先插一行元数据再写文件
// (反过来的话,写完文件而插库失败会留下一个没有任何行指向它的孤儿文件,
// 磁盘上永远清不掉,因为清理任务是按库行扫的)。
func Accept(c *gin.Context, field string, maxBytes int64) ([]byte, Kind, error) {
	if maxBytes <= 0 || maxBytes > MaxBytes {
		// 调用方的校验器已经挡过一次,这里是第二道:配置热更新之后仍然要有确定的上界。
		maxBytes = MaxBytes
	}

	// 【必须在读第一个字节之前】。放在 FormFile 之后等于先把整个请求体收下来,
	// 那时限流的意义已经没有了 —— 一个 2 GiB 的 POST 已经吃完了内存和磁盘临时空间。
	// 多给 1 MiB 是 multipart 边界与表单头的开销,不是内容的余量。
	bodyCap := maxBytes + (1 << 20)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, bodyCap)

	// ContentLength 是客户端声明的,【不是】准入依据 —— 真正的截断由上面那行做。
	// 这里读它只为把错误从"表单解析失败"提升成"文件太大":多层解析之后
	// MaxBytesError 会不会被原样透出取决于 mime/multipart 的实现细节,
	// 而告诉用户"图片太大"和"请求格式不对"是两种完全不同的指引。
	if c.Request.ContentLength > bodyCap {
		return nil, Kind{}, ErrTooLarge
	}

	fh, err := c.FormFile(field)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, Kind{}, ErrTooLarge
		}
		return nil, Kind{}, ErrEmpty
	}
	if fh.Size > maxBytes {
		return nil, Kind{}, ErrTooLarge
	}

	src, err := fh.Open()
	if err != nil {
		return nil, Kind{}, ErrEmpty
	}
	defer func() { _ = src.Close() }()

	// 读满 maxBytes+1:恰好等于上限是合法的,多出一个字节才是超限。
	// header 里的 Size 已经查过一次,这里再查一次是因为 Size 来自客户端声明的
	// Content-Length,而真正落盘的是这条流。
	data, err := io.ReadAll(io.LimitReader(src, maxBytes+1))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, Kind{}, ErrTooLarge
		}
		return nil, Kind{}, ErrEmpty
	}
	if int64(len(data)) > maxBytes {
		return nil, Kind{}, ErrTooLarge
	}
	if len(data) == 0 {
		return nil, Kind{}, ErrEmpty
	}
	kind, ok := Sniff(data)
	if !ok {
		return nil, Kind{}, ErrType
	}
	return data, kind, nil
}

// Write 把内容写进落盘目录。
//
// O_EXCL 是并发写同名的最后一道锁:文件名有 128 位熵,撞名在实践中不会发生,
// 但"不会发生"和"发生了会静默覆盖别人的图"之间差着一个标志位。
// 权限 0600 / 0700:上传内容可能是 PII,同机器上的其他进程没有理由读到它。
func (s *Store) Write(storedName string, data []byte) error {
	rel, ok := RelPath(storedName)
	if !ok {
		return fmt.Errorf("%w: %q", ErrMalformedName, storedName)
	}
	full := filepath.Join(s.Dir(), rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(full)
		return err
	}
	return f.Close()
}

// Remove 从磁盘删除一个文件。文件本就不存在视为成功 ——
// 清理任务的目的是"磁盘上没有它",不是"我亲手删掉了它"。
//
// 形状非法的名字【什么都不删】并返回 nil:猜错的方向是删掉别的文件。
// 告警交给调用方,它才知道这一行属于哪张单。
func (s *Store) Remove(storedName string) error {
	rel, ok := RelPath(storedName)
	if !ok {
		common.SysError("qianye/imagestore: 元数据里的文件名形状非法,已跳过删除: " + storedName)
		return nil
	}
	if err := os.Remove(filepath.Join(s.Dir(), rel)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Locate 返回可以直接交给 c.File 的绝对路径。
//
// 两种失败要分开:ErrMalformedName 是"库里那一行坏了"(调用方应当告警),
// ErrMissing 是"文件不在盘上"(多节点各存各的时最常见的一种表现,也可能是
// 落盘失败留下的残行)。后者不该回 500 —— 它不是服务端故障。
func (s *Store) Locate(storedName string) (string, error) {
	rel, ok := RelPath(storedName)
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrMalformedName, storedName)
	}
	full := filepath.Join(s.Dir(), rel)
	if st, err := os.Stat(full); err != nil || st.IsDir() {
		return "", ErrMissing
	}
	return full, nil
}

// CachePrivate 是**私密内容**的缓存口径:工单截图、提现凭证这类
// "只有特定几个人有权看"的图片一律用它。no-store 让内容既不进共享代理,
// 也不落进浏览器的磁盘缓存 —— 一台共用电脑上按后退键不该翻出别人的身份材料。
const CachePrivate = "private, no-store"

// CachePublicImmutable 是**公开且按 ref 不可变**内容的缓存口径。
//
// 用它的前提有两条,缺一条都不能用:
//
//  1. 这份内容对任何人都是同一份(否则 public 会让代理把 A 的图发给 B);
//  2. 一个 ref 对应的字节永远不变 —— 本包的落盘名由 crypto/rand 生成且
//     Write 带 O_EXCL,同名覆盖不可能发生,所以这一条天然成立。
//
// 存在的理由是抽奖大厅:卡片背景图是首屏上并排十几张的图,配 no-store 等于
// 每次进大厅都把它们重新拉一遍。
const CachePublicImmutable = "public, max-age=604800, immutable"

// Serve 把文件回给**已鉴权**的调用者。鉴权由调用方在此之前完成。
//
// 四个响应头都是必须的:
//   - Content-Type 由入库时的魔数判定给出,不由扩展名或请求头决定
//   - X-Content-Type-Options: nosniff 让浏览器不要自作主张改判类型 ——
//     一份伪装成 JPEG 的 HTML 不能因为浏览器"猜"出 text/html 就被当页面执行
//   - Cache-Control 由调用方在 CachePrivate / CachePublicImmutable 里二选一。
//     做成参数而不是写死:两种内容的口径相反,而写死一个再让另一方在调用前
//     自己 c.Header 覆盖是行不通的 —— 本函数里的 c.Header 会把它盖掉,
//     于是"我明明设了缓存"变成一句没有任何效果的代码。空串按私密处理,
//     因为把公开当私密只是慢一点,反过来是泄漏。
//   - Content-Disposition 的 downloadName 必须由服务端拼出,
//     永远不含用户提供的任何字符串。
func Serve(c *gin.Context, fullPath, mime, downloadName, cacheControl string) {
	if cacheControl == "" {
		cacheControl = CachePrivate
	}
	c.Header("Content-Type", mime)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", cacheControl)
	c.Header("Content-Disposition", `inline; filename="`+downloadName+`"`)
	c.File(fullPath)
}
