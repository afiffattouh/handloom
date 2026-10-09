# Direction for teams and enterprises

> Produced on 2026-10-09 by a research session: eight web-research tracks, an independent fact-check of the ten claims the plan leans on, eleven candidate directions, a six-member review committee (security architect, SRE, engineering manager, open-source strategist, AI researcher, red-team skeptic), a chair, and a completeness critic. The evidence, scores and every reviewer are in [evidence.md](evidence.md). **This is advice to weigh, not a decision**: the open questions at the end are the owner's to answer, and the critic's verdict below says what must be tested before committing.

## The short version

Handloom is a self-hosted gate for coding-agent work. It sits across several agent CLIs (Claude Code, Codex, OMP, Pi, OpenCode) and local models. A hub with no git, shell or model in it holds the task graph and audit record. A link on each machine runs agents in git worktrees and runs the check command on the device. Only a signed-in person can accept work. It is meant for small teams that run mixed vendors and keep confidential work on local models: consultancies, agencies, small regulated teams and engineering groups with a security reviewer. It should win where each vendor's own control layer cannot. Those layers are single-vendor and keep their control plane in the vendor's cloud. The big self-hosted platforms (OpenHands, Coder) sell breadth, SSO and isolation. The direction is to be the most trustworthy gate, not the broadest platform. First, make the core provably safe under crash, retry and stuck agents, and publish an honest record of what is enforced, what is only instructed and what is not built. Second, remove the cheap trust holes: the hub being able to widen what agents may do on every device, and confidential jobs not enforced in the link. Third, get real outside users and a case study with numbers. Only then add container isolation, schedules and identity, and add each when a named user needs it or when the core cannot be called safe without it. The committee found no first-hand evidence that teams want a cross-vendor layer, so the plan treats that as a hypothesis to test in the next 90 days.

## Principles

1. Narrow and durable beats broad. Keep the invariants that no one else states: the hub never runs git, a shell or a model; the device's check beats the agent's claim; only a person accepts; confidential jobs stay on local models.
2. The unit of durability is the task graph, the git branch and device-run evidence. Do not build journaled replay or a workflow engine. Document how an external Temporal or n8n flow can start jobs through the operator API.
3. Enforce, do not instruct. Every control is either enforced in the hub or link and covered by a test, or it is labelled 'instructed' or 'not built'. The honest 'not built' status beats a weak implementation.
4. Claim only what Handloom's own fault-injection tests show. Do not use 'durable', 'secure', 'compliant' or 'exfiltration prevention' without a test and a stated limit. The wording is 'supports controls for', never 'compliant'.
5. Containment and hub trust come before anything that widens exposure. Inbound webhooks, event triggers and remote MCP come only after isolation, device-side approval and team scoping.
6. A trigger may only create a reviewable job. There is no unattended auto-merge mode, ever. The review queue is the product.
7. Measure before scaling. Unattended runs multiply unmeasured failure rates, so outcome metrics and enforced budgets come before schedules.
8. Use stable primitives (OIDC, RFC 8693, RFC 8707, RFC 9728, W3C traceparent) and adopt open formats (AGENTS.md, SKILL.md, MCP). Do not bind stored data or customer promises to drafts such as OTel gen_ai or NIST overlays.
9. Features follow named users. SSO, SCIM, Postgres and a managed hub are built when a design partner is blocked on them, not in advance.
10. Match scope to one maintainer. Every new guarantee ships with a fault-injection or contract test, or it does not ship (the D51 rule).

## The bets

| | Bet | When | Committee |
| --- | --- | --- | --- |
| B1 | Core contract: make unattended work safe at the task layer | now | Strongest agreement. |
| B2 | Hub is not fleet-wide remote code execution: device-side approval and link-enforced confidentiality | now | Not scored as its own candidate. |
| B3 | Evidence, outcome metrics and operability | now | Highest composite: D5 3.87, five of six reviewers named it a top bet (CISO, SRE, strategist, researcher, red-team). |
| B4 | Prove the wedge: design partners, a case study with numbers, and a first-100 plan | now | D6 composite 3.67 (third): feasibility 5, differentiation 4.17, but enterprise fit only 2.33. |
| B5 | Container isolation: one honest backend behind the Isolator seam | next | D2 composite 2.92 (fifth). |
| B6 | Safe automation: schedules that only create reviewable jobs | next | D4 composite 2.88 (sixth). |
| B7 | Identity and team scoping, pilot-driven | later | D3 composite 2.42 (eighth). |

## Now (next 3 months)

### B1. Core contract: make unattended work safe at the task layer

**Why.** Handloom's durability is lease-based supervision around git. Known holes: a stuck agent that claimed nothing is invisible (F1), agents with no terminal never expire (D53), time, turns and cost are not enforced (D56), and upgrade with work in flight is untested (D95). Every later bet (schedules, teams, remote access) multiplies these holes. Vendor CLIs are absorbing per-session durability, but not hub-level correctness across devices. The argument rests on first principles and the project's own gaps, not on the conformance preprint.

**What to build.** In this order. (1) A fault-injection suite in CI covering: late submit after the lease expired or the task was reassigned, double accept, double answer, duplicate link, lead loss, hub restart, hub restored from an older backup, and upgrade with a job in flight. (2) Enforced per-task and per-job limits (wall time, turns, owner-priced cost) with a stop at the cap, plus a no-progress and give-up exit that also covers an agent that claimed nothing. (3) Consume-once answers and idempotency keys on submit, accept, answer and close. (4) A lease epoch (fencing token) only where step 1 reproduces a real race. (5) A required handoff note per task (state, what was tried, next step, how to verify), stored in the hub and written into the worktree for a replacement agent. (6) A written resume policy: a resumed agent is told it was interrupted and must verify git and task state first; a cut-off effect is never silently re-run. (7) A published 'what survives a crash and what does not' page. (8) A rule that a job keeps its profile version, plus an N and N-1 hub/link skew policy. (9) Dual-mode stdio MCP for the 2026-07-28 revision (server/discover, per-request _meta, resultType), with a contract test for each of the five CLIs against old and new modes, so a CLI update cannot silently drop the coordination tools.

**How we will know.** The fault-injection suite runs in CI and passes for every listed scenario. No task runs past its cap in a deliberate runaway test. A stuck agent that claimed nothing raises an alert within a stated time. A job with work in flight survives a hub and link upgrade in a scripted test. Each of the five CLIs still gets its handloom tools against both MCP modes. The crash page lists only guarantees that a test backs.

**Where the committee stood.** Strongest agreement. D1 composite 3.75 (second), named in the top bets of all six reviewers, risk 2 (low), feasibility 4. Enterprise fit is only 3.17, because the work is invisible on a procurement checklist (CISO, EM, strategist). Dissent on scope: the researcher says fencing tokens are mis-weighted and stuck-exit, limits and handoff matter more. The red-team lens says fencing and idempotency may solve a problem Handloom does not have and wants it done only after a test shows a bug. The EM calls fencing invisible to a 12-person team. The SRE disagrees and says fencing is the defensible, cheap edge. The chair's resolution is that tests come first and fencing is built only where a test reproduces a race. The MCP dual-mode item was pulled out of D7 phase 1, which the SRE, EM, strategist, researcher, CISO and red-team all called mandatory maintenance.

### B2. Hub is not fleet-wide remote code execution: device-side approval and link-enforced confidentiality

**Why.** The CISO's top finding: the device trusts the hub for profiles and for the job's verify command. Anyone who can post to the hub, or a compromised hub, can choose what an agent may do and run a shell command on every linked device. Per-device approval of profile hashes was deferred (D56, D61). The pinning code already exists, so this is the best security return per hour on the list. It was buried inside D2. Separately, the confidential-job promise (local models only) is the product's wedge and must hold even if a profile is edited or the hub is compromised.

**What to build.** (1) Device-side approval of profile hashes and verify-command hashes. The link refuses to start or check anything whose hash a device administrator (or a second key) has not authorised. A hub signature alone is not enough. (2) Enforce confidential jobs in the link, not only the hub. A confidential job cannot start an agent from a cloud profile, cloud-CLI agents never see the confidential folders, and the agent has no route to a cloud endpoint. (3) A threat model document that says the hub is trusted today, agents run as the link's OS user, and what each change here fixes. (4) SECURITY.md with a disclosure process and security contact. (5) Pin the CLI version a job starts with, and show it in the audit record.

**How we will know.** A test shows a hub that posts a new profile or a changed verify command is refused by the device until a local approval is given. A test shows a confidential job on a cloud profile is refused at the link, and a test shows the refusal still holds when the hub row is altered. A written red-team attempt (see experiments) can no longer run an unapproved command on a linked device through the hub.

**Where the committee stood.** Not scored as its own candidate. It combines the device-hash part of D2 and the confidential-job guarantee of D6. D2 composite 2.92 (enterprise fit 5.0 from all reviewers, feasibility 2.0 for the whole container work). D6 composite 3.67 (feasibility 5, enterprise fit 2.33). Support for the parts: the CISO says pull it out of D2 and ship it first; the SRE and red-team lists name device-side verification as a must-have; the CISO, strategist, researcher and red-team name the link-enforced confidential guarantee. The committee never scored this exact scope, and the chair created it from the review text. Dissent: none on the idea, but the strategist would sequence security work after proof of users.

### B3. Evidence, outcome metrics and operability

**Why.** Auditors certify organisations, not tools, so the realistic enterprise story is the best evidence and the clearest enforcement points, stated honestly. The audit log is append-only by trigger but not chained, signed or exportable, and there is no inventory. The SRE adds the unglamorous items that decide trust: backup schedule, stated RPO and RTO, health checks and metrics. The researcher says the product's central guarantee (evidence-gated acceptance) is unmeasured: nothing shows whether a check can tell a wrong solution from a right one, and nothing tracks rubber-stamping. The flight record and the outcome counters are the same data.

**What to build.** Quick wins first: (1) The 'enforced / instructed / not built' control-mapping page against OWASP ASI01-ASI10, the NIST drafts, the IMDA framework and the auditor lists, with no compliance claim. (2) Outcome metrics from data the hub already holds: accepted-task rate, first-pass check success, rejection and rework, merge-conflict rate, time to accept, human-wait time, cost per accepted task, policy denials, abandoned and duplicate tasks, each with its sample size and a dash when n is small. (3) A per-task flight record: diff, path-policy decision, check command and output, accept or reject decision and who made it. Then: (4) Hash-chained audit rows with signed checkpoints, with the signing key held off the hub, a verify command, NDJSON/syslog export and retention settings. (5) An inventory report of agents, devices, tokens and profile hashes. (6) Scheduled off-box backup (a Litestream sidecar), a stated RPO and RTO, a restore drill, an upgrade and rollback runbook, /healthz and basic Prometheus or OTLP metrics, and a W3C traceparent per job. (7) SBOM and govulncheck in the release workflow. (8) A check-validity warning: flag a job with no check or a trivially passing one.

**How we will know.** A reviewer can run the verify command and detect a modified audit row. The control page exists and every 'enforced' line links to a test. A restore drill from the off-box copy works on a clean box. The metrics page shows real numbers from the author's own jobs, with sample sizes. The release workflow publishes an SBOM.

**Where the committee stood.** Highest composite: D5 3.87, five of six reviewers named it a top bet (CISO, SRE, strategist, researcher, red-team). Enterprise fit 4.33, feasibility 4, risk 2. Dissent: the EM scored user value 2 and says a flight recorder does not make anyone open Handloom on Monday. The strategist, red-team and researcher said to skip OTel gen_ai mapping, transcript capture, and skill and CLI pinning, and the CISO says not to store full transcripts on the hub by default; the plan omits all of these. The researcher says the chain and signing are secondary to the per-task record. The CISO and SRE say the signing key must not live on the hub's own disk. Metrics come from a slice of D9 (6 reviewers: composite 3.17, feasibility 2.33) without replay or an eval platform.

### B4. Prove the wedge: design partners, a case study with numbers, and a first-100 plan

**Why.** Every demand claim in the research is vendor roadmap or analyst framing. There is no published case study, effectively one real user (the author), and one maintainer. The strategist and red-team lenses both say the first thing to build is evidence of use. Candidate wedge: consultancies, agencies and small regulated teams of about 3 to 30 doing client-confidential work with several agent CLIs. This is a hypothesis, not a settled niche. Vibe Kanban's shutdown shows free orchestration has weak commercial footing, so a sustainability path also needs deciding.

**What to build.** (1) Name three outside people or teams, at least one of them an ordinary product team, and have each run a real job on their own repo within 90 days. Time to first accepted task is the key measure. (2) Publish one case study from the author's real jobs with numbers (accepted work, check results, cost per accepted task, what the gate caught). (3) Rewrite the README around the invariants and this buyer, and stop calling it 'self-hosted orchestration for coding agents'. (4) Lower the cold-start cost: a one-line install, a join command for a laptop, one default profile, and a team setup guide that does not need hand-made profiles. (5) Write the support policy: the tested list of CLI versions and protocols, and what is out of scope. (6) Decide the sustainability model (paid install and support through consultancies, a sponsor or a second maintainer, Apache-2.0 core kept) and resolve the EU Cyber Resilience Act manufacturer-or-steward question with legal advice before selling anything. (7) Answer the Cursor question in writing, because half the target teams use it and it is not a supported CLI.

**How we will know.** At least three non-author users have each run a real job and been interviewed. The case study is public. The owner can say which of the 14 open issues have a requester who is not the owner. Time to first accepted task for a new person is under one hour.

**Where the committee stood.** D6 composite 3.67 (third): feasibility 5, differentiation 4.17, but enterprise fit only 2.33. Top bet for the strategist and red-team; the other four did not name it. Dissent is real. The EM says it points away from product teams. The CISO says it must not become a reason to skip isolation, identity or audit. The SRE says it treats a missing recovery story as a market choice. The researcher takes the verified invariants but not the roadmap freeze. The red-team lens says the buyer is asserted, not evidenced, and the local-model angle is eroding (Copilot CLI BYOK with Ollama and vLLM). The chair adopts D6 as positioning and as a test, not as a freeze of other work.

## Next (3 to 9 months)

### B5. Container isolation: one honest backend behind the Isolator seam

**Why.** Agents run as the link's OS user with unattended flags, and repository content is itself an execution surface. This is the biggest reviewer finding and a precondition for any triggered or unattended work. It is not a moat: Cursor, OpenHands, Coder and Docker all ship it. A half-built container mode would be read as a security claim and would be worse than the honest 'not built' status.

**What to build.** One OCI backend (hardened runc, runsc or kata selectable) behind the existing seam. Default-deny network and a per-profile egress allowlist through a proxy, without calling it exfiltration prevention (domain fronting). No Docker socket, no host home, no pre-trusted directories, no project-level agent settings loaded in isolated mode. Resolve the .git exposure (a read-write bind mount lets a box damage history). Redesign credentials first: cloud-CLI profiles get secrets injected by a proxy so the box never holds them; local-model profiles carry no cloud credential. Start with no-egress local-model profiles and one CLI, then widen. Show a Rule-of-Two indicator on each profile (untrusted input, sensitive access, outward action). Document the runtime versions tested and what the mode does not stop.

**How we will know.** Tests prove a box cannot read the host home or SSH keys and cannot reach the network except through the allowlist. All five CLIs are run under default-deny egress and the failures (telemetry, update checks, web tools) are listed. The docs state the limits. A hostile-repository test (repo config tries to run before trust) fails inside the box.

**Where the committee stood.** D2 composite 2.92 (fifth). Enterprise fit 5.0 from all reviewers, named a top bet by four (CISO, SRE, researcher, red-team), but feasibility 2.0 and risk 4.0. Dissent on timing: the strategist says do it late and narrowly, pilot-driven. The EM says the team will not feel it, though the security reviewer will block the pilot without it. The CISO wants it first. The chair's call is 'next': the threat model, the egress test and the credential design happen now (see experiments), and the build follows B1 to B3.

### B6. Safe automation: schedules that only create reviewable jobs

**Why.** Every vendor ships schedules and triggers, and the EM's one claim for week-one value is work that happens when nobody is typing. A team-owned, auditable schedule is a real gap, because hosted routines run as the creator's identity and are per-account. It is also where unmeasured failure and weak containment turn into 3 a.m. incidents, so it is gated on B1 and B5.

**What to build.** Phase 1 only: job templates (profile, brief, repo, check command), local cron, and the knowledge-repo librarian (D68) as the first schedule, since it takes no external payload and proposes on a branch for a person to merge. Per-automation owner and identity. Enforced caps per run and per day (wall time, turns, owner-priced cost), a limit on concurrent and queued runs, skip-if-open-job-for-key, a consecutive-failure circuit breaker, a dry-run mode that creates a job but starts no agent, and kill switches at automation, all-automations and hub level that also cancel running work. Audit every fire, skip and cap hit. Then scheduled chores that end in a reviewable artifact (CI-failure summaries, dependency and backlog triage). Inbound webhooks, GitHub and issue events are phase 2 and are blocked until isolation and team scoping exist; payloads would be stored as untrusted data and could never choose the profile, device or check command.

**How we will know.** A scheduled librarian job runs for a month with no duplicate fire, no breach of its caps and no unreviewed merge. The kill-switch test cancels a running automated job. The review queue is not swamped: the abandoned-task rate for automated jobs is reported and stays low.

**Where the committee stood.** D4 composite 2.88 (sixth). Highest user value (4.0), a top bet for three reviewers (SRE, EM, strategist), but risk 4.33 and feasibility 2.5. Sharp dissent. The EM ranks it first, because it is the only week-one value. The CISO and red-team lens would reject anything beyond the librarian schedule before isolation, device-side approval and team scoping. The SRE and researcher say it must follow the budget, stuck-exit and metrics work. The strategist keeps only cron plus the librarian and defers webhooks. The chair takes the narrow version.

## Later

### B7. Identity and team scoping, pilot-driven

**Why.** SSO and per-team separation are the largest structural gap for a shared hub, since every signed-in person sees every job. The demand evidence is low-confidence and secondary, there is no moat, and a missed team filter in a single-schema SQLite hub is a cross-team leak that is worse than no isolation. The honest answer today is one hub per trust boundary.

**What to build.** Build only when a named design partner is blocked. Order: (1) OIDC login with group-to-role mapping; (2) short-lived, audience-bound credentials for devices and AI-app tokens, each with a named human owner, a revocation list and the inventory report from B3; (3) optional separation of duties (starter cannot accept; a second approver for confidential jobs); (4) question timeouts, reminders and escalation to a second person; (5) per-team scoping only through one central enforcement point with negative tests for every handler, never hand-added WHERE clauses; (6) remote MCP as an OAuth 2.1 resource server (RFC 9728, RFC 8707, RFC 9207, client ID metadata documents) with scopes that mean 'read and start, never approve', modelled on MCP Tasks vocabulary, and only after (5). SAML, SCIM and Enterprise-Managed Authorization only on a named request.

**How we will know.** A design partner signs in through their own identity provider and a negative test suite shows one team cannot read another team's jobs, machines or alert topics. Remote tokens cannot approve anything.

**Where the committee stood.** D3 composite 2.42 (eighth). Enterprise fit 4.83, the highest of any candidate, but differentiation 1.33, feasibility 2.0 and risk 4.33. Top bet for the CISO and EM only. The SRE, strategist and red-team lens say to ship OIDC login alone, or nothing, until a partner is blocked. The CISO ranks isolation and the hub-trust gap above SSO. D7 phase 2 (remote MCP) scored 2.5 and is folded in here, behind team scoping.

## Non-goals

- No journaled replay, durable-execution engine or workflow engine. Integrate with Temporal or n8n through the operator API instead.
- No 'run unattended and auto-merge' mode. A person accepts every task.
- No D10 non-engineering business-workflow product. The 41 starter profiles stay as a free asset for consultancies. Device-checked evidence is the product's guarantee and has no equivalent for documents.
- No D11 company-building, managed hub or paid enterprise tier now. Only the cheap parts are taken: off-box backup with a stated RPO and RTO, an upgrade runbook, and a decision on sustainability in B4.
- No Postgres or second storage path without a named customer requirement and a load measurement of the single-connection hub.
- No SCIM, SAML or Enterprise-Managed Authorization work in advance of a named partner.
- No inbound webhooks, event triggers or remote MCP before isolation, device-side approval and team scoping.
- No A2A integration (watch only), no OTel gen_ai attribute-level promises, no transcript capture on the hub by default, no ACP v2 chasing.
- No generic 'any ACP agent' claim. At most one structured driver (Codex app-server or ACP) as a bounded spike, announced only when tested.
- No replay, pass^k or regression-gate eval platform. Use outcome metrics now and leave eval tooling to external tools. A small fixed-task trial harness is allowed only as an experiment.
- No competing on single-machine worktree orchestration or agent chat UX.
- No compliance, certification or 'secure' claims. No public benchmarks (SWE-bench variants) for picking profiles or models.
- No publishing more architecture documents before the case study exists.

## The enterprise baseline

What has to be true for a team or an enterprise to adopt it. Each line is a checklist item, to be marked *enforced*, *instructed* or *not built* with a test behind every "enforced".

- [ ] Device-side approval of profile hashes and verify-command hashes, so a compromised hub cannot widen what agents may do (B2).
- [ ] Confidential jobs enforced in the link and covered by tests: no cloud profile, no route to a cloud endpoint (B2).
- [ ] Enforced wall-time, turn and owner-priced cost limits per task and per job, a stuck and no-progress exit that covers an agent that claimed nothing, and a circuit breaker (B1).
- [ ] Fencing and idempotency where tests show a race, consume-once answers, and a stale agent's writes rejected and audited (B1).
- [ ] A written resume and crash policy, a published page of what survives a crash, and a fault-injection suite in CI (B1).
- [ ] A rule that a job keeps its profile version, an N and N-1 hub/link skew policy, and an upgrade and rollback runbook that states what happens to in-flight jobs (B1, B3).
- [ ] Dual-mode stdio MCP with a contract test per supported CLI (B1).
- [ ] A tamper-evident, exportable audit log: hash chain with signed checkpoints, signing key held off the hub, a verify command, NDJSON or syslog export, retention settings (B3).
- [ ] An inventory report of agents, devices, tokens, profiles and profile hashes (B3).
- [ ] A published enforced / instructed / not-built control page and a threat model, with 'supports controls for' wording only (B2, B3).
- [ ] SECURITY.md with a disclosure process, SBOM, govulncheck, and signed releases (the signing already exists) (B2, B3).
- [ ] Off-box scheduled backup, a stated RPO and RTO, a tested restore drill, /healthz and metrics, and alerts for lead loss, link offline, stuck task, unanswered question and budget breach (B3).
- [ ] A kill switch that revokes a device or agent and stops running work at job and hub level (B1, B6).
- [ ] Container isolation with default-deny network and no ambient credentials, or a plain statement that agents run as the link user and the link host is the blast radius (B5).
- [ ] OIDC with group-to-role mapping, named human owners for tokens, short-lived audience-bound credentials, optional separation of duties (B7, when a partner is blocked).
- [ ] Per-team scoping with a central enforcement point and negative tests, or an explicit 'one hub per trust boundary' statement until it exists (B7).
- [ ] Per-person attribution of who started, who accepted and what each agent cost, and a view of only one's own work (B3, B7).
- [ ] Notifications that reach chat (not just one ntfy topic), with question timeouts and a second approver (B7).
- [ ] A bus-factor answer: a second maintainer or sponsor, a written security-fix and release policy, and a support policy with a tested CLI list (B4).
- [ ] A published case study with real numbers (B4).

## Risks

| Risk | Mitigation |
| --- | --- |
| Building enterprise surface area before a single external team has used the product (red-team, strategist). A solo maintainer ends with several half-built features and no users. | Put B4 in the first quarter. Cap 'now' work to B1, B2, B3 and B4, and drop the audit chain and export from B3 to 'next' if capacity runs short. Start B5 to B7 only on a named user or a gate. |
| Overclaiming durability, security or compliance. Handloom's recovery is lease detection plus git, which is weaker than journaled replay. A weak container mode would be read as a boundary. | Publish the crash page and the enforced / instructed / not-built page. Every 'enforced' line links to a test. Do not use 'durable', 'secure' or 'compliant' in docs until a test backs it. |
| Commoditisation from vendors (agent view, Routines, Managed Agents, Agent HQ, Cursor self-hosted machines, Anthropic self-hosted runners) and from funded self-hosted platforms (OpenHands, Coder). Neutrality only matters if teams actually mix vendors. | Lead with the invariants, not with orchestration. Check in B4 whether partners mix vendors. If none do, the thesis needs revisiting. |
| Single maintainer is itself a procurement finding. Protocol and CLI churn (MCP broke twice in eight months, Gemini CLI was cut off, Pi changed owner) eats the maintainer's time. | Write a support policy with a tested list. Add contract tests per CLI. Look for a second maintainer or sponsor. Track the monthly time spent on churn. |
| Triggers and unattended runs turn a human-present tool into one that spends money at 3 a.m. and widen the attack surface (prompt injection through payloads, a public inbound endpoint). | Gate B6 on B1 and B5. Phase 1 is local cron plus the librarian only. Payloads are untrusted data and never choose profile, device or check. |
| The human gate may be a rubber stamp. Review capacity is the bottleneck (38 percent of rejected agent PRs in one dataset were abandoned unreviewed). Automation bias is unmeasured. | Track time to accept and acceptance without opening the diff in B3. Offer separation of duties and sampled review. Do not claim the gate works until it is measured. |
| Team scoping retrofitted onto a single-schema hub leaks data across teams. | One hub per trust boundary until a central enforcement point and negative tests exist (B7). |
| Metrics invite Goodhart behaviour (small safe changes, weak checks), and transcripts or traces widen what a hub compromise exposes. | Pair outcome metrics with sampled human review. Keep transcripts opt-in, local and redacted. Test check validity with seeded bad solutions. |
| Standards move under the plan (MCP, NIST drafts, OTel gen_ai, AI Act dates, ID-JAG). | Build on stable primitives. Keep any mapping layer thin and version-pinned. Cite primary pages for dated claims. |
| Legal exposure: the Cyber Resilience Act (steward reporting from 2027-12-11) and any commercial offering. | Get legal advice on manufacturer versus steward before selling anything (B4). |

## Where the committee disagreed

- CISO / security architect: containment and hub trust rank above correctness. A duplicate submit is a nuisance; a compromised hub or an unsandboxed agent is the incident. Expects other reviewers to over-rank triggers (D4) and remote MCP (D7), which widen the attack surface before containment exists. Ranks isolation and the hub-trust gap above SSO, and reads D6 and D3 as not being alternatives, since a small regulated firm needs the same isolation and audit. Would not approve Handloom on a production-adjacent machine today. Did not name D6 or D9 as a top bet. The chair kept isolation in 'next', not 'now', which the CISO would dispute.
- Platform / SRE lead: the missing recovery contract comes first. Fencing, budgets, backup, RPO and RTO, upgrade skew and hub-down behaviour matter more than SSO or triggers. Calls the competitive and standards tracks 'noise'. Says D1 should ship as 'v0.2, no new features' and 'durable' should not be used until it holds in CI. Says D1's case stands on Handloom's own holes, not on the conformance preprint. Dissents from the researcher and red-team lens that fencing may be unneeded, and from the strategist's pilot-driven approach to all enterprise items.
- Engineering manager: nobody adopts a harness because it passes a questionnaire. Ranked D4 (schedules) first and said a 12-person team would not pay the cold-start tax without a week-one result. Gave low value to D2 (2), D5 (2), D6 (2) and said fencing and idempotency are invisible to a team. Wants output as a PR where the team already reviews, per-person attribution, chat notifications and a Cursor answer. Expects the market to deliver team-shared agents inside Anthropic or Cursor subscriptions within two quarters. The chair put schedules in 'next' and made the cold-start fix a part of B4, which only partly answers this.
- Open-source strategist: pick a wedge and a first-100-users plan before an enterprise feature direction. Ranked D6 first. Says SSO, SCIM, Postgres and a managed hub are built only when a named partner is blocked. Isolation is late, narrow and pilot-driven. Says the invariants are mainly a positioning claim, not only a security feature. Dissents from the CISO and SRE on timing, and from the EM on the buyer: the EM wants product teams, the strategist wants consultancies.
- Applied AI researcher: instrument and measure before building enterprise plumbing. Ranked D9 first, scoped to outcome metrics, a repeatable fixed-task trial harness with pass^k, and check-validity testing. Says fencing tokens guard a bug class shown only in a single un-replicated preprint about other frameworks, and that Handloom's effects are mostly git, which is recoverable. The real killers are stuck agents, runaway loops, weak checks, mis-planning and reviewer fatigue. Warns against quoting the Google 17.2x versus 4.4x result as a coding result, and says the lead-plus-workers shape and handoff notes are partly folklore to be re-tested per model generation. Disagrees with the SRE on fencing. The chair took the metrics and check-validity parts but not the trial harness beyond an experiment.
- Red-team skeptic: argues for a much smaller project. Ranked D6 first, and wants a 1.0 of the gate, one isolation backend, audit export and the confidential guarantee, saying no to SSO, triggers and Postgres for a year. Says the correctness case may solve a problem Handloom does not have, the 'unoccupied ground' is inferred from absence of evidence, and vendor-neutrality is only valuable if teams really mix vendors, which nobody has shown. Warns that sequencing everything produces five half-built features. The chair's plan is still a sequence of seven bets, which this lens would say is too many; it is kept because only B1 to B4 are 'now' and B5 to B7 are gated.
- Split inside the committee on D3 and D4: the CISO and EM both ranked D3 as a top-four bet, but for opposite reasons (blocker for pilots versus blocker for sharing a hub). Split on D1: six of six named it, yet three reviewers (researcher, red-team, EM) want it cut down.
- No candidate was flagged 'contested' by the score rule (a gap of 3 or more on enterprise fit or risk). The disagreements above are about ordering and scope, not about individual scores.

## Claims to treat with care

- There is no first-hand evidence that teams want a cross-vendor coordination layer. Every demand finding is vendor roadmap or analyst framing (Gartner items were only seen in search results). The wedge in B4 is a hypothesis.
- The conformance study (arXiv 2608.03836) is a single-author, not peer-reviewed preprint about LangGraph, CrewAI and pydantic-graph, not about lease-and-git systems, and is not independently replicated. ACRFence (arXiv 2603.20625) is also a single source. Neither should be quoted as a reason for D1 or in marketing.
- The Google Research result (17.2x versus 4.4x error amplification, 39-70% sequential degradation) used non-coding tasks. Do not quote it as a coding result. 'Lead plus workers is the evidence-backed shape' is an inference.
- Anthropic's posts on long-running agents and harness design are single-vendor accounts. The comparison between them and Handloom is the researchers' internal assessment. No independent measurement compares context resets and handoff notes with compaction.
- The 'unoccupied ground' (human-only approval, hub with no code or shell, per-client knowledge repos) is inferred from absence in the pages the researchers opened. They did not read OpenHands or Coder closely enough to rule out a hard approval gate or a per-client feature.
- That SSO and SOC 2 are consistent procurement blockers rests on low-confidence secondary sources. Open-source software cannot hold SOC 2 or ISO 42001, so the buyer's own controls carry the review.
- Many 2026 dates and version numbers come from secondary sources: Claude Code agent view (2.1.139, 2026-05-11), the AGENTS.md fallback (2.1.277), the 2.1.274 OTel event, Cursor's acquisition and revenue figures, and Claude Code revenue. Treat the traction numbers as low confidence.
- Dates conflict across sources: A2A joining AAIF (17, 20 or 27 August 2026), AI Act Omnibus entry into force (27 or 29 July 2026), AWS DevOps Agent GA (31 March versus 18 June). Cite primary pages before using any in public.
- The vulnerability scores disagree: Copilot CVE-2025-53773 is given as 9.6 in one summary and 7.8 by Microsoft; the Cursor CVE-2026-22708 details come from a search summary. Do not quote either score.
- The hostile-repository risk (Handloom launches agents with hook-trust bypass and pre-trusted directories) is a design-level risk, not a demonstrated exploit.
- It is unverified how Claude Code, Codex, OpenCode, OMP and Pi fall back against old stdio MCP servers, and which of them implement the 2026-07-28 revision. B1 assumes the risk is real and tests it.
- The research did not read Handloom's source. Whether fencing, idempotency keys or consume-once handling already exist in code is unconfirmed. Handloom's own proof so far is 3 to 5 runs per scenario, mostly one task and one worker.
- The 'Vibe Kanban shut down for lack of a business model' reading rests on search snippets and the repo banner, not the blog post.
- Vendor-reported or unvalidated figures: Microsoft TokenOps gains, Datadog error rates, agent SLO targets, tool-poisoning rates above 60 percent, exposed MCP server counts, agent PR merge rates by vendor, SWE-Bench Pro retraction.
- Claude Code's resume rule (a cut-off tool call is not re-run, and the model must check) is a prompt-level instruction, not an enforced at-most-once guarantee.
- The Ralph-loop failure mode is low confidence. The research found nothing on non-engineering enterprise automation, and no threshold at which SQLite stops being enough.

## Experiments to run before the big bets

- Red-team your own setup (1 to 2 days, before B2 and B5): with a throwaway hub token, post a profile and verify command that runs a harmless marker command on a linked device. Then have an agent try to read ~/.ssh, another CLI's login files and the ssh-agent socket. Record what works. This sizes B2 and B5.
- Fault-injection spike (about 1 week): script the races in B1 (late submit after lease expiry, double accept, double answer, duplicate link, hub restore from an older backup, upgrade with a job in flight). Fencing and idempotency are built only for the scenarios that fail.
- Mine the existing audit log for past jobs (a few hours): accepted, rejected, abandoned, check-failed, time to accept, cost per accepted task. Include how often you accepted within seconds. This gives the first real numbers for B3 and the case study.
- Check validity (1 to 2 days): seed a known-bad solution into three real check commands and see whether each one fails it. If checks are weak, the gate's value is smaller than assumed.
- MCP compatibility matrix (1 to 2 days): run each of the five CLIs against the current stdio server and a stub that speaks 2026-07-28 behaviour. Record which fall back and which lose the handloom tools.
- Three outside runs (30 to 90 days): ask three named external people to complete one real job on their own repo. Measure time to first accepted task and interview them. At least one should be an ordinary product team. This tests the wedge and the cold-start cost.
- Egress-deny trial (about 2 days, before building B5): run each of the five CLIs in a throwaway container with default-deny network and list what breaks (telemetry, update checks, web tools, login refresh).
- Restore drill (half a day): restore the live hub's backup on a clean box from an off-box copy and time it. This gives the first honest RPO and RTO.
- Hub load check (half a day): drive the single-connection SQLite hub with realistic UI and SSE load and many links. This settles whether Postgres is ever a question.
- Handoff note ablation (after B1 ships): run the same multi-task job with and without the required handoff note across a forced lead replacement. This tests whether the note is real value or folklore.
- Competitor reality check (1 day): read OpenHands' and Coder's docs for a hard human-approval gate and per-client knowledge features, and check Copilot CLI and Anthropic self-hosted runners for mixed-vendor support. This tests the 'unoccupied ground' claim.

## Questions for the owner

- Who are three people or teams, not you, who would run Handloom on a real job in the next 90 days, and how many people other than you have run one so far?
- Is Handloom a business, a respected open-source project, or an asset for your own consulting? The answer decides B4's sustainability path, whether B5 to B7 are ever built, and how the red-team 'much smaller 1.0' argument lands.
- If a hub token leaks today, can an attacker run an arbitrary shell command on every linked device through a verify command or a profile that allows a shell? What stops it?
- What can an agent reach beyond its worktree today (home directory, SSH keys, ssh-agent, cloud CLIs logged in as you, other CLIs' logins)? Which machines hold production credentials?
- Across your real jobs, how many tasks were accepted, sent back, abandoned or failed their check? Have you accepted work that passed check.sh but was wrong, and do you ever accept without reading the diff?
- Which real failures cost you time or money (stuck agent, runaway tokens, merge conflict, lost lead, crash)? Would fencing have prevented any of them?
- Do real teams mix Claude Code, Codex and local models on one job, or standardise on one vendor? What does your own usage show, and what did Handloom catch that the vendor CLIs would not?
- What does restoring the hub from an older backup do today to running tasks, leases and already-accepted submits? When did you last do a restore from an off-box copy, and what is your real backup schedule?
- What does a hub or link upgrade do to work in flight (D95), and how long may a link run an older version than the hub?
- How are confidential jobs enforced today if a profile is edited or the hub is compromised: in the link, in the hub, or by convention? Has any client confidential work run through Handloom, and can you publish anonymised numbers?
- Where must accepted work land for a team: a GitHub pull request with evidence attached, or the Handloom integration branch you merge? How does Cursor fit?
- How many hours a week can you give this for the next six months, and who could review patches or restore the hub if you are unavailable? Would you accept an external security reviewer or a second maintainer?
- Has any prospective user asked for SSO, isolation or schedules by name, or were those inferred from competitor lists? Which of the 14 open issues have a requester who is not you?
- What is your plan if Anthropic or Cursor ships team-shared, cross-machine, mixed-vendor agents inside their subscriptions in the next two quarters?
- Will you commit to publishing the enforced / instructed / not-built page even where it weakens the pitch?

## The critic's verdict

*Added after the session:* the critic noted the agents never read Handloom's source. Three of the code claims the plan leans on were checked by reading it: confidential jobs are refused for cloud profiles in the hub (`internal/hub/spawns.go`) and nowhere in the link (`internal/link` has no confidentiality check), so the guarantee holds only while the hub is honest; the audit log is append-only through database triggers but not hash-chained or signed (`internal/store`); and the verify command and profile come from the hub and the link runs them, so a compromised hub can run a command on every linked machine (the README's security note says as much). Those three hold. The claims about stuck agents, headless agents and limits come from `DECISIONS.md` (D53, D56, D95) and were not re-tested.

The direction (a narrow, trustworthy human-accepted gate, tests before features, honest enforced/instructed/not-built labelling, no workflow engine) is coherent and fits the evidence. The plan is not yet decision-grade, for three reasons. Demand is unproven, and it is the assumption every later bet rests on. Handloom's source was never read, so B1 and B2 rest on unverified claims about the code. The 'now' list is too big for one maintainer and in one place breaks the report's own principles (outside users before containment, more docs before the case study, prompt-level resume rules). Treat B4's demand test, the source and red-team spike, and the vendor-terms check as gates that come before the rest of 'now'. Trim B1 to B3 to their cheapest items until those gates pass.

**Missing:**

- Primary demand evidence. No interviews, no design-partner signal, and no check of who else (besides the author) has run Handloom. Every bet from B2 to B7 depends on a buyer who has not been found.
- Vendor terms of service and licensing for driving logged-in CLI subscriptions from a third-party orchestrator. Track 2 listed it as a gap, but the report's risk list and open questions omit it. If Anthropic, OpenAI or others restrict this, the core model changes. Gemini CLI being cut off is already a warning sign.
- Handloom's own source code was never read. B1 (fencing, idempotency, consume-once) and B2 (pinning code 'already exists', best return per hour) both rest on unverified assumptions about what is already built.
- Local-model capability. The confidential-work wedge assumes local models (for example qwen on the GB10) can do real tasks that pass checks. No evidence on pass rates or on how Copilot BYOK with Ollama or vLLM erodes the angle.
- Willingness to pay and pricing for the consultancy and regulated-team buyer. B4 asks for a sustainability decision but no research supports any option.
- Stakeholders not represented: legal or DPO and procurement at the target buyer (data residency, client contracts, what clients allow agents to see), and the engineers who must review the queue (reviewer fatigue is flagged but not studied).
- Data residency and secrets handling (Vault, 1Password agents). Track 3 dropped them, and B5's 'proxy injects secrets' design has no evidence behind it.
- Platform coverage (macOS and Windows links, Docker sbx hardware requirements) and Cursor and Copilot CLI support. Half the target teams use Cursor, yet it is only raised as a written-answer item.

**Weakly supported:**

- B2 was never scored. The chair built it from review text, yet it is called the best security return per hour. The only supporting claim, that the pinning code exists, is unverified (source not read).
- B4's wedge (consultancies, agencies, small regulated teams) comes from D6, which scored enterprise fit 2.33 and got only 2 top-bet votes. The 'unoccupied ground' it relies on is inferred from absence in pages that were not read closely (OpenHands, Coder). The Vibe Kanban reading rests on snippets.
- B1's handoff note, lead-plus-workers shape and resets over compaction rest on single-vendor Anthropic posts. Fencing rests on a single un-replicated preprint about other frameworks. Both are acknowledged in the report, but the handoff note is still a required item.
- B6's claim of a 'real gap' in team-owned auditable schedules is not checked against Cursor Automations (team pools, self-hosted machines) or Codex automations. The research did not verify who owns those schedules.
- B3's hash chain with off-hub signing key is heavy operational work with no buyer asking for it. The report's own principle ('features follow named users') argues against it.
- B1 item 9 (dual-mode MCP for five CLIs) assumes each CLI's behaviour against the 2026-07-28 revision, which is unverified. Also, the spec read was a 'draft' page.
- The Rule-of-Two indicator in B5 adopts vendor guidance that fact-check inject-1 says is not a standard.

**Contradictions:**

- Positioning says vendor control layers are 'single-vendor'. Fact-check commod-2 says GitHub hosts Claude and Codex agents in Copilot, and commod-1 says Cursor is multi-model and cross-vendor ecosystems are growing. The wedge is overstated.
- Principle: 'containment and hub trust come before anything that widens exposure'. Yet B4 (in 'now') has outside users run unattended agents on their own repos while isolation (B5) is 'next'. Agents run as the link user with hook-trust bypass, which the report itself calls a design-level hostile-repo risk. At minimum B2 and the egress or threat-model work must precede outside runs, or the pilot needs a restricted mode.
- Principle: 'match scope to one maintainer' and the red-team warning against many half-built features. 'Now' holds B1 to B4 with about 29 sub-items (9 + 5 + 8 + 7) in 90 days. The report admits the plan is still too many bets for the red-team lens, and its only mitigation is dropping the audit chain.
- Principle: 'enforce, do not instruct'. B1 item 6 adopts a written resume policy telling a resumed agent to verify state. That is the same prompt-level mechanism the report flags in Claude Code as not enforced at-most-once.
- Non-goal: 'no publishing more architecture documents before the case study exists'. B2 and B3 publish a threat model, a crash page, an enforced/instructed/not-built control page and SECURITY.md, all before B4's case study.
- Scores versus plan: the top composite (D5 3.87) is carried by feasibility and low risk, not user value (2.83, with the EM scoring it 2). D2 has enterprise fit 5.0 and four top-bet votes but sits fifth, and the CISO ranks it first. The composite formula is not shown, and risk polarity is unclear. The 'now' order (B1 to B3) follows ease more than the stated enterprise goal. B1 is also called 'strongest agreement' while three reviewers want it cut down.
- The 20-item enterprise baseline (OIDC, per-team scoping, chat notifications, second approver) conflicts with the narrow small-team positioning and the 'built when a partner is blocked' principle, with no tie-break rule.

**Follow-ups that would most change the decision:**

- Run a 2 to 3 week demand test first: 5 to 8 interviews or design-partner runs with consultancies and small regulated teams. The key question is whether they mix vendors or keep confidential work local, and what they use today. This decides whether B2 to B7 matter at all. Do it before B1 to B3 grow past the cheapest items.
- Read the Handloom source and run the red-team and fault-injection spikes (1 to 2 weeks together). This settles whether fencing, idempotency, consume-once and profile-hash pinning exist, and sizes B1 and B2 on facts.
- Check vendor terms (Anthropic, OpenAI, Cursor, Gemini, Copilot) on running CLIs under third-party orchestrators with subscription logins, plus rate limits. A restriction would change the product model, so do it in a day.
- Do the competitor reality check on primary pages: OpenHands and Coder approval gates and per-client knowledge, Copilot Agent HQ on Business, Anthropic self-hosted runners, Cursor team automations. This tests the 'unoccupied ground' and 'single-vendor' claims.
- Benchmark local models on 10 to 20 real jobs with checks, to test the confidential wedge's feasibility and the BYOK erosion risk.
- Re-sequence: move the threat model, device-side profile approval and the egress-deny trial ahead of any outside-user runs, or run the outside pilots in a restricted no-hostile-repo mode.
- Run the check-validity and audit-log mining experiments. They are cheap and show whether the gate itself works, which every bet assumes.
- Get legal advice on the Cyber Resilience Act manufacturer-or-steward question and on client-confidentiality terms before any paid offer.

