//go:build windows

package platform

import (
	"os"
	"syscall"
)

const utf8CodePage = 65001

const (
	fileTypeUnknown = 0
	fileTypeChar    = 2 // console
)

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleCP  = kernel32.NewProc("SetConsoleCP")
	procSetConsoleOut = kernel32.NewProc("SetConsoleOutputCP")
	procGetConsoleOut = kernel32.NewProc("GetConsoleOutputCP")
	procGetFileType   = kernel32.NewProc("GetFileType")
)

// EnableUTF8Console switches the attached Windows console to UTF-8 (cp65001)
// so operator-facing Chinese on stdout/stderr is not decoded as the OEM
// code page (F54). No-op when no console is attached (redirected logs stay UTF-8).
func EnableUTF8Console() {
	_, _, _ = procSetConsoleOut.Call(uintptr(utf8CodePage))
	_, _, _ = procSetConsoleCP.Call(uintptr(utf8CodePage))
}

// ConsoleOutputCodePage returns the current output code page, or 0 on failure.
func ConsoleOutputCodePage() uint32 {
	r1, _, _ := procGetConsoleOut.Call()
	return uint32(r1)
}

// WriteUTF8BOMIfRedirected prepends UTF-8 BOM when f is a pipe or disk file
// (redirected logs). No-op for a real console — EnableUTF8Console already
// set cp65001. Never call this on stderr (JSON progress, F72).
func WriteUTF8BOMIfRedirected(f *os.File) {
	if f == nil {
		return
	}
	r1, _, _ := procGetFileType.Call(f.Fd())
	if r1 == fileTypeUnknown || r1 == fileTypeChar {
		return
	}
	_, _ = f.Write(UTF8BOM)
}
