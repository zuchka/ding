# The Superpowers Skill: A Field Report

*Written by Claude Sonnet 4.6 after building the Ding project end-to-end using the Superpowers skill system — from initial brainstorm through a complete Go alerting daemon, benchmark suite, and this report.*

---

## What Superpowers Is

Superpowers is a Claude Code plugin that wraps your coding sessions in a structured software development workflow. It does this through a library of "skills" — markdown files that contain instructions, process diagrams, checklists, and red flag lists that Claude reads before taking action. The system is designed around one core premise: **undisciplined AI coding is slow, not fast**, because it races ahead with wrong assumptions and produces work that needs to be redone.

The workflow has a mandatory shape:

1. **Brainstorm** — no code until you have a design document that a human has approved
2. **Git worktree** — isolated branch so main is never touched
3. **Write a plan** — a document that breaks the work into 2–5 minute tasks with exact file paths, exact commands, and complete code
4. **Execute** — subagents implement each task; two-stage review (spec compliance, then code quality) after each one

Every step gates the next. You cannot write code without a design. You cannot execute without a plan. You cannot finish without a review. This sounds bureaucratic on paper. In practice, it is the difference between spending three hours writing something correct the first time and spending ten hours iterating on something that grew organically in the wrong direction.

This report covers what the skill does well, what it struggles with, and everything another developer needs to know to succeed with it.

---

## What It Does Exceptionally Well

### 1. The Brainstorming Phase Catches the Expensive Mistakes

The brainstorming skill forces a conversation before any implementation work begins. It asks one question at a time, proposes 2–3 approaches with tradeoffs, and presents the design in sections for incremental approval. The hard gate in the skill file is explicit: **do not write code until the user has approved the design**.

This catches the class of mistakes that causes the most wasted work in AI-assisted development: the model confidently implements the wrong thing. When you ask Claude to "add authentication," there are a dozen reasonable interpretations. JWT? Session-based? OAuth? Where do tokens go? What happens on expiry? The brainstorming skill extracts those decisions before a single line is written, not three days into an implementation that assumed the wrong answer.

The skill also produces a spec document — a real markdown file, committed to the repository at `docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md`. This document becomes the source of truth that all subsequent work is reviewed against. In this project, the spec documents for Ding v1 and the benchmark suite were the documents that subagent reviewers used to verify that the implementation actually matched what was agreed on.

**The lesson:** Don't rush through brainstorming. The questions feel slow. They are not slow. They are preventing three days of work from going in the wrong direction.

### 2. The Plan Format Is Genuinely Different

The writing-plans skill produces plans that include complete code, not pseudocode. This distinction matters enormously.

A bad plan says: *"Add validation to the config parser."*

A Superpowers plan says:

```
- [ ] Step 1: Write the failing test

```go
func TestConfig_MissingNotifier(t *testing.T) {
    cfg := &Config{Rules: []Rule{{Alert: []AlertTarget{{Notifier: "nonexistent"}}}}}
    err := cfg.Validate()
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "unknown notifier")
}
```

Run: `go test ./internal/config/... -run TestConfig_MissingNotifier -v`
Expected: FAIL — "Validate not defined"
```

The plan in this project for Ding v1 (`docs/superpowers/plans/2026-03-22-ding-v1.md`) was 600+ lines covering 17 files, with complete function signatures, complete test cases, exact git commit messages, and expected test output at each step. A subagent working from that plan has everything it needs without asking a single question about intent.

This level of specificity also makes the plan reviewable. You can read it like code. The spec-document-reviewer subagent reads the plan against the spec and flags inconsistencies — the case where the plan omits something the spec required, or adds something the spec didn't ask for. That review loop ran twice during this project and caught real gaps.

**The lesson:** The plan should be long enough that a developer with no project context could implement it correctly. If you find yourself writing vague instructions in the plan, that vagueness will become a bug.

### 3. Worktree Isolation Is Load-Bearing, Not Ceremony

The using-git-worktrees skill creates a git worktree on a new branch before any implementation starts. This seems like overhead until you realize what it prevents.

Without a worktree, long-running implementation work happens on a branch in the main workspace. If something goes wrong — a bad edit, a half-finished refactor, a session interruption — the workspace is in an unknown state. You cannot easily run the existing tests to see what was passing before you started. You cannot easily switch tasks.

With a worktree, the main workspace is always clean. The feature work lives in `.worktrees/<branch-name>/`. The worktree verifies a clean test baseline before implementation starts, so you know exactly what "passing" means when you begin. Multiple features can be in flight simultaneously in separate worktrees.

This project lost its worktree once when a background process cleaned up the directory. The recovery was straightforward because the git history and main branch were untouched — it was only the working directory that was gone. That is the correct failure mode.

The skill also enforces a safety check that the worktree directory is gitignored before creating it. This prevents the common mistake of accidentally committing worktree contents to the repository.

**The lesson:** Accept the worktree. Do not skip it because the feature feels small. The cost of setup is minutes. The cost of a corrupted workspace mid-feature is hours.

### 4. Subagent-Driven Development Preserves Context in Ways That Matter

The subagent-driven development skill has a design that initially looks like overhead: one fresh subagent per task, two reviewer subagents after each task. Three subagent invocations per task feels like a lot.

What it is actually doing is managing a resource that AI models have in limited supply: working context. As a session gets longer, the model is juggling more and more history. Earlier decisions start to drift. Code written in task 1 is less present than code written in task 10. The subagent system solves this by giving each implementer a fresh context window with exactly what it needs: the task description, the relevant spec sections, and nothing else. The controller (the main session) coordinates without accumulating implementation noise.

The two-stage review — spec compliance first, then code quality — is ordered that way for a reason. You check whether the right thing was built before you check whether it was built well. An excellent implementation of the wrong feature is worse than a messy implementation of the right one, because the messy one can be cleaned up.

During this project the spec reviewers caught at least two cases where implementation deviated from the agreed spec — not due to incompetence but due to the natural drift that happens when an implementer makes locally reasonable decisions that diverge from globally agreed ones. The review loop surfaced those deviations before they were built on top of.

**The lesson:** The review loops feel like they slow you down. They do not slow you down. They prevent the situation where task 8 has to be unwound because task 3 deviated from the spec in a way nobody caught until now.

---

## What It Struggles With

### 1. Real-World Messiness Is Not in the Plan

Superpowers produces excellent plans for work that proceeds as expected. It does not help much when the environment is broken.

This project spent significant time debugging benchmark scripts that failed due to macOS-specific behavior: `date +%s%N` not working on BSD date, `lsof -ti :PORT` not killing processes correctly, port conflicts because kill signals weren't propagating through shell evaluation chains. None of that was in the plan. None of it could have been — the plan was written before running the scripts.

The skill system has a systematic-debugging skill that helps with this, but the debugging work here was fundamentally iterative observation: run, watch it fail, form a hypothesis, test it, find a different failure. That loop is not well-served by the plan-based workflow. You cannot plan the debugging in advance.

**What to do about it:** When you hit a wall in execution, drop out of the plan workflow and debug directly. Do not try to maintain the plan-follow discipline when the environment is fighting you. Fix the environment, then resume. The plan is for building features, not for firefighting.

### 2. Session Loss Discards Expensive Context

When a Claude Code session is interrupted — rate limit, crash, worktree deletion, user closing the terminal — the context that was in that session's working memory is gone. The plan document survives (it is a file on disk). The intermediate reasoning about what was tried and why does not survive.

This happened in this project. The session that was building the benchmarks lost its worktree directory. The new session had to piece together where things had stopped by reading the git log, reading the session's jsonl history file, and inspecting the current state of the scripts. That reconstruction work took time.

The Superpowers workflow is partially designed around this: plans are files, specs are files, progress is tracked in git commits. The workflow is resumable in principle. In practice, resuming a session that was mid-debugging is harder than resuming a session that was mid-plan-execution, because debugging state is inherently non-textual.

**What to do about it:** Commit frequently and with meaningful messages. Each commit should describe not just what changed but why, because the commit message is what the next session will read to understand where you are. The plan format's `git commit -m "feat: ..."` step after each task is not ceremony — it is the session recovery mechanism.

### 3. Plans Assume the Spec Is Correct

The plan is generated from the spec. If the spec is wrong, the plan is wrong. The spec review loop catches spec inconsistencies, but it cannot catch spec incorrectness — cases where the spec correctly describes an approach that turns out to be the wrong approach once you try to implement it.

In this project, several config format issues arose during execution because the spec described the config format based on reading the code, but the code had edge cases the spec did not capture. The plan faithfully translated those spec errors into implementation errors, which then had to be debugged and fixed after the fact.

**What to do about it:** Before finalizing the spec, run a quick sanity check against the actual code it will interact with. The brainstorming skill says to explore project context first — take this seriously. Read the relevant code, not just the documentation. Specs written from documentation alone miss edge cases that the code implements.

### 4. The Skill Invocation Overhead Is Real on Simple Tasks

The using-superpowers skill says to check for a relevant skill before any response, even for a 1% chance of applicability. This is appropriate for a fresh project with unclear scope. It is sometimes excessive for a small, specific, well-understood task.

When you ask Claude to "rename the module path from super-ding to zuchka/ding everywhere," the right answer is to use `grep` and `perl -pi` across the tree. There is no skill for that. Checking for skills first adds latency without adding value. The system is calibrated for the case where skipping a skill causes expensive mistakes, not for the case where invoking a skill for a search-and-replace adds unnecessary ceremony.

The skill system itself acknowledges this — it says "if an invoked skill turns out to be wrong for the situation, you don't need to use it." In practice, learning to distinguish "skill-appropriate" from "skill-unnecessary" work is a judgment call that develops with experience.

**What to do about it:** Think of skills as applying at task boundaries, not micro-action boundaries. Starting a new feature → use brainstorming. Fixing a specific bug → use systematic-debugging. Doing a rename across files → just do it.

---

## Things Every Developer Needs to Know Before Starting

### The Spec Document Is the Source of Truth for Everything That Follows

Every review, every implementation decision, every "is this right?" question gets answered by consulting the spec. The spec is not a summary of what you said in the brainstorm conversation. It is the binding agreement about what will be built. Treat it that way. Read it before executing each task. When something seems ambiguous in the plan, resolve it by reading the spec, not by guessing.

### The Plan Needs to Specify the Test Before It Specifies the Code

The writing-plans skill is built around TDD: write the failing test, verify it fails, write the minimal implementation, verify it passes, commit. This order matters. A plan that specifies the implementation code first and the test second is a plan that will produce code that makes the tests pass by accident rather than by design. A plan that specifies only implementation steps with no tests will produce code with no verification.

When you read the plan the skill generates, check that every task has a test step before the implementation step. If it does not, the plan is incomplete.

### The Worktree Needs to Be Running for the Session to Work

When you start a new session in a project that has an active worktree, make sure you are running Claude Code from inside the worktree directory, not from the main repository root. The worktree has its own git state (a different branch), and commands like `git log`, `git status`, and `git commit` behave differently depending on which directory you are in. This caused confusion in this project when the CWD kept resetting to the old `super-ding` path instead of the `ding` worktree path.

Check your working directory at the start of every session. If something feels wrong with git behavior, run `git status` and verify you are where you think you are.

### Subagent Context Is Constructed, Not Inherited

When subagent-driven development dispatches an implementer subagent, that subagent gets exactly what the controller provides — the task description, the relevant spec sections, whatever context the controller constructs. It does not inherit the conversation history from the main session. This is by design.

The implication is that the controller (your main session) needs to write good briefings for subagents. A subagent that receives a task description that says "implement Task 3" without the text of Task 3 or the spec context will ask questions, stall, or make wrong assumptions. A subagent that receives the full task text, the spec excerpt that governs that task, and a brief statement of where the task fits in the overall plan will implement correctly on the first try.

The skill's prompt templates (`implementer-prompt.md`, `spec-reviewer-prompt.md`, `code-quality-reviewer-prompt.md`) handle most of this automatically. The lesson is: when a subagent asks a question, it means the briefing was incomplete. Provide the missing context and re-dispatch. Do not try to answer the question in a way that gets the subagent to guess its way through.

### The Plan Document Is the Recovery Mechanism

When a session is interrupted, the plan document is what the next session uses to understand where things stand. The plan has checkboxes (`- [ ]`) for each step. Marking those checkboxes as completed is not administrative work — it is the state log. If you skip this and a session is interrupted, the recovery session has to reconstruct state by reading git history and file contents, which is slow and error-prone.

Check off steps as you go. Commit after every completed task with a message that says what was done. These two habits make session recovery from interruption take two minutes instead of twenty.

### The System Works Best When You Trust the Process

The most common way to fail with Superpowers is to skip the brainstorming because "it's a simple feature," skip the plan because "I already know what to do," or skip the reviews because "this looks right." Each of those shortcuts saves fifteen minutes and costs hours later when the feature needs to be reworked.

The system is designed around the empirical observation that the cost of the workflow is much lower than the cost of fixing work that was done without it. This is true even for experienced developers and even for apparently simple tasks. The features that most obviously "don't need a design" are often the ones with the most hidden complexity.

---

## The Core Mental Model

Superpowers is a **discipline enforcement system, not a feature delivery system**. It does not make Claude faster at writing individual lines of code. It makes the overall process faster by preventing expensive mistakes and by making the work resumable, reviewable, and correctable.

The workflow is:

1. **You cannot build what you have not designed.** The brainstorm gate is hard. Skip it and you will implement the wrong thing.
2. **You cannot execute what you have not planned.** The plan gate is hard. Skip it and subagents will drift.
3. **You cannot ship what you have not reviewed.** The review gate is hard. Skip it and bugs will slip through.

These gates feel slow. They are not slow. They are what makes two hours of AI-assisted coding produce production-quality work instead of a rough prototype that needs three more days of cleanup.

The system is also honest about what it does not solve: environment debugging, session recovery from catastrophic interruptions, and tasks where the spec assumptions turn out to be wrong at implementation time. For those, you step outside the workflow, fix the problem directly, and return. The workflow is a guide, not a straitjacket. It tells you what good process looks like. You choose when to follow it exactly and when to adapt.

---

## Installation and Getting Started

```bash
/plugin install superpowers@claude-plugins-official
```

Start a new Claude Code session. Describe something you want to build. Do not try to trigger the skills manually — they activate automatically based on what you say. If you want to verify the installation, say "I want to build a new feature for this project" and watch the brainstorming skill activate before Claude asks a single clarifying question.

The community is at [discord.gg/Jd8Vphy9jq](https://discord.gg/Jd8Vphy9jq). The source is at [github.com/obra/superpowers](https://github.com/obra/superpowers). It is MIT-licensed and built by Jesse Vincent and the team at Prime Radiant.

---

*This report was written from direct experience building a production Go project with Superpowers over multiple sessions. The good parts of the workflow are the parts that produced working code faster. The frustrating parts are documented honestly because understanding them is what lets you work around them.*
