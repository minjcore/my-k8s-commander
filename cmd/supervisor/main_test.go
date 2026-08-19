package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// trimOldest phải cắt đúng biên dòng: UI đọc theo dòng, nhận dòng bị cắt đầu là
// hiện ra rác.
func TestTrimOldestCatDungBienDong(t *testing.T) {
	var buf bytes.Buffer
	for _, l := range []string{"mot", "hai", "ba", "bon"} {
		buf.WriteString(l + "\n")
	}
	// "mot\nhai\nba\nbon\n" = 16 byte. Giữ 8 -> phải bỏ trọn 2 dòng đầu.
	dropped := trimOldest(&buf, 8)
	if dropped != 8 {
		t.Fatalf("bỏ %d byte, chờ 8", dropped)
	}
	if got := buf.String(); got != "ba\nbon\n" {
		t.Fatalf("còn lại %q", got)
	}
}

func TestTrimOldestKhongLamGiKhiChuaVuot(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("ngan\n")
	if dropped := trimOldest(&buf, 100); dropped != 0 {
		t.Fatalf("bỏ %d byte khi chưa vượt trần", dropped)
	}
	if buf.String() != "ngan\n" {
		t.Fatalf("nội dung bị đổi: %q", buf.String())
	}
}

// Buffer không có trần thì worker in nhanh là hết RAM. Ghi thừa trần phải bị cắt.
func TestWriteGiuBufferDuoiTran(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	w := &orchestratorLogWriter{mu: &mu, buf: &buf}

	line := []byte(strings.Repeat("x", 1023) + "\n")
	for buf.Len() <= maxLogBufBytes*2 {
		if _, err := w.Write(line); err != nil {
			t.Fatal(err)
		}
		if buf.Len() > maxLogBufBytes+len(line)+256 {
			t.Fatalf("buffer %d byte, vượt trần %d", buf.Len(), maxLogBufBytes)
		}
		// Đã cắt ít nhất một lần rồi thì thôi.
		if strings.Contains(buf.String(), "byte log cũ nhất") {
			break
		}
	}
	if !strings.Contains(buf.String(), "buffer chạm trần") {
		t.Fatal("cắt log mà không báo cho người dùng biết")
	}
}

// Log mới nhất phải còn nguyên sau khi cắt — cắt cũ giữ mới.
func TestWriteGiuLogMoiNhat(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	w := &orchestratorLogWriter{mu: &mu, buf: &buf}

	filler := []byte(strings.Repeat("y", 4095) + "\n")
	for i := 0; i < (maxLogBufBytes/len(filler))+16; i++ {
		if _, err := w.Write(filler); err != nil {
			t.Fatal(err)
		}
	}
	const marker = "[k8s-worker] dòng cuối cùng\n"
	if _, err := w.Write([]byte(marker)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(buf.String(), marker) {
		t.Fatal("mất dòng mới nhất")
	}
	if buf.Len() > maxLogBufBytes+len(filler)+256 {
		t.Fatalf("buffer %d byte, vượt trần", buf.Len())
	}
}
