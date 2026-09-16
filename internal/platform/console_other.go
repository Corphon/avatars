//go:build !windows

package platform

import "os"

// EnableUTF8Console is a no-op off Windows; Unix stdio is already UTF-8.
func EnableUTF8Console() {}

// ConsoleOutputCodePage is 0 off Windows (no OEM console code page).
func ConsoleOutputCodePage() uint32 { return 0 }

// WriteUTF8BOMIfRedirected is a no-op off Windows. Unix viewers treat UTF-8
// without BOM as the default; a BOM would only surprise JSON/text tools.
func WriteUTF8BOMIfRedirected(_ *os.File) {}
