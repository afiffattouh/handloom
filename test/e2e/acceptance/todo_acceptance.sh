#!/usr/bin/env bash
# Black-box acceptance of the todo app on a checkout: prints "ACCEPT passed/total" and one line per failure.
# The agents never see this file. usage: todo_acceptance.sh <dir>
cd "$1" || exit 2
pass=0; total=0; T="$(mktemp -d)"; export TODO_FILE="$T/todo.json"
t() { total=$((total+1)); if eval "$2" >"$T/out" 2>"$T/err"; then pass=$((pass+1)); else echo "  not met: $1"; fi; }
run() { python3 todo.py "$@"; }
t "add prints the new id" '[ "$(run add "buy milk")" = "1" ]'
t "a second add prints 2" '[ "$(run add "write report" --tag work --tag urgent)" = "2" ]'
t "list shows an open task as: 1 [ ] buy milk" 'run list | grep -qx "1 \[ \] buy milk"'
t "list shows tags as #tag after the text" 'run list | grep -qx "2 \[ \] write report #work #urgent"'
t "list --tag work shows only the tagged task" '[ "$(run list --tag work)" = "2 [ ] write report #work #urgent" ]'
t "done hides the task from list" 'run done 1 && ! run list | grep -q "buy milk"'
t "list --all shows done tasks as [x]" 'run list --all | grep -qx "1 \[x\] buy milk"'
t "the tasks persist in TODO_FILE as JSON" 'python3 -c "import json,sys; d=json.load(open(sys.argv[1])); assert d" "$TODO_FILE"'
t "export csv has the header and a row" '[ "$(run export --format csv | head -1)" = "id,text,done,tags" ] && run export --format csv | grep -qx "2,write report,false,work;urgent"'
t "export json is an array of objects with the four fields" 'run export --format json | python3 -c "import json,sys; d=json.load(sys.stdin); assert isinstance(d,list) and set(d[0])>={\"id\",\"text\",\"done\",\"tags\"}"'
t "remove deletes a task" 'run remove 2 && ! run list --all | grep -q "write report"'
t "an unknown id fails with a message on stderr" '! run done 99 && [ -s "$T/err" ]'
t "the README documents every command and TODO_FILE" 'for w in add list done remove export TODO_FILE; do grep -q -- "$w" README.md || exit 1; done'
t "the code is split into a storage module and the command-line module" '[ "$(ls *.py | grep -v -e "^todo.py$" -e "^test" | wc -l)" -ge 1 ] && grep -q "import" todo.py'
t "there are at least six unit tests and they pass" '[ "$(grep -rh "def test_" tests 2>/dev/null | wc -l)" -ge 6 ] && ./check.sh'
echo "ACCEPT $pass/$total"
rm -rf "$T"
