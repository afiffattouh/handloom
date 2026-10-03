# Protocol handloom/1

JSON over HTTP. Every path starts with `/v1`. Any client that follows this page can talk to a hub. The hub answers with header `Handloom-Protocol: handloom/1`.

This page covers what M0 and M1 implement. Escalations, the web board and MCP come later.

## Authentication

`Authorization: Bearer <token>`. The token's prefix says what it is.

| Token | Prefix | Who holds it | Got from |
|---|---|---|---|
| admin | `hva_` | the person who runs the hub | `handloom hub init`, printed once |
| join | `hvj_` | nobody for long; one use | `POST /admin/devices` |
| device credential | `hvd_` | the device's link, file mode 0600 | `POST /devices/join` |
| human | `hvh_` | a person | `POST /admin/humans` |

The hub stores only SHA-256 hashes of tokens.

A device request acts for one agent, named in the header `Handloom-Agent: <name>`. The hub checks that this agent is registered on the calling device and rejects the request with 403 if not. Agents never hold a token: they call the link's unix socket, and the link adds the device credential.

## Scopes

Enforced by the hub on every request. A rejected request gets 403 and is written to the audit log as `denied`.

| Action | lead | worker | observer | human |
|---|---|---|---|---|
| read board and messages | yes | yes | yes | yes |
| send messages | yes | yes | no | yes |
| create, assign, accept, reject, cancel tasks | yes | no | no | yes |
| claim, heartbeat, submit, release, block own task | yes | yes | no | no |
| open escalation | yes | no | no | no |
| answer escalation | no | no | no | yes |

The admin token may read, administer (devices, humans, projects, roles) and read the audit log. It may not send messages or change tasks. Roles are set by the admin or a human, never by an agent. A new agent is a `worker`.

## Errors

A failed request returns a status other than 200 and a body `{"error": "<text>", "code": "<code>"}`.

| Status | Code | Meaning |
|---|---|---|
| 400 | `bad_request` | malformed request; unknown JSON fields are rejected |
| 401 | `unauthorized` | missing, wrong or revoked token |
| 403 | `forbidden` | the caller's scope does not allow it, or the task is not the caller's |
| 404 | `not_found` | no such task, agent or project in the caller's project |
| 409 | `conflict` | wrong task status, unfinished dependency, name taken |
| 429 | `rate_limited` | too many messages from this sender (60 per minute) |
| 501 | `not_implemented` | escalations |

## Endpoints

### Devices and administration

```
POST /devices/join                 {join_token}            -> {device, credential}     no auth; the token works once
POST /admin/devices                {name}                  -> {name, token}            admin; token is the join token
GET  /admin/devices                                        -> [device]                 admin
POST /admin/devices/{name}/revoke                                                      admin
POST /admin/humans                 {name}                  -> {name, token}            admin
POST /admin/projects               {name}                                              admin
GET  /projects                                             -> [project]                admin, human
GET  /audit?after=<seq>&limit=<n>                          -> [audit row]              admin, human
GET  /whoami                                               -> {kind, name, device, agent}
```

### Agents

```
POST /agents                       {name, kind, project?, wake_target?, session_id?} -> agent    device
GET  /agents?project=                                      -> [agent]                  read
POST /agents/{name}/role           {role}                  -> agent                    admin, human
POST /agents/{name}/state          {state}                 -> agent                    the agent's device
POST /agents/{name}/turn-end                               -> {block, unread}          the agent's device
POST /agents/{name}/wake           {method, reason?}       -> {messages}               the agent's device
GET  /device/agents                                        -> [agent + unread, undelivered, last_unread_id]   device
```

- `state` is `idle`, `working`, `blocked` or `offline`. The hub also sets `unknown`.
- `turn-end` is the end-of-turn hook. If the agent has unread mail that this hook has not handed over before, the answer is `block: true`, the agent stays `working`, the mail is marked delivered by `hook`, and a `wake` row is written. Otherwise the agent becomes `idle`. One batch of mail holds the agent at most once.
- `wake` is the link's report. `method` `tmux` or `herdr`: a nudge was typed; unread mail is marked delivered. `method` `none` with a `reason`: the agent could not be woken; the hub writes `wake.failed` and messages the lead. Reason `no_inbox_after_nudges` also sets the agent's state to `unknown`.
- Registering an existing name from the same device refreshes its kind, wake target and session id. From another device it is a 409.

### Messages

```
POST /messages                     {to, body, task_id?, project?}  -> {ids, recipients}   send
GET  /inbox                                                -> [message]   unread for the calling agent; marks them read
GET  /inbox?all=1                                          -> [message]   full history; marks nothing
POST /messages/{id}/delivered      {method}                               the recipient's device
```

`to` is an agent name, `role:<role>` (every agent with that role), or `task:<id>` (the owner plus the lead). The sender is set by the hub from the credentials: `agent:<name>`, `human:<name>` or `hub`. A request cannot set it.

The hub itself sends notices as `hub`: task assigned, task can be claimed now, submitted, accepted, rejected, cancelled, released, blocked, lease expired, agent cannot be woken.

### Tasks

```
POST /tasks                        {title, body?, assigned_to?, depends_on?, project?} -> task   manage
GET  /tasks?status=&owner=&project=                        -> [task]                   read
GET  /tasks/{id}                                           -> task                     read
POST /tasks/{id}/assign            {agent}                 -> task                     manage; open tasks only
POST /tasks/{id}/claim                                     -> task                     work
POST /tasks/{id}/heartbeat                                 -> task                     owner
POST /tasks/{id}/release                                   -> task                     owner
POST /tasks/{id}/block             {reason}                -> task                     owner; empty reason clears
POST /tasks/{id}/submit            {evidence[], note?}     -> task                     owner
POST /tasks/{id}/accept                                    -> task                     manage; submitted tasks only
POST /tasks/{id}/reject            {reason}                -> task                     manage; submitted tasks only
POST /tasks/{id}/cancel                                    -> task                     manage; any task not done
```

Statuses: `open`, `claimed`, `submitted`, `done`, `cancelled`. `blocked` is a flag on a claimed task.

- **Claim** needs status `open`, all `depends_on` tasks `done`, and, if `assigned_to` is set, the caller to be that agent. It sets the owner and a lease (15 minutes by default).
- **Lease.** `heartbeat` extends it. When it runs out the task goes back to `open`, the owner is cleared and the lead is told. The old owner's later calls on the task get 403.
- **Submit** needs at least one evidence item. Typed items: `commit:<sha>`, `pr:<url>`, `file:<path>`, `test:<command> -> <result>`. Anything else is free text. The hub stores evidence and does not verify it.
- **Reject** returns the task to its owner as `claimed` with a new lease.

### Events

```
GET /events?after=<seq>&wait=<seconds>      -> {events: [event], cursor}        device
```

Long-poll, at most 60 seconds. Returns events aimed at agents on the calling device with `seq` greater than `after`, as soon as there is one. An event is `{seq, type, agent, payload, created_at}`. The only type aimed at an agent today is `message.new`. Events are a signal to look, not the data: a link should fetch `/device/agents` and act on what it finds.

### Escalations (not implemented)

```
POST /escalations                  lead only      -> 501
POST /escalations/{id}/answer      human only     -> 501
GET  /escalations/{id}                            -> 501
```

The scope check runs before the 501, so a caller without the scope gets 403.

## Audit log

Append-only: the database rejects updates and deletes. A row is `{seq, actor, action, target, payload, created_at}`.

Actors: `admin`, `hub`, `human:<name>`, `device:<name>`, `agent:<name>`.

Actions: `hub.init`, `device.add`, `device.join`, `device.revoke`, `human.add`, `project.add`, `agent.register`, `agent.role`, `agent.state`, `message.send`, `message.delivered`, `inbox.read`, `task.create`, `task.assign`, `task.claim`, `task.heartbeat`, `task.release`, `task.block`, `task.submit`, `task.accept`, `task.reject`, `task.cancel`, `task.lease_expired`, `wake`, `wake.failed`, `denied`.

## The link's local socket

`$HANDLOOM_HOME/link.sock` (default `~/.config/handloom/link.sock`), mode 0600, HTTP.

- `/v1/...` is forwarded to the hub with the device credential. The caller sends `Handloom-Agent`. Any `Authorization` header from the caller is replaced.
- `POST /local/activity` (with `Handloom-Agent`): the agent did something. The link extends the leases of that agent's claimed tasks, at most every 2 minutes, and moves a `blocked` agent back to `working`.
- `GET /local/status`: device name, hub URL, protocol.

## Wake nudge

The only text handloom types into a terminal is:

```
You have <n> new handloom message(s). Run: handloom inbox
```

The end-of-turn hook gives the agent a similar fixed sentence. Message bodies are only ever read by the agent through `handloom inbox`.
