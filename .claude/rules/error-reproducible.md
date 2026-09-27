# Rule: A defect claim needs a failing test

**Every** claim that code in this repository is wrong must be backed by a test that fails because of
that defect. If there is no failing test, it is not a finding.

This applies wherever a defect is claimed: audits, code reviews, `/code-review` findings, bug
reports, issue and PR descriptions, commit messages that say "fix", and answers to "is this
broken?". It also applies to findings from subagents. Their claims are held to the same bar before
they are passed on.

## What counts as proof

1. **The test is written and run, and it fails.** Showing the failure output is part of the claim.
   A test that has only been described or sketched is not proof.
2. **It fails for the reason claimed.** The assertion encodes the correct behaviour, so the failure
   message states the defect. For example: `expected ErrConflict, got unique_violation`. A failure
   caused by a compile error, a missing fixture, a timeout or a panic somewhere else does not count.
3. **It uses the public surface the defect is reachable from.** Examples: the port, the HTTP route,
   or the store adapter. It does not reach into internals to build a state that production can never
   produce. If the triggering state is only reachable through a race or a crash window, the test
   drives it deterministically, using an injected clock, a hook, a blocking fake sink or two
   explicit transactions. It does not rely on sleeps.
4. **It is deterministic.** A race-dependent reproduction must fail on every run of
   `go test -count=20`, or under `-race` when the defect is itself a data race. A test that fails
   only sometimes is recorded as a flaky reproduction, and the claim is not confirmed.
5. **It follows the project's test rules.** That means `table-test` form when there are several
   cases, `use-mockgen` doubles and `use-testcontainers` for real services. A defect that exists
   only on one dialect or driver is reproduced against that dialect or driver, never against
   memstore.

## Where the test lives

- **When only reporting (audits, reviews, read-only tasks)**, write the test outside the repo, in
  the session scratchpad, as a module that points at the repo through `replace` directives or a
  scratch `go.work`. Name the file and the exact command in the report, so the reader can re-run it.
- **When fixing**, the reproduction becomes the **red** step of `golang-tdd.md`. Move it into the
  repo next to the code it covers, confirm it still fails, then make it pass. The fix and its test
  land together.

## How to report

Each finding lists:

- the file and line of the defect
- the test that reproduces it, with its path and name
- the command that runs it
- the relevant lines of the failure output

A finding whose reproduction was not run, or did not fail, is either dropped or reported under a
separate **Unconfirmed** heading. Such a finding says why no test was possible, and it is never
ranked alongside confirmed findings.

## What falls outside this rule

These are reported as **observations**, not defects, and they are labelled as such:

- documentation or godoc that disagrees with the code
- a design-rule violation that causes no wrong behaviour, such as a default that cannot be replaced
  or a missing option
- a style or maintainability concern

As soon as an observation is claimed to cause wrong behaviour, it becomes a defect claim and needs a
failing test.
