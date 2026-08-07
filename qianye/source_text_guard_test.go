package qianye

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// source_text_guard_test.go —— 源码里不许出现裸 NUL 字节。
//
// # 缺陷原样
//
// web/src/features/qy/pages/admin-group-matrix/lib/draft.ts 的一句注释里,
// 「调用方自己 split(<裸 0x00>) 就等于把键的编码复制了一份」这句话里的分隔符
// 是**直接写进去的裸 0x00**,而不是转义。后果与它的内容毫无关系:
//
//	git 判定一个文件是不是二进制,看的就是前 8000 字节里有没有 NUL。
//	一旦判成二进制,`git diff` 对这个文件的输出永远只有一行
//	「Binary files a/… and b/… differ」,`--stat` 只给
//	「Bin 13103 -> 13585 bytes / 1 file changed, 0 insertions(+), 0 deletions(-)」。
//
// 于是这个文件的**每一次改动都对 review 隐身**。实测发生过:某一轮往这个文件
// 里塞了一个新导出类型,三路交付的对抗式复核按 git diff 逐文件看过去,
// 拿到的是 0 行正文。这次塞进去的东西无害,但同一条隐身通道对下一次改倍率
// 解析规则一视同仁 —— 而 draft.ts 正是矩阵页、配置弹窗与【可用模型分组】列
// 三处共用的倍率草稿解析层,属计费相邻代码。
//
// 仓库规范写着「计费与路由的改动必须有能在改错时变红的测试」;
// 自动化那一半有测试,人工那一半被这条通道绕开了,所以它需要一条自己的锁。
//
// # 为什么是全仓扫描而不是只钉住那一个文件
//
// 钉住单个文件只能防住已经发生过的那一次。NUL 进入源码的途径(编辑器把
// 「插入一个真实分隔符」当成了字面意思、脚本拼串、复制粘贴带走控制字符)
// 在任何一个文件上都成立,而症状恒定是"看不见的改动",没有别的信号。
//
// .gitattributes 里另外给 *.ts / *.tsx / *.go 显式加了 `diff` 属性,让**已经
// 提交进历史**的 NUL 不再遮蔽 diff;这条测试防的是新增。两者都需要:
// 前者管过去,后者管将来。
func TestSourceFilesContainNoNulBytes(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)

	// 只扫源码文本。二进制资产(图片、字体、字典)本来就该含任意字节。
	textExt := map[string]bool{
		".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
		".json": true, ".yaml": true, ".yml": true, ".md": true,
		".css": true, ".html": true, ".sh": true, ".sql": true,
	}
	skipDir := map[string]bool{
		".git": true, "node_modules": true, "dist": true, "build": true,
		"vendor": true, "logs": true, "data": true,
	}

	offenders := make([]string, 0, 1)
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if skipDir[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !textExt[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		// 大文件(锁文件、生成的清单)照样扫:NUL 在任何位置都会让 git
		// 把整个文件判成二进制。
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if idx := bytes.IndexByte(body, 0); idx >= 0 {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, filepath.ToSlash(rel))
		}
		return nil
	})
	require.NoError(t, err)

	assert.Empty(t, offenders,
		"这些文件含裸 NUL 字节,git 会把它们判成二进制,此后对它们的每一次改动"+
			"在 `git diff` 里都只显示一行「Binary files differ」——"+
			"改错了 review 端一个字都看不到。请把 NUL 写成转义(如 \\u0000):%v", offenders)
}
