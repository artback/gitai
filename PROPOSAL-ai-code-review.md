# Feature Proposal: `gitai review` — AI-Powered Pre-Commit Code Review

## Summary

Add a `gitai review` command that analyzes staged/changed diffs using the existing AI provider infrastructure and returns actionable code review feedback before committing. This naturally extends gitai's core value proposition — it already understands diffs deeply for commit message generation; surfacing review insights from the same diffs is a high-leverage addition.

## Motivation

Developers using gitai already trust the tool to analyze their changes at commit time. A common workflow gap exists between writing code and committing it: self-review. Many bugs, style issues, and security problems are caught during code review but could be identified earlier. By the time a PR is opened, the cost of fixing issues is higher (context switching, CI re-runs, reviewer wait time).

`gitai review` closes this gap by giving developers instant AI feedback on their changes *before* they commit, using the same AI providers they've already configured.

## Proposed UX

### CLI Usage

```bash
# Review all changed files (interactive file selection, same as suggest)
gitai review

# Review specific files
gitai review src/auth.go src/middleware.go

# Review with a focus hint
gitai review --hint "check for SQL injection"

# Quick mode — skip file selector, review everything
gitai review --all

# Output as JSON for CI/editor integration
gitai review --format json
```

### Interactive TUI Flow

1. File selector (reuse existing `file_selector.go`)
2. Spinner: "Reviewing changes..."
3. Review results displayed in a scrollable view grouped by file:
   - Each finding has: severity (critical/warning/info), file + line range, description, suggested fix
4. Keybindings:
   - `Enter` — expand/collapse a finding
   - `f` — filter by severity
   - `s` — switch to `suggest` flow (commit after reviewing)
   - `q` — quit

### Example Output (non-interactive)

```
src/auth.go
  ⚠ warning (L42-45): Plaintext password comparison — use constant-time
    comparison (subtle.ConstantTimeCompare) to prevent timing attacks.

  ● info (L78): Unused error return from db.Close() — consider logging
    or handling.

internal/api/handler.go
  ✖ critical (L23): User input passed directly to fmt.Sprintf in SQL
    query — use parameterized queries to prevent SQL injection.

  ⚠ warning (L91-95): Goroutine launched without context cancellation
    propagation — pass parent context to avoid leaks.

Summary: 1 critical, 2 warnings, 1 info across 2 files
```

## Architecture

### What to Reuse (Existing)

| Component | Location | Reuse |
|---|---|---|
| AI provider infrastructure | `internal/ai/provider/` | 100% — same providers, same config |
| Diff generation | `internal/git/git.go` | 100% — `GetChangesForFiles`, `generateBatchDiff` |
| File selector TUI | `internal/tui/suggest/file_selector.go` | 100% — identical workflow |
| Security scanner | `internal/security/` | Complement — run alongside review |
| Config loading | `internal/config/` | Extend with review-specific options |
| Hint processing | `internal/tui/suggest/hint_processing.go` | Reuse for ticket context |

### What to Build (New)

```
internal/
  ai/
    review.go              # ReviewService — new AI prompt + structured parsing
    review_prompt.md       # System prompt for code review
  tui/
    review/
      review_flow.go       # Orchestrates the review TUI flow
      results_model.go     # Bubbletea model for displaying findings
cmd/
  review.go                # Cobra command definition
```

### Key Design Decisions

**1. Separate system prompt, not overloaded onto suggest**

The review prompt needs different instructions (find issues, cite line numbers, categorize severity) than the commit message prompt. Keeping them separate maintains the quality of both.

**2. Structured output parsing**

Ask the AI to return findings in a lightweight structured format (e.g., one finding per block with severity/location/description markers) so the TUI can render them properly. Fall back to plain text display if parsing fails.

**3. Streaming support (future)**

The `provider.AIProvider` interface currently returns a full string. A future enhancement could add streaming to show findings as they arrive, but the initial version works fine with the existing blocking API.

**4. Composability with suggest**

After reviewing, users should be able to press `s` to seamlessly transition into the commit flow. The selected files and context carry over — no need to re-select.

## Review Prompt Design

The system prompt for review should instruct the AI to:

- Focus only on the **changed lines** (not the entire file context it can't see)
- Categorize findings: `critical` (bugs, security), `warning` (code smells, potential issues), `info` (style, minor improvements)
- Include approximate line numbers from the diff hunks
- Provide a concrete suggestion for each finding, not just a description
- Be concise — developers won't read paragraphs
- Avoid false positives by only flagging things with reasonable confidence

## Configuration

Extends the existing config structure:

```yaml
review:
  severity: warning     # minimum severity to display (critical, warning, info)
  max_findings: 20      # cap AI output to avoid noise
  categories:           # which review categories to enable
    - security
    - bugs
    - performance
    - style
```

## Why This Feature Specifically

1. **Highest leverage** — reuses 70%+ of existing infrastructure (AI providers, diff engine, TUI, config). The marginal cost is low; the marginal value is high.
2. **Natural workflow fit** — developers already run `gitai suggest` at commit time. Adding `gitai review` as the step right before is a natural extension, not a context switch.
3. **Differentiation** — most AI commit message tools only do commit messages. Adding review creates a more complete "AI-assisted commit workflow" that competitors don't offer.
4. **Monetization path** — review is a feature teams pay for (see: CodeRabbit, Codacy, etc.). Even as a local CLI tool, it positions gitai for team/enterprise features later.
5. **Composable** — the `--format json` output enables integration with editors (VS Code extension), CI pipelines, and pre-commit hooks, expanding gitai's reach beyond the terminal.

## Implementation Estimate

- Phase 1: Core `review` command with non-interactive output — ~300 LOC
- Phase 2: Interactive TUI with results viewer — ~400 LOC
- Phase 3: `suggest` integration (review → commit flow) — ~100 LOC
- Phase 4: JSON output + CI mode — ~100 LOC

Total: ~900 LOC of new code, leveraging ~2000 LOC of existing infrastructure.
