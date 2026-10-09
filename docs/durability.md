# What survives a failure

Handloom's durable state is the task graph, the evidence, the questions and the audit log, all in one SQLite file on the hub, plus the git branches on the machines. An agent's own memory (its context window) is not durable: when an agent is lost, the next one starts from the task, the evidence and git. This page says what happens when something goes wrong, and which test backs each line. A line without a test says so.

## What happens, and the test that shows it

| Something goes wrong | What happens | Test |
| --- | --- | --- |
| An agent's terminal disappears | The link stops vouching for it; after 90 s it is marked offline. If it was a lead with work in flight, the person is told. | `TestAgentLeaseExpiresIntoOffline`, `TestLeadLostIsToldToTheHuman` |
| An agent goes silent on a claimed task | The task lease (15 min, renewed by the agent's activity) runs out; the task is open again and the lead is told. | `TestLeaseExpiryReturnsTaskToOpen` |
| The owner submits after its lease expired | Refused with "not yours", in words; the task stays open and unchanged; the old owner can claim it again. | `TestALateSubmitAfterTheLeaseExpiredIsRefusedAndTheTaskStaysOpen` |
| A task was given to someone else | The old owner's submit and heartbeat are refused; the new owner's work is the work. | `TestAReassignedTaskCannotBeSubmittedByItsOldOwner` |
| Accept arrives twice, or eight times at once | One wins, the rest get 409; recorded once. | `TestAcceptingTwiceIsRefusedAndRecordedOnce`, `TestTwoAcceptsAtTheSameTimeHaveOneWinner` |
| Two agents claim the same task at once | Exactly one wins; nobody gets an error from the hub itself. | `TestTwoClaimsAtTheSameTimeHaveOneWinner` |
| A question is answered twice at once | One answer is kept and the lead is told once. | `TestTwoAnswersAtTheSameTimeHaveOneWinner` |
| The lead is lost while workers carry on | Workers can still submit; a person gives the job a new lead, who reviews the submitted work. | `TestWorkSubmittedWhileTheLeadIsLostIsReviewedByTheNewLead` |
| The hub restarts | Tasks, evidence, open questions, audit and credentials are all there; leases keep running from where they were. | `TestARestartedHubKeepsTheTaskGraphTheLeasesAndTheAudit` |
| The hub is restored from an older backup | Work done after the backup is gone from the hub. An agent that remembers newer state is refused in words and can simply do it again; a task that did not exist at backup time is "not found". | `TestAHubRestoredFromAnOlderCopyRefusesStaleRequestsGently` |
| The hub and link are upgraded with a job in flight | Claimed and submitted tasks and open questions are unchanged; an older link keeps working against the newer hub; work finishes across the upgrade. The database is snapshotted before any migration, and a hub refuses a database from a newer release. | `test/e2e/upgrade.sh` (real older release), `TestMigrationSnapshotsAnExistingDatabase`, `TestNewerSchemaIsRefused` |
| An agent says "working" for 45 minutes holding no task | It is reported once to its lead (or, for a lead, to the person). A task has a lease that activity renews; this covers an agent that hangs before it claims anything. Change it with `--stuck-after`. | `TestAStuckAgentThatNeverClaimedAnythingIsReported` |
| A task is held for too long by an agent that keeps renewing its lease | Off by default. With `--max-task-time`, the task is taken back and the lead told, however busy the owner is. | `TestATaskHeldLongerThanTheTimeLimitIsTakenBackEvenIfItsOwnerIsBusy` |

## What is not covered yet

- **Turns and cost caps.** Only wall time per task is enforced, and only when you set it. Usage and cost are measured, not capped.
- **Agents with no terminal never expire** (headless agents), because nothing vouches for them; only task leases and the stuck report cover them.
- **A restored hub does not know devices, agents or people created after the backup.** Their credentials stop working until they are made again. Not tested.
- **The link dying in the middle of a merge or a check.** The merge and the check are queued on the hub and retried by the link, but a crash at that exact point has not been tested.
- **The hub being down.** Agents' requests fail until it is back; what each CLI does meanwhile is not tested.
- **An agent's own context.** Lost with the agent. A required handoff note for every task is planned (issue #15).
- **Replay.** Handloom does not journal and replay an agent's steps, and does not plan to: work is recovered from git, the evidence and the task.

## Running the checks

`go test ./internal/hub -run 'TestALate|TestAReassigned|TestAccepting|TestTwo|TestWorkSubmitted|TestAStuck|TestARestarted|TestAHubRestored|TestATaskHeld'` runs the cases above (CI runs the whole suite). `test/e2e/upgrade.sh` needs `gh`, `tmux` and network access, and downloads the older release.
