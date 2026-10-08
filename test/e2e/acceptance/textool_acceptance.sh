#!/usr/bin/env bash
# Black-box acceptance of the text tool on a checkout: prints "ACCEPT passed/total". The agents never see it.
cd "$1" || exit 2
pass=0; total=0; T="$(mktemp -d)"
t() { total=$((total+1)); if (eval "$2") >"$T/out" 2>"$T/err"; then pass=$((pass+1)); else echo "  not met: $1"; fi; }
run() { python3 tool.py "$@"; }
printf 'one two\nthree\n' > "$T/words.txt"
printf 'name,age\nAda,36\nLin,29\n' > "$T/a.csv"
printf 'name,note\nAda,"a, b"\n' > "$T/q.csv"
t "the existing greet command still works" '[ "$(run greet World)" = "hello, World" ]'
t "wordcount prints lines, words and characters" '[ "$(run wordcount "$T/words.txt")" = "2 3 14" ]'
t "wordcount on a missing file fails with a message" '! run wordcount "$T/nope.txt" && [ -s "$T/err" ]'
t "slugify makes a slug" '[ "$(run slugify "Hello, World! 2024")" = "hello-world-2024" ]'
t "slugify removes accents and joins arguments" '[ "$(run slugify Crème brûlée)" = "creme-brulee" ]'
t "csv2json prints an array of objects of strings" 'run csv2json "$T/a.csv" | python3 -c "import json,sys; assert json.load(sys.stdin)==[{\"name\":\"Ada\",\"age\":\"36\"},{\"name\":\"Lin\",\"age\":\"29\"}]"'
t "csv2json handles a quoted field with a comma" 'run csv2json "$T/q.csv" | python3 -c "import json,sys; assert json.load(sys.stdin)[0][\"note\"]==\"a, b\""'
t "help lists all four commands" 'h="$(run help)"; for c in greet wordcount slugify csv2json; do echo "$h" | grep -q "$c" || exit 1; done'
t "an unknown command fails" '! run nosuchcommand'
t "the README documents each new command" 'for c in wordcount slugify csv2json; do grep -q "$c" README.md || exit 1; done'
t "each command is in its own module" '[ "$(ls *.py | grep -v -e "^tool.py$" -e "^greet.py$" | wc -l)" -ge 3 ]'
t "there are at least nine unit tests and the check passes" '[ "$(grep -rh "def test_" tests | wc -l)" -ge 12 ] && ./check.sh'
echo "ACCEPT $pass/$total"
rm -rf "$T"
