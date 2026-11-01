// Package playbook compiles versioned delivery recipes into ordinary task plans.
package playbook

import (
	"fmt"
	"strings"

	"handloom/internal/plan"
)

const MVP = "mvp-v1"
const LeadProfile = "mvp-orchestrator"

// Profiles deliberately have their own namespace: installing a playbook never
// changes a user's existing starter profiles.
var ProfileStarters = map[string]string{
	LeadProfile: "lead-engineering", "mvp-product": "product-manager",
	"mvp-design": "frontend-designer", "mvp-architect": "coder",
	"mvp-backend": "backend-dev", "mvp-frontend": "frontend-dev",
	"mvp-qa": "qa-tester", "mvp-security": "security-reviewer",
	"mvp-review": "code-reviewer", "mvp-release": "devops",
}

const LeadInstructions = `This job uses the system-managed MVP playbook. The task graph already exists: inspect it with handloom job plan and handloom task list. Do not create, assign, or spawn workers; the controller handles those operations. Review each submitted task against its required outputs and the device's checks, then accept or reject with a concrete reason. Do not treat successful commands as proof of usability. Read independent QA, security and code-review findings before accepting release readiness; unresolved critical/high findings block acceptance. Resolve design disagreements in one recorded decision; bound review to one review and one remediation round, then report an explicit blocker. Never invent credentials or deployment success. Release readiness is a handoff, not a deployed product. Ask the owner only about missing access or a consequential ambiguity that prevents useful progress.`

const Stack = `Default stack for new web products: TypeScript, Next.js, PostgreSQL, a single migration layer, established authentication, accessible reusable UI components, Playwright browser tests, and a Docker image intended for Dokploy. Pin exact supported versions in the lockfiles and record them in docs/architecture.md. For an existing product, preserve its stack unless the brief explicitly asks for migration. This recipe selects a stack; it does not provision a database or install a template.`

func Compile(name, brief string) (plan.Plan, error) {
	if name != MVP {
		return plan.Plan{}, fmt.Errorf("unknown playbook %q; available: %s", name, MVP)
	}
	brief = strings.TrimSpace(brief)
	if brief == "" || len(brief) > 16000 {
		return plan.Plan{}, fmt.Errorf("product brief must contain 1 to 16000 bytes")
	}
	common := "Read the original product brief in the job and the handoff/evidence of dependency tasks. Preserve the owner's intent; label assumptions. Work only in your profile's allowed paths. Commit changes in your worktree before submitting (read-only reviewers submit evidence only). Submit artifact paths, tests actually run, results, limitations, and a handoff note. Do not claim unrun checks passed.\n\n"
	steps := []plan.Step{
		{Key: "brief", Title: "Define the product and acceptance criteria", Profile: "mvp-product", Body: "Write docs/specs/product.md: original request, target users, core user journeys, MVP boundaries, explicit exclusions, measurable acceptance criteria, risks and assumptions. Optimize the request into a precise brief without adding unrequested features. Ask only when a missing decision prevents progress."},
		{Key: "ux", Title: "Design the user journeys", Profile: "mvp-design", DependsOn: []string{"brief"}, Body: "Write docs/design/ux.md: primary journeys, navigation, screen inventory, interactions, loading/empty/error states and accessibility requirements. Check the journey against each acceptance criterion; keep the everyday path simple."},
		{Key: "architecture", Title: "Define the stack and interfaces", Profile: "mvp-architect", DependsOn: []string{"brief"}, Body: Stack + "\nWrite docs/architecture.md with data model, API contracts, authorization boundaries, implementation ownership, test strategy and environment-variable names (never secret values). Reconcile product scope and security risks; identify assumptions that need the lead's decision."},
		{Key: "ui", Title: "Define the visual design", Profile: "mvp-design", DependsOn: []string{"ux", "architecture"}, Body: "Read docs/design/ux.md and docs/architecture.md. Write docs/design/ui.md and design/ artifacts for layout, typography, tokens, component states and responsive behavior. Define concrete visual acceptance criteria. Avoid redesigning the product scope."},
		{Key: "backend", Title: "Build the backend and data layer", Profile: "mvp-backend", DependsOn: []string{"architecture", "ui"}, Body: "Implement the backend contracts, migrations, authentication/authorization and integration tests. Establish the pinned dependency baseline and application boot path. Cover invalid inputs and permission boundaries. Keep schema changes compatible and document migration/recovery requirements."},
		{Key: "frontend", Title: "Build complete user journeys", Profile: "mvp-frontend", DependsOn: []string{"backend", "ui"}, Body: "Implement the agreed UI against the integrated backend. Complete the primary user journeys including loading, empty and error states. Run browser checks at desktop and mobile sizes; capture rendered-screen evidence and compare it with docs/design/ui.md. Do not substitute a static mock for a working flow."},
		{Key: "qa", Title: "Independently test usability and behavior", Profile: "mvp-qa", DependsOn: []string{"frontend"}, Body: "Independently derive tests from docs/specs/product.md and docs/design/ux.md, not from implementation summaries. Add/run browser and integration tests for core journeys, failures, authorization and accessibility. Inspect rendered screens against the design. Report reproducible findings with severity and evidence in the task submission; do not change application code or weaken acceptance criteria."},
		{Key: "security", Title: "Independently review security", Profile: "mvp-security", DependsOn: []string{"frontend"}, Body: "Review the integrated product's trust boundaries, auth/session behavior, authorization, input handling, secret exposure, dependencies and database access. Report each finding with severity, evidence, affected path and recommended fix. Be explicit about checks you could not perform. Do not modify implementation or approve your own work."},
		{Key: "review", Title: "Review implementation and maintainability", Profile: "mvp-review", DependsOn: []string{"frontend"}, Body: "Independently review the integrated code against the brief and architecture. Report correctness and maintainability findings with severity and evidence; verify tests cover the product's risks. Do not edit the implementation. Give a recommendation with limitations rather than an unexplained approval."},
		{Key: "release", Title: "Prepare the release handoff", Profile: "mvp-release", DependsOn: []string{"qa", "security", "review"}, Body: "Prepare or verify Docker/CI/deployment files, health checks and a release runbook under deploy/. Record exact commit/image, configuration names, migration strategy, backup/rollback requirements and the smoke-test checklist. Confirm the lead has resolved blocking QA/security/review findings. Do not create GitHub repositories, push, or deploy: those connectors and launch policies are not implemented by this recipe. State 'ready for deployment' only with supporting evidence; never invent a live URL."},
	}
	for i := range steps {
		steps[i].Body = common + steps[i].Body
	}
	p := plan.Plan{Version: 1, Playbook: MVP, MaxAttempts: 2, Steps: steps}
	_, err := p.Order()
	return p, err
}
