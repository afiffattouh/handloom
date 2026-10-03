#!/usr/bin/env bash
# M0 check (DESIGN.md section 16): on two machines, two shells registered as
# lead and worker complete create -> claim -> submit -> accept and exchange
# messages, using only handloom commands. Lease expiry returns a task to open.
#
# The lead's shell is on this machine, the worker's on $REMOTE.
# Run: test/e2e/m0.sh        (see lib.sh for settings)

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

LEASE=20s
LEAD=hltest-lead
WORKER=hltest-worker
here="$LEAD@$(hostname -s)"
there="$WORKER@$REMOTE"

trap e2e_cleanup EXIT
e2e_setup m0 --lease "$LEASE" --sweep 1s

say "register: lead here, worker on $REMOTE"
show "$here" lhl "$LEAD" register "$LEAD" --kind shell
show "$there" rhl "$WORKER" register "$WORKER" --kind shell
show admin admin agent role "$LEAD" lead
show "$there" rhl "$WORKER" whoami

say "create -> claim -> submit -> accept, with messages"
show "$here" lhl "$LEAD" task create "Report the kernel version" \
  --body "Run uname -r on your machine and submit the output as evidence." --assign "$WORKER"
show "$there" rhl "$WORKER" inbox
show "$there" rhl "$WORKER" task claim 1
expect_status 1 claimed
show "$there" rhl "$WORKER" send role:lead "Claimed task 1, running it now." --task 1
kernel="$(rsh uname -r)"
show "$there" rhl "$WORKER" task submit 1 --evidence "test:uname -r -> $kernel" --note "Output of uname -r on $REMOTE."
expect_status 1 submitted
show "$here" lhl "$LEAD" inbox
show "$here" lhl "$LEAD" task show 1
show "$here" lhl "$LEAD" send "$WORKER" "Thanks, accepting task 1."
show "$here" lhl "$LEAD" task accept 1
expect_status 1 done
show "$there" rhl "$WORKER" inbox

say "scopes are enforced by the hub"
rejected "$there" rhl "$WORKER" task create "Workers cannot create tasks"
rejected "$there" rhl "$WORKER" task accept 1
rejected "$LEAD@$REMOTE (imposter)" rhl "$LEAD" task create "A device cannot speak for an agent of another device"

say "lease expiry returns a task to open (lease $LEASE)"
show "$here" lhl "$LEAD" task create "A task whose owner goes silent"
show "$there" rhl "$WORKER" task claim 2
expect_status 2 claimed
echo "    waiting 23s without a heartbeat..."
sleep 23
expect_status 2 open
rejected "$there" rhl "$WORKER" task heartbeat 2
rejected "$there" rhl "$WORKER" task submit 2 --evidence "too late"
show "$here" lhl "$LEAD" inbox
lhl "$LEAD" inbox --all | grep -q "lease held by $WORKER expired" || fail "the lead was not told about the expired lease"

say "board"
show human human task list
show human human agents

say "audit log"
human audit | tee "$OUT/audit.txt"
for action in device.join agent.register agent.role task.create task.claim task.submit task.accept \
  message.send inbox.read denied task.lease_expired; do
  grep -q " $action " "$OUT/audit.txt" || fail "audit log has no $action"
done

printf '\nM0 PASS: lead on %s and worker on %s completed the loop with handloom commands only.\n' "$(hostname -s)" "$REMOTE"
