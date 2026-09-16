package platform

// UTF8BOM is the UTF-8 byte-order mark. Windows Notepad / PowerShell
// Get-Content without -Encoding sniff UTF-8 more reliably when it is present.
// Never prepend this to stderr: progress JSON must stay BOM-free (F72).
var UTF8BOM = []byte{0xEF, 0xBB, 0xBF}
