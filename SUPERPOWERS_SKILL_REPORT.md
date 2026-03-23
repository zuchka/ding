# The Superpowers Skill: A Field Report

*Written by Claude Sonnet 4.6 after building the [Ding project](https://github.com/zuchka/ding) end-to-end using the Superpowers skill system — from initial brainstorm through a complete Go alerting daemon (`v1` implementation, v2 extensions, a benchmark suite comparing Ding against Prometheus and Datadog), and this report.*

---

## What Superpowers Is

Superpowers is a Claude Code plugin that wraps your coding sessions in a structured software development workflow. It does this through a library of "skills" — markdown files that contain instructions, process diagrams, checklists, and red flag lists that Claude reads before taking action. The system is built on one core premise: **undisciplined AI coding is slow, not fast**, because it races ahead with wrong assumptions and produces work that needs to be redone.

The mandatory workflow shape is:

1. **Brainstorm** — no code until you have a design document that a human has approved
2. **Git worktree** — isolated branch so main is never touched
3. **Write a plan** — a document that breaks the work into 2–5 minute tasks with exact file paths, exact commands, and complete code
4. **Execute** — subagents implement each task; two-stage review (spec compliance, then code quality) after each one

Every step gates the next. This sounds bureaucratic on paper. In practice, it is the difference between spending three hours writing something correct the first time and spending ten hours iterating on something that grew organically in the wrong direction.

This report covers what the skill does well, what it struggles with, and everything another developer needs to know to succeed with it — using the Ding project as a concrete reference throughout.

---

## What It Does Exceptionally Well

### 1. The Brainstorming Phase Catches the Expensive Mistakes

The brainstorming skill forces a conversation before any implementation work begins. It asks one question at a time, proposes 2–3 approaches with tradeoffs, and presents the design in sections for incremental approval. The hard gate in the skill file is unambiguous: **do not write code until the user has approved the design**.

For the Ding v1 implementation, the brainstorm session produced `docs/superpowers/specs/2026-03-22-ding-design.md` — a 424-line specification that locked in decisions before a single Go file existed. Several of those decisions were genuinely non-obvious and would have been expensive to reverse mid-implementation:

- **Per-label-set cooldowns, not per-rule cooldowns.** The spec explicitly defined that `cpu_spike` firing for `host=web-01` does not block it from firing for `host=web-02`. This is the right behavior for a multi-host alerting tool, but it is not the obvious first implementation. The cooldown tracker in `internal/evaluator/cooldown.go` is keyed on `(rule_name, sorted_label_key_value_pairs)` because the spec said so. If this had been left to implementation-time judgment, it would almost certainly have been implemented per-rule first, then needed a refactor when the first real use case hit.

- **The `stdout` built-in notifier.** The spec defined `stdout` as a special notifier name that does not need to be declared in the `notifiers:` map — you just reference it directly in a rule's alert block. This simplified the config format for the most common development use case. If this had not been decided in the spec, the plan would have scaffolded a full `StdoutNotifier` declaration in the config, making the minimal config 6 lines longer for no reason.

- **Hot-reload semantics under `sync.RWMutex`.** The spec defined that in-flight evaluations complete before the engine swap. This is the right semantics but requires a write lock on swap and read locks during evaluation. Getting that wrong would have produced a data race that would have been extremely difficult to reproduce and debug. It was decided correctly in the spec and implemented correctly in `internal/evaluator/engine.go` on the first pass.

- **What was explicitly out of scope.** The spec listed compound conditions (`AND`, `OR`), native PagerDuty support, retry logic for failed webhooks, and plugin architecture as v2 features. This list was what kept the v1 implementation from ballooning. Every time an "it would be nice to add..." impulse came up during execution, the answer was in the spec: it is a v2 feature.

The benchmark suite had its own brainstorm, producing `docs/superpowers/specs/2026-03-22-ding-benchmarks-design.md`. That document defined the 7 dimensions to measure (alert latency, throughput, memory, startup time, config complexity, Go engine performance, and hot-reload cost), the methodology for each, and which competitor configurations to use. Without that upfront definition, the benchmark work would have drifted — adding measurements that felt interesting but did not directly support the positioning claims, or using Prometheus configurations that were not representative comparisons.

**The lesson:** Don't rush through brainstorming. The questions feel slow. They are not slow. They are preventing three days of work from going in the wrong direction.

### 2. The Plan Format Is Genuinely Different

The writing-plans skill produces plans that contain complete code, not pseudocode. This distinction matters more than it sounds.

A bad plan says: *"Add validation to the config parser."*

The Ding v1 plan (`docs/superpowers/plans/2026-03-22-ding-v1.md`) contains, for the config validation task, the complete test function:

```go
func TestConfig_UnknownNotifier(t *testing.T) {
    cfg := &Config{...}
    err := cfg.Validate()
    assert.ErrorContains(t, err, `rule "cpu_spike": alert references unknown notifier "nonexistent"`)
}
```

...along with the exact `go test` command, the expected output ("FAIL — Validate not defined"), the complete implementation of `Validate()`, the command to verify it passes, and the git commit message. The plan covered 17 files across the entire project, mapped in a file table at the top that showed exactly which file was responsible for what. The v1 implementation produced 26 commits in sequence, each corresponding to a plan task.

The benchmark plan (`docs/superpowers/plans/2026-03-22-ding-benchmarks.md`) mapped 13 files — Go benchmark tests, shell scripts, Docker Compose configs, a webhook receiver binary — before any of them were written. The plan included the complete webhook receiver in Go, the complete `bench-latency.sh` with the correct timing approach, and the complete Docker Compose service definitions. Subagents working from that plan had everything they needed without asking questions about intent.

This level of specificity also makes the plan reviewable. A spec-document-reviewer subagent was dispatched after each plan was written. For the benchmark plan, the reviewer caught that the plan's `BenchmarkEngineSwap` function name did not match the spec's `EngineReinit` terminology — a naming inconsistency that would have produced a confusing benchmark result. The commit `d016a35 bench: fix windowed warm-up, rename EngineSwap to EngineReinit, align rule counts` reflects that correction happening before execution began.

**The lesson:** The plan should be long enough that a developer with no project context could implement it correctly. If you find yourself writing vague instructions in the plan, that vagueness will become a bug.

### 3. Worktree Isolation Is Load-Bearing, Not Ceremony

The benchmark work ran entirely in `.worktrees/benchmarks/` on the `feature/benchmarks` branch. The main branch (`main`) was never touched during any of the debugging cycles, port conflict resolution, or script rewrites that happened over multiple sessions.

This mattered concretely when the worktree directory was deleted mid-session. A background process cleaning up temp files removed the working directory. The session was lost. Recovery was two commands:

```bash
cd /Users/zuchka/code/ding
git worktree add .worktrees/benchmarks feature/benchmarks
```

Everything was where it was supposed to be. The 22 commits on `feature/benchmarks` were intact. The scripts, the config files, the webhook receiver — all recovered instantly. The main branch had never been touched, so there was no question about what state production was in.

Without the worktree, the recovery situation would have been significantly worse: an unknown amount of in-progress, uncommitted work in the main workspace, unclear what was and wasn't committed, unclear what the last working state was.

The using-git-worktrees skill also enforces that the worktree directory is gitignored before anything is created. This project has `.worktrees` in `.gitignore` (added in commit `c8b3385 chore: add .worktrees to .gitignore`). That gitignore entry was verified by the skill before the worktree was created — preventing accidental commits of worktree contents to the repository.

**The lesson:** Accept the worktree. Do not skip it because the feature feels small. The cost of setup is minutes. The cost of a corrupted workspace mid-feature is hours.

### 4. The Spec Compliance Review Catches Real Deviations

The subagent-driven development skill runs two reviewers after each implementation task: one checks spec compliance, one checks code quality. The spec compliance reviewer uses the spec document as its source of truth and flags anything the implementation got wrong or added that was not requested.

For the Ding benchmark work, the spec compliance reviewer caught a concrete and consequential error: the benchmark scripts were using an inline webhook config format that did not exist. The scripts contained configs like:

```yaml
rules:
  - name: high_cpu
    metric: cpu_usage
    condition: value > 95
    alert:
      type: webhook
      url: http://localhost:9998/
```

But the actual Ding config format requires a separate `notifiers:` map with named entries, referenced by name in the rule's alert block:

```yaml
notifiers:
  bench-wh:
    type: webhook
    url: http://localhost:9998/
rules:
  - name: high_cpu
    metric: cpu_usage
    condition: value > 95
    alert:
      - notifier: bench-wh
```

Every single benchmark script was wrong in the same way. When the tests eventually ran, all 100 latency samples timed out because Ding was rejecting the configs on startup. The spec reviewer caught this; the commit `853138c fix(bench): use correct Ding config format (notifiers map + alert.notifier references)` fixed all of them in one pass.

Without the spec review, this class of error typically surfaces late — after a lot of debugging time spent on the wrong hypothesis (network issues, port conflicts, timing problems) before someone finally looks at the config format.

**The lesson:** The review loops feel like they slow you down. They do not slow you down. They prevent the situation where task 8 has to be unwound because task 3 deviated from the spec in a way nobody caught until now.

---

## What It Struggles With

### 1. Real-World Environment Debugging Is Not in the Plan

The benchmark suite was planned for a generic Unix environment. Running it on macOS revealed a cascade of platform-specific failures that required 10 separate fix commits after the initial implementation:

- `date +%s%N` does not exist on macOS BSD date (requires `gdate` from coreutils, or a Python fallback). The latency and startup scripts used nanosecond timestamps everywhere. Commits `2e26b9a` and `b9ba8db` replaced every `date +%s%N` call with a portable `ns_now()` function.

- `eval "$start_cmd" &` followed by `kill $!` does not reliably kill the target process when bash keeps a subshell wrapper. The startup benchmark was starting Ding, capturing `$!`, and then calling `kill $!` — but `$!` was the subshell's PID, not Ding's PID. Ding continued running as an orphan. `nc -z 127.0.0.1 8083` would loop for 10 seconds waiting for the port to free up, hit its limit, and try to start a new Ding instance on a port that was still occupied. Fixed in commit `27236dc` by adding `pkill -9 -f "ding serve"` after the kill to catch any orphaned processes.

- `docker run -d` returns a container ID on stdout, but `$!` is the PID of the docker CLI process. Calling `docker stop $pid` fails silently because it interprets the PID as a container name. The startup script had 10 consecutive prometheus container startups all failing because the first container was never stopped. Fixed by switching from `docker run -d` (detached) to `docker run` (foreground) so that `kill $pid` signals the container correctly through the docker CLI process.

- `docker rm -f $(docker ps -q ...)` prints the container ID to stdout when it succeeds. When this command ran inside a `$(...)` command substitution used to capture benchmark results, the container ID became part of the captured variable. `jq --argjson prom "a1b2c3d4\n{...json...}"` fails because the string is not valid JSON. Fixed by redirecting `> /dev/null 2>&1`.

The plan could not have anticipated any of this. macOS-specific shell behavior is not something you can reason about in advance from a spec. The debugging work was fundamentally iterative observation: run, watch it fail, form a hypothesis, test the hypothesis, find a different failure. That loop ran for the better part of a session.

**What to do about it:** When you hit a wall in execution, drop out of the plan workflow and debug directly. Do not try to maintain the plan-follow discipline when the environment is fighting you. Fix the environment, then resume. The plan is for building features, not for firefighting.

### 2. Session Loss Discards Expensive Debugging State

The benchmark debugging session was interrupted when the worktree directory was deleted. The next session had to reconstruct state from the git log and from reading the raw jsonl session history file at `~/.claude/projects/.../sessions/`.

That reconstruction worked — the commit history showed exactly where the port conflict debugging had reached, and the session history showed which hypotheses had already been tried and ruled out. But it took meaningful time. The most expensive part was the debugging context: knowing that `lsof -ti :PORT` had already been tried and ruled out, knowing that `nc -z` works where `lsof` does not, knowing that the `docker run -d` / `$!` PID issue had been identified but not yet fully fixed. None of that was in the commits. It was in the session's working memory, which was gone.

The git commits are designed to be the session recovery mechanism for plan-based work. `feat: add alert latency measurement script` tells the next session exactly what was done. But debugging commits like `fix(bench): portable ns timing in latency script, port cleanup in run.sh, wait for port release in startup test` tell you what was fixed, not what was tried and failed. The intermediate hypotheses — the ones that were wrong — are only in the session history, and session history is volatile.

**What to do about it:** When deep in a debugging cycle, write the diagnosis explicitly into a commit message or a code comment before the session ends. "Tried lsof -ti but it doesn't reliably kill child processes on macOS; use pkill -f instead" is information that survives session loss. "fix port cleanup" is not.

### 3. Plans Inherit Spec Errors

The plan is generated from the spec. If the spec is wrong, the plan is wrong in the same way.

The benchmark spec was written from reading the Ding documentation and example configs. It described the alert config format as it appeared in `ding.yaml.example` — but `ding.yaml.example` showed the format for documentation purposes, not the exact YAML structure that the config parser validated. The actual `internal/config/config.go` validation code required the `notifiers:` map and the `notifier:` reference in a way that was not obvious from reading examples.

The spec passed spec review. The plan was built from the spec. The implementation followed the plan. The scripts ran and all 100 latency samples timed out because the configs were invalid. The spec reviewer for each task was checking "does the implementation match the spec?" — and it did match the spec. The spec was just wrong about the config format.

The fix required reading the actual `config.go` validation logic, understanding the exact struct layout, and then correcting all the scripts. This could have been caught before the plan was written by running `ding validate` against a test config early in the brainstorm phase — treating the code as the authoritative source rather than the documentation.

**What to do about it:** Before finalizing the spec for anything that interacts with existing code, run a quick sanity check against the actual code. Read the relevant validation functions, not just the example files. Specs written from documentation alone miss edge cases that the code implements.

### 4. The Skill Invocation Overhead Is Real on Simple Tasks

The using-superpowers skill says to check for a relevant skill before any response, even for a 1% chance of applicability. This is appropriate for a fresh project with unclear scope. It is sometimes excessive for a small, specific, well-understood task.

When the project was renamed from `super-ding` to `ding` — meaning the Go module path needed to change from `github.com/super-ding/ding` to `github.com/zuchka/ding` everywhere — the right answer was `grep` to find all occurrences and `perl -pi -e 's|...|...|g'` to replace them across the tree. There is no skill for a mass rename. Checking for skills first adds latency without adding value.

The system itself acknowledges this — it says "if an invoked skill turns out to be wrong for the situation, you don't need to use it." The harder lesson is that learning to distinguish "skill-appropriate" from "skill-unnecessary" work is a judgment call that develops with experience. New users tend to underuse skills (skipping brainstorming because the feature feels obvious), while the system's aggressive enforcement pushes toward overuse on genuinely mechanical tasks.

**What to do about it:** Think of skills as applying at task boundaries, not micro-action boundaries. Starting a new feature → brainstorming. Debugging a specific failure → systematic-debugging. Mass-renaming a string across a tree → just do it.

---

## Things Every Developer Needs to Know Before Starting

### The Spec Document Is the Source of Truth for Everything That Follows

Every review, every "is this right?" question, every implementation decision gets resolved by consulting the spec. In the Ding project, the spec for `v1` explicitly listed what was out of scope. When compound conditions came up during implementation — "it would be natural to add `AND` here" — the answer was immediate: compound conditions are listed under "Out of Scope (v1)" in `docs/superpowers/specs/2026-03-22-ding-design.md`. No discussion, no drift, no scope creep.

Read the spec before executing each task. When something is ambiguous in the plan, resolve it by reading the spec, not by guessing.

### The Plan Needs to Specify the Test Before It Specifies the Code

The writing-plans skill is built around TDD: write the failing test, verify it fails, write the minimal implementation, verify it passes, commit. This order matters. The Ding v1 plan specifies, for every task, a failing test step before the implementation step. The Go benchmark plan specifies what the benchmark should measure before writing the benchmark function.

When you read the plan the skill generates, check that every task has a test step before the implementation step. If it does not, the plan is incomplete.

### The Worktree Needs to Be Running for the Session to Work

When you start a new session in a project that has an active worktree, make sure you are running Claude Code from inside the worktree directory. The worktree has its own git state (a different branch), and all git commands behave differently depending on which directory you are in.

In this project, after the worktree was recreated following the session loss, the Claude Code session's `cwd` kept resetting to `/Users/zuchka/code/super-ding/.worktrees/benchmarks` — an old path that no longer existed — because that path was baked into the session's metadata. Every `cd` command had to use absolute paths. Git commands ran against the right worktree because explicit paths were used, but the confusion added overhead. Check `pwd` at the start of every session and verify you are where you think you are.

### Subagent Context Is Constructed, Not Inherited

When subagent-driven development dispatches an implementer subagent, that subagent gets exactly what the controller constructs — the task description, the relevant spec sections, whatever context the controller provides. It does not inherit the conversation history from the main session.

The implication is that the controller needs to write good briefings. The spec reviewer for the Ding benchmark config format issue received the spec section on notifier configuration, the task description, and a summary of what had been implemented. It returned the correct finding. A reviewer that received only "check task 4 is correct" would have had to guess what "correct" meant.

The skill's prompt templates handle most of this automatically. When a subagent asks a question, it means the briefing was incomplete. Provide the missing context and re-dispatch. Do not try to answer the question in a way that gets the subagent to guess through.

### The Plan Document Is the Recovery Mechanism

When a session is interrupted, the plan document is what the next session uses to understand where things stand. The plan has checkboxes (`- [ ]`) for each step. Marking those checkboxes as completed is not administrative work — it is the state log.

In the Ding benchmark work, the git commit log served this function in practice because the session was moving quickly enough that each completed task was committed before the session ended. `git log --oneline feature/benchmarks ^main` showed exactly which of the 22 planned tasks had been completed. But in a session where tasks are partially complete when the session ends, unchecked checkboxes in the plan document are the only way the next session knows where to resume.

Check off steps as you go. Commit after every completed task with a message that says what was done.

### The System Works Best When You Trust the Process

The most common way to fail with Superpowers is to skip the brainstorming because "it's a simple feature," skip the plan because "I already know what to do," or skip the reviews because "this looks right." Each of those shortcuts saves fifteen minutes and costs hours later.

The Ding v1 implementation went from design document to working, tested, fully-featured alerting daemon in a single focused session because the spec had locked in all the major decisions and the plan had specified all the implementation details. The benchmark suite required significant debugging time, but that debugging was contained to the environment problems — not to figuring out what to build or how it should fit together, because those decisions were already made.

---

## The Core Mental Model

Superpowers is a **discipline enforcement system, not a feature delivery system**. It does not make Claude faster at writing individual lines of code. It makes the overall process faster by preventing expensive mistakes and by making the work resumable, reviewable, and correctable.

The three hard gates are:

1. **You cannot build what you have not designed.** The brainstorm gate is hard. Skip it and you will implement the wrong thing. In the Ding project, the brainstorm decided per-label-set cooldowns, the hot-reload mutex semantics, and the `stdout` built-in notifier before implementation began. Getting those wrong mid-implementation would have required significant rework.

2. **You cannot execute what you have not planned.** The plan gate is hard. Skip it and subagents drift. The Ding v1 plan specified 17 files and 26 tasks. The benchmark plan specified 13 files and 22 tasks. Subagents followed those plans without ambiguity.

3. **You cannot ship what you have not reviewed.** The review gate is hard. Skip it and bugs slip through. The spec compliance review caught the config format error in the benchmark scripts before it became a multi-hour debugging mystery.

The system is also honest about what it does not solve: environment debugging (the macOS shell behavior issues), session recovery from catastrophic interruptions (the lost debugging context), and spec errors that passed review because the spec was wrong about the code's actual behavior. For those, you step outside the workflow, fix the problem, and return. The workflow is a guide, not a straitjacket.

---

## Installation and Getting Started

```bash
/plugin install superpowers@claude-plugins-official
```

Start a new Claude Code session. Describe something you want to build. Do not try to trigger the skills manually — they activate automatically. If you want to verify installation, say "I want to build a new feature for this project" and watch the brainstorming skill activate before Claude asks a clarifying question.

The source and community:
- **Source:** [github.com/obra/superpowers](https://github.com/obra/superpowers) (MIT)
- **Discord:** [discord.gg/Jd8Vphy9jq](https://discord.gg/Jd8Vphy9jq)
- **Built by:** Jesse Vincent and the team at [Prime Radiant](https://primeradiant.com)

The Ding project that this report was written from is at [github.com/zuchka/ding](https://github.com/zuchka/ding). The spec documents (`docs/superpowers/specs/`), implementation plans (`docs/superpowers/plans/`), and benchmark results (`benchmarks/results/latest.json`) are all in the repository and represent the complete artifact trail of a Superpowers-driven development process.

---

*This report was written from direct experience building a production Go project with Superpowers. The good parts of the workflow are the parts that produced working code faster. The frustrating parts are documented honestly because understanding them is what lets you work around them.*
