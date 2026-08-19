package common

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// audio_duration_guard_test.go —— 转写计费的唯一依据是文件自述的时长,所以
// 「解析不出时长」必须是错误,不能是 0 秒。
//
// 缺陷形态一:.ogg/.oga/.opus 在内容不是 OGG 时返回 (0, nil) —— getOGGDuration
// 失败后回落 getOpusDuration,后者一个 "OggS" 页头都没扫到就 EOF 退出,
// totalGranulePos 保持 0。0 秒 → 0 token → hasBillableUsage() 判成「没有可计费
// 用量」→ 整笔免单,而上游照常把音频转写出来并按分钟收平台的钱。攻击成本是改
// 一个文件名。.mp3 与 .m4a 也各有自己的 0/NaN 出口。
//
// 缺陷形态二:WAV 的 data chunk 声明多少字节就按多少字节算时长。同一份 16KB
// 的文件既可以自称 600 秒(多收 588 倍),也可以自称 2 字节(少收 588 倍)。

// buildWav 造一个 16-bit / 单声道 / 16kHz 的 WAV:declaredDataSize 是写进 data
// chunk 头里的字节数,realDataBytes 是文件里**真的**有多少音频字节。
func buildWav(t *testing.T, declaredDataSize uint32, realDataBytes int) []byte {
	t.Helper()
	const (
		sampleRate = 16000
		numChans   = 1
		bitDepth   = 16
	)
	buf := &bytes.Buffer{}
	byteRate := uint32(sampleRate * numChans * bitDepth / 8)
	blockAlign := uint16(numChans * bitDepth / 8)

	buf.WriteString("RIFF")
	require.NoError(t, binary.Write(buf, binary.LittleEndian, uint32(36+realDataBytes)))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	require.NoError(t, binary.Write(buf, binary.LittleEndian, uint32(16)))
	require.NoError(t, binary.Write(buf, binary.LittleEndian, uint16(1))) // PCM
	require.NoError(t, binary.Write(buf, binary.LittleEndian, uint16(numChans)))
	require.NoError(t, binary.Write(buf, binary.LittleEndian, uint32(sampleRate)))
	require.NoError(t, binary.Write(buf, binary.LittleEndian, byteRate))
	require.NoError(t, binary.Write(buf, binary.LittleEndian, blockAlign))
	require.NoError(t, binary.Write(buf, binary.LittleEndian, uint16(bitDepth)))
	buf.WriteString("data")
	require.NoError(t, binary.Write(buf, binary.LittleEndian, declaredDataSize))
	buf.Write(bytes.Repeat([]byte{0x01, 0x00}, realDataBytes/2))
	return buf.Bytes()
}

func TestUnparseableAudioIsAnErrorNotAFreeTranscription(t *testing.T) {
	// 一段不是任何真实音频容器的字节。足够大,免得解析器因为"太短"而恰好报错。
	payload := bytes.Repeat([]byte{0x41, 0x42, 0x43, 0x44}, 400_000)

	for _, ext := range []string{".ogg", ".oga", ".opus", ".mp3", ".m4a", ".mp4", ".wav", ".flac", ".webm", ".aac"} {
		t.Run(ext, func(t *testing.T) {
			duration, err := GetAudioDuration(context.Background(), bytes.NewReader(payload), ext)
			assert.Error(t, err,
				"解析不出时长必须报错;返回 (0, nil) 会让这次转写完全免费")
			assert.Zero(t, duration)
		})
	}
}

func TestWavDurationIsBoundedByTheBytesActuallyPresent(t *testing.T) {
	const (
		sampleRate    = 16000.0
		bytesPerFrame = 2.0
	)
	realBytes := 16_000 // 0.5 秒的真实音频

	cases := []struct {
		name         string
		declared     uint32
		wantDuration float64
	}{
		{
			// 实测形态:16KB 的文件把 data 声明成 19,200,000(= 600 秒),
			// 旧代码照单全收,多收 588 倍。
			name:         "an oversized declared data chunk is clamped to the real bytes",
			declared:     19_200_000,
			wantDuration: float64(realBytes) / bytesPerFrame / sampleRate,
		},
		{
			// 声明值老实时逐位不变。
			name:         "an honest declaration is used as-is",
			declared:     uint32(realBytes),
			wantDuration: float64(realBytes) / bytesPerFrame / sampleRate,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wav := buildWav(t, tc.declared, realBytes)
			duration, err := GetAudioDuration(context.Background(), bytes.NewReader(wav), ".wav")
			require.NoError(t, err)
			assert.InDelta(t, tc.wantDuration, duration, 0.001)
		})
	}
}
