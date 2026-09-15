param(
    [string]$AvatarsExe = ".\bin\avatars.exe",
    [string]$Root = ".\.avatars\real-capability-smoke",
    [switch]$KeepWorkspaces
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Resolve-Exe([string]$Path) {
    $resolved = Resolve-Path -LiteralPath $Path -ErrorAction SilentlyContinue
    if ($null -eq $resolved) {
        throw "avatars executable not found: $Path"
    }
    return $resolved.Path
}

function New-SmokeWorkspace([string]$Name) {
    $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
    $safeName = $Name -replace "[^A-Za-z0-9_.-]", "-"
    $path = Join-Path $script:SmokeRoot "$stamp-$safeName"
    New-Item -ItemType Directory -Force -Path $path | Out-Null
    return $path
}

function Write-TextFile([string]$Path, [string]$Content) {
    $dir = Split-Path -Parent $Path
    if ($dir -and -not (Test-Path -LiteralPath $dir)) {
        New-Item -ItemType Directory -Force -Path $dir | Out-Null
    }
    Set-Content -LiteralPath $Path -Value $Content -Encoding UTF8
}

function Run-Command([string]$Name, [string]$Workdir, [string[]]$Command) {
    Push-Location $Workdir
    try {
        $output = & $Command[0] @($Command[1..($Command.Count - 1)]) 2>&1 | Out-String
        $code = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    if ($code -ne 0) {
        throw "case '$Name' command failed ($code): $($Command -join ' ')`n$output"
    }
    return $output
}

function Run-Intent([string]$Name, [string]$Workdir, [string]$Intent) {
    return Run-Command $Name $Workdir @($script:Avatars, "intent", $Intent)
}

function Assert-Contains([string]$Name, [string]$Text, [string]$Needle) {
    if (-not $Text.Contains($Needle)) {
        throw "case '$Name' missing text: $Needle`n--- output ---`n$Text"
    }
}

function Assert-NotContains([string]$Name, [string]$Text, [string]$Needle) {
    if ($Text.Contains($Needle)) {
        throw "case '$Name' unexpected text: $Needle`n--- text ---`n$Text"
    }
}

function Assert-File([string]$Name, [string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "case '$Name' missing file: $Path"
    }
}

function Assert-PythonCompile([string]$Name, [string]$Workdir, [string]$Path) {
    Run-Command $Name $Workdir @("python", "-m", "py_compile", $Path) | Out-Null
}

function Start-Case([string]$Name) {
    Write-Host "CASE $Name ..."
}

function Pass-Case([string]$Name) {
    $script:Passed += $Name
    Write-Host "PASS $Name"
}

function Invoke-Case([string]$Name, [scriptblock]$Body) {
    Start-Case $Name
    try {
        & $Body
        Pass-Case $Name
    } catch {
        $script:Failed += $Name
        Write-Host "FAIL $Name"
        throw
    }
}

$script:Avatars = Resolve-Exe $AvatarsExe
$script:SmokeRoot = Join-Path (Resolve-Path -LiteralPath ".").Path $Root
$script:Passed = @()
$script:Failed = @()

if ((Test-Path -LiteralPath $script:SmokeRoot) -and -not $KeepWorkspaces) {
    Remove-Item -LiteralPath $script:SmokeRoot -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $script:SmokeRoot | Out-Null

Invoke-Case "script-llm-first" {
    $workdir = New-SmokeWorkspace "script-llm-first"
    $out = Run-Intent "script-llm-first" $workdir "用 python 写一个 squares.py 脚本：计算 1 到 5 的平方，每行打印 n -> n*n。不要写 Hello 模板。"
    Assert-Contains "script-llm-first" $out "Generator: llm"
    $file = Join-Path $workdir "squares.py"
    Assert-File "script-llm-first" $file
    $content = Get-Content -LiteralPath $file -Raw
    Assert-NotContains "script-llm-first" $content "Hello from script"
    Assert-Contains "script-llm-first" $content "range(1, 6)"
    Assert-Contains "script-llm-first" $content "n * n"
    Assert-PythonCompile "script-llm-first" $workdir "squares.py"
}

Invoke-Case "attached-file-exec" {
    $workdir = New-SmokeWorkspace "attached-file-exec"
    Write-TextFile (Join-Path $workdir "task.md") @"
# Task

用 python 写一个 doc_task.py 脚本。
脚本打印 countdown: 3, 2, 1, done。
不要输出 Hello 模板。
"@
    $out = Run-Intent "attached-file-exec" $workdir "@task.md"
    Assert-Contains "attached-file-exec" $out "Generator: llm"
    $file = Join-Path $workdir "doc_task.py"
    Assert-File "attached-file-exec" $file
    $content = Get-Content -LiteralPath $file -Raw
    Assert-NotContains "attached-file-exec" $content "Hello from script"
    Assert-Contains "attached-file-exec" $content "countdown"
    Assert-PythonCompile "attached-file-exec" $workdir "doc_task.py"
}

Invoke-Case "repl-memory-follow-up" {
    $workdir = New-SmokeWorkspace "repl-memory-follow-up"
    Push-Location $workdir
    try {
        $inputLines = @(
            "用 python 写一个 memory_follow.py 脚本：打印 memory ok，不要写 Hello 模板。",
            "脚本在哪？",
            "/exit"
        ) -join [Environment]::NewLine
        $out = $inputLines | & $script:Avatars repl 2>&1 | Out-String
        $code = $LASTEXITCODE
    } finally {
        Pop-Location
    }
    if ($code -ne 0) {
        throw "case 'repl-memory-follow-up' repl failed ($code)`n$out"
    }
    Assert-Contains "repl-memory-follow-up" $out "memory_follow.py"
    $file = Join-Path $workdir "memory_follow.py"
    Assert-File "repl-memory-follow-up" $file
    $content = Get-Content -LiteralPath $file -Raw
    Assert-NotContains "repl-memory-follow-up" $content "Hello from script"
}

Invoke-Case "analysis-depth" {
    $workdir = New-SmokeWorkspace "analysis-depth"
    Write-TextFile (Join-Path $workdir "README.md") @"
# Risky Mini App

Stores user text into data.json and reads it back.
"@
    Write-TextFile (Join-Path $workdir "app.py") @"
import json
from pathlib import Path

DATA = Path("data.json")

def load():
    return json.loads(DATA.read_text())
"@
    $out = Run-Intent "analysis-depth" $workdir "分析这个项目，找 1 个具体风险，写入 depth_report.md，不改文件。"
    Assert-Contains "analysis-depth" $out "Full answer:"
    $report = Join-Path $workdir "depth_report.md"
    Assert-File "analysis-depth" $report
    $content = Get-Content -LiteralPath $report -Raw
    Assert-Contains "analysis-depth" $content "Evidence"
    Assert-Contains "analysis-depth" $content "app.py"
}

Invoke-Case "bootstrap-python" {
    $workdir = New-SmokeWorkspace "bootstrap-python"
    $out = Run-Intent "bootstrap-python" $workdir "用 Python 搭一个 ledger-lite CLI 空项目：支持 add/list 两个命令，数据保存到本地 JSON 文件，生成 README 和 unittest 测试。"
    Assert-Contains "bootstrap-python" $out "Generator: llm"
    Assert-Contains "bootstrap-python" $out "Verifier: PASS"
    Assert-File "bootstrap-python" (Join-Path $workdir "README.md")
    Assert-File "bootstrap-python" (Join-Path $workdir "tests")
}

Invoke-Case "repair-closure" {
    $workdir = New-SmokeWorkspace "repair-closure"
    Write-TextFile (Join-Path $workdir "go.mod") "module calcsmoke`n`ngo 1.22`n"
    Write-TextFile (Join-Path $workdir "calc.go") @"
package main

func Multiply(a, b int) int {
	return a + b
}
"@
    Write-TextFile (Join-Path $workdir "calc_test.go") @"
package main

import "testing"

func TestMultiply(t *testing.T) {
	if got := Multiply(3, 4); got != 12 {
		t.Fatalf("Multiply(3,4) = %d, want 12", got)
	}
}
"@
    $out = Run-Intent "repair-closure" $workdir "修复 calc.go：Multiply 当前实现错误，改到 go test ./... 通过。"
    Assert-Contains "repair-closure" $out "Generator: llm"
    Run-Command "repair-closure" $workdir @("go", "test", "./...") | Out-Null
    $content = Get-Content -LiteralPath (Join-Path $workdir "calc.go") -Raw
    Assert-Contains "repair-closure" $content "a * b"
}

Invoke-Case "edit-many" {
    $workdir = New-SmokeWorkspace "edit-many"
    Write-TextFile (Join-Path $workdir "go.mod") "module notetaker`n`ngo 1.22`n"
    Write-TextFile (Join-Path $workdir "notes.go") @"
package main

type Note struct {
	ID   int
	Text string
}

func Add(notes []Note, text string) []Note {
	return append(notes, Note{ID: len(notes) + 1, Text: text})
}
"@
    Write-TextFile (Join-Path $workdir "main.go") @"
package main

func main() {}
"@
    Write-TextFile (Join-Path $workdir "notes_test.go") @"
package main

import "testing"

func TestAdd(t *testing.T) {
	notes := Add(nil, "one")
	if len(notes) != 1 || notes[0].ID != 1 {
		t.Fatalf("Add failed: %#v", notes)
	}
}
"@
    Write-TextFile (Join-Path $workdir "README.md") "# note-taker`n`nCommands: add.`n"
    $out = Run-Intent "edit-many" $workdir "给这个 note-taker CLI 增加 delete 命令：支持按 ID 删除笔记；同步更新 README.md、main.go、notes.go、notes_test.go，并运行 go test ./... 验证。"
    Assert-Contains "edit-many" $out "Edit-many mode: apply"
    Assert-Contains "edit-many" $out "Verifier: PASS"
    Run-Command "edit-many" $workdir @("go", "test", "./...") | Out-Null
    Assert-Contains "edit-many" (Get-Content -LiteralPath (Join-Path $workdir "notes.go") -Raw) "Delete"
    Assert-Contains "edit-many" (Get-Content -LiteralPath (Join-Path $workdir "README.md") -Raw) "delete"
}

Write-Host ""
Write-Host "Real capability smoke complete."
Write-Host "Passed: $($script:Passed.Count)"
foreach ($name in $script:Passed) {
    Write-Host "- $name"
}
Write-Host "Workspace root: $script:SmokeRoot"
