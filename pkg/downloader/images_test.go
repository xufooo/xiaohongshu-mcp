package downloader

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tinyPNG 一张 1x1 合法 PNG（固定字节）。
var tinyPNG = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89, 0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82}

func TestReadImageBodyWithinLimit(t *testing.T) {
	data, err := readImageBody(strings.NewReader("small"), 1024)
	if err != nil {
		t.Fatalf("小内容读取不应失败: %v", err)
	}
	if string(data) != "small" {
		t.Fatalf("内容不符: %q", data)
	}
}

func TestReadImageBodyExceedsLimit(t *testing.T) {
	_, err := readImageBody(strings.NewReader("1234567890"), 5)
	if err == nil {
		t.Fatal("超限读取应报错")
	}
	if !strings.Contains(err.Error(), "exceeds size limit") {
		t.Fatalf("错误信息不清晰: %v", err)
	}
}

func TestDisplayImageURLStripsSensitiveParts(t *testing.T) {
	raw := "https://sns-img.example.com/abc.png?xsec_token=SECRET&xsec_source=pc#frag"
	got := displayImageURL(raw)
	if strings.Contains(got, "SECRET") || strings.Contains(got, "frag") {
		t.Fatalf("敏感部分泄漏: %q", got)
	}
	if !strings.Contains(got, "sns-img.example.com/abc.png") {
		t.Fatalf("path 未保留: %q", got)
	}
	// userinfo 也应清除
	rawUser := "https://user:pass@sns-img.example.com/abc.png?t=SECRET"
	gotUser := displayImageURL(rawUser)
	if strings.Contains(gotUser, "user") || strings.Contains(gotUser, "pass") || strings.Contains(gotUser, "SECRET") {
		t.Fatalf("userinfo/token 泄漏: %q", gotUser)
	}
	// 解析失败 fail-closed
	if got := displayImageURL("http://[bad url"); got != "[invalid-url]" {
		t.Fatalf("畸形 URL 应返回占位，got %q", got)
	}
}

func TestDownloadImageSuccessAndLimits(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(tinyPNG)
		case "/big.png":
			w.Header().Set("Content-Length", fmt.Sprintf("%d", maxRemoteImageBytes+1))
			_, _ = w.Write(tinyPNG)
		case "/text.txt":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("not an image"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	d := NewImageDownloader(t.TempDir())

	// 合法小 PNG 成功
	path, err := d.DownloadImage(srv.URL + "/ok.png?xsec_token=LEAK")
	if err != nil {
		t.Fatalf("合法 PNG 下载失败: %v", err)
	}
	if path == "" {
		t.Fatal("路径为空")
	}

	// Content-Length 超限读取前拒绝
	_, err = d.DownloadImage(srv.URL + "/big.png?xsec_token=LEAK")
	if err == nil {
		t.Fatal("Content-Length 超限应拒绝")
	}
	if !strings.Contains(err.Error(), "exceeds size limit") {
		t.Fatalf("超限错误不清晰: %v", err)
	}
	if strings.Contains(err.Error(), "LEAK") {
		t.Fatalf("错误信息泄露 token: %v", err)
	}

	// 非图片拒绝
	_, err = d.DownloadImage(srv.URL + "/text.txt")
	if err == nil {
		t.Fatal("非图片应拒绝")
	}
}

// failingTransport 返回带完整 URL 的 *url.Error，模拟真实网络错误形态。
type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, &url.Error{
		Op:  "Get",
		URL: "https://example.com/img.jpg?xsec_token=SECRET123",
		Err: fmt.Errorf("connection refused"),
	}
}

// TestDownloadImageErrorStripsURLToken 传输错误剥离 URL 后不得泄露 xsec_token。
func TestDownloadImageErrorStripsURLToken(t *testing.T) {
	d := &ImageDownloader{
		savePath:   t.TempDir(),
		httpClient: &http.Client{Transport: failingTransport{}},
	}
	_, err := d.DownloadImage("https://example.com/img.jpg?xsec_token=SECRET123")
	if err == nil {
		t.Fatal("下载应失败")
	}
	if strings.Contains(err.Error(), "SECRET123") {
		t.Fatalf("错误信息泄露 URL token: %v", err)
	}
	if !strings.Contains(err.Error(), "example.com/img.jpg") {
		t.Fatalf("错误信息应保留脱敏 URL: %v", err)
	}
}

// TestDownloadImageChunkedStreams 无 Content-Length 的分块响应也要能落盘，且字节完全一致。
func TestDownloadImageChunkedStreams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 不设置 Content-Length，并主动 Flush 触发分块传输。
		w.Header().Set("Content-Type", "image/png")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write(tinyPNG[:16])
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write(tinyPNG[16:])
	}))
	defer srv.Close()

	d := NewImageDownloader(t.TempDir())
	path, err := d.DownloadImage(srv.URL + "/chunked.png")
	if err != nil {
		t.Fatalf("分块 PNG 下载失败: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取落盘文件失败: %v", err)
	}
	if !bytes.Equal(data, tinyPNG) {
		t.Fatalf("流式落盘内容不一致: got %d bytes, want %d bytes", len(data), len(tinyPNG))
	}
}

// TestWriteImageStreamRejectsOversize 流式写入超限时必须报错且不留下半成品。
func TestWriteImageStreamRejectsOversize(t *testing.T) {
	original := maxRemoteImageBytes
	maxRemoteImageBytes = 64
	defer func() { maxRemoteImageBytes = original }()

	dir := t.TempDir()
	target := filepath.Join(dir, "big.png")
	err := writeImageStream(target, tinyPNG, strings.NewReader(strings.Repeat("x", 128)))
	if err == nil {
		t.Fatal("超过上限的流式写入应报错")
	}
	if !strings.Contains(err.Error(), "exceeds size limit") {
		t.Fatalf("错误信息不清晰: %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("失败后不应留下目标文件: %v", statErr)
	}
	if _, statErr := os.Stat(target + ".tmp"); !os.IsNotExist(statErr) {
		t.Fatalf("失败后不应留下临时文件: %v", statErr)
	}
}

// TestWriteImageStreamWithinLimit 正常路径：头部 + 剩余流全部写入目标文件。
func TestWriteImageStreamWithinLimit(t *testing.T) {
	target := filepath.Join(t.TempDir(), "ok.png")
	if err := writeImageStream(target, tinyPNG[:16], strings.NewReader(string(tinyPNG[16:]))); err != nil {
		t.Fatalf("正常流式写盘失败: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取落盘文件失败: %v", err)
	}
	if !bytes.Equal(data, tinyPNG) {
		t.Fatalf("内容不一致: got %d bytes, want %d bytes", len(data), len(tinyPNG))
	}
}
