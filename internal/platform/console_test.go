package platform

import (
	"os"
	"runtime"
	"testing"
	"unicode/utf8"
)

func TestEnableUTF8Console_DoesNotPanic(t *testing.T) {
	EnableUTF8Console()
}

func TestS7B_ChineseUTF8RoundTrip(t *testing.T) {
	s := "做成了什么、怎么测、还差什么"
	if !utf8.ValidString(s) {
		t.Fatal("fixture must be valid UTF-8")
	}
	// Mojibake pattern from reading UTF-8 as GBK then re-encoding.
	if looksLikeGBKMojibake(s) {
		t.Fatal("plain Chinese must not look like GBK mojibake")
	}
}

func TestF54_WriteUTF8BOMIfRedirected_DiskFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/out.log"
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	WriteUTF8BOMIfRedirected(f)
	_, _ = f.WriteString("收到：从零做一个库")
	_ = f.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if len(data) < 3 || data[0] != 0xEF || data[1] != 0xBB || data[2] != 0xBF {
			t.Fatalf("Windows redirected stdout should start with UTF-8 BOM, got %q", data[:min(8, len(data))])
		}
	} else if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		t.Fatal("Unix must not prepend UTF-8 BOM")
	}
}

func TestF54_WriteUTF8BOMIfRedirected_NilSafe(t *testing.T) {
	WriteUTF8BOMIfRedirected(nil)
}

func looksLikeGBKMojibake(s string) bool {
	// Typical F54 console garbage: 鎴 / 浜 / Â / Ã sequences instead of CJK.
	for _, r := range []rune{'鎴', '浜', 'Ã', 'Â'} {
		for _, c := range s {
			if c == r {
				return true
			}
		}
	}
	return false
}
