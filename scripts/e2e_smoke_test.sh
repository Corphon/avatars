#!/bin/bash
# avatars E2E Smoke Test Suite
# Validates core NL routing: script creation, edit, bootstrap, analysis, clarify.
# Usage: cd new_project_for_test && bash ../scripts/e2e_smoke_test.sh

set -e
AVATARS="${AVATARS_BIN:-./avatars/bin/avatars.exe}"
PASS=0
FAIL=0

clean() {
  rm -rf .avatars stage *.py *.html *.go *.txt note-keeper 2>/dev/null || true
}

test_case() {
  local name="$1"
  local input="$2"
  local expect="$3"
  echo -n "  [$name] "
  if echo "$input" | timeout 120 $AVATARS repl 2>&1 | grep -q "$expect"; then
    echo "PASS"
    PASS=$((PASS + 1))
  else
    echo "FAIL (expected: $expect)"
    FAIL=$((FAIL + 1))
  fi
}

echo "=== avatars E2E Smoke Test ==="
echo ""

# 1. Script creation
clean
test_case "script-py" "写一个python脚本，打印hello world" "Wrote:"

# 2. Edit existing file
clean
echo "print('hello')" > test.py
test_case "edit-py" "修改test.py，把hello改成你好" "Wrote:"

# 3. Analysis request
clean
echo "# README" > README.md
test_case "analysis" "帮我分析这个项目" "analysis"

# 4. Bootstrap
clean
test_case "bootstrap" "搭一个python项目，叫demo" "Bootstrap mode: apply"

# 5. Clarify vague request
clean
test_case "clarify" "帮我做点事" "不确定\|clarify\|not sure"

# 6. Capability question
clean
test_case "capability" "你能做什么" "Capabilities"

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
