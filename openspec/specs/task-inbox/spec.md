# task-inbox Specification

## Purpose

Defines how a host builds contextual task lists and buckets from the engine: the orderings an inbox query supports and the exact paging each keeps, counting what a query or a set of buckets matches, and listing a group's queue. All of it works with no transport involved.

## Requirements

### Requirement: Inbox queries order by creation unless the host chooses another supported ordering

The system SHALL order query results by creation when no ordering is requested. It SHALL let the host request one of these supported orderings instead, each ascending or descending:

- **priority**;
- **due date**, with tasks that have no deadline after every task that has one;
- **urgency**: priority, then due date with no-deadline tasks last, then creation.

Ties under every ordering SHALL be broken by creation, so an ordering is total. The system SHALL reject an ordering it does not support, rather than ignoring it or falling back.

#### Scenario: Default ordering is creation order

- **WHEN** a host queries without requesting an ordering
- **THEN** tasks are returned oldest first

#### Scenario: Urgency puts the most urgent work first

- **WHEN** a host requests urgency ordering over tasks of priority 1 due tomorrow, priority 1 with no deadline, priority 0 due next week and priority 5 due today
- **THEN** they are returned as: priority 0 due next week, priority 1 due tomorrow, priority 1 with no deadline, priority 5 due today

#### Scenario: Tasks without a deadline sort last by due date

- **WHEN** a host requests due-date ordering, ascending, over tasks with and without deadlines
- **THEN** every task with a deadline is returned before every task without one

#### Scenario: Descending reverses the chosen ordering

- **WHEN** a host requests priority ordering, descending
- **THEN** the least urgent priority is returned first

#### Scenario: An unsupported ordering is refused

- **WHEN** a host requests an ordering that is not one of the supported orderings
- **THEN** the query fails with a validation error and no results

### Requirement: Paging is exact under every ordering and identical on every store

Under every supported ordering, paging through a query SHALL return each matching task exactly once: no task repeated, and no task skipped because another page was read. It SHALL do so on every supported store, driver and dialect, with identical results for identical data.

A continuation token SHALL be bound to the ordering that produced it. Continuing with a different ordering SHALL fail with a validation error.

#### Scenario: No repeats or gaps across pages

- **WHEN** a host pages with a small page size through tasks that share priorities and deadlines, under each supported ordering
- **THEN** the concatenated pages contain every matching task exactly once, in the ordering's order

#### Scenario: Paging is stable while tasks are created

- **WHEN** a host pages under urgency ordering while new tasks are created
- **THEN** no task already returned on an earlier page is returned again on a later one

#### Scenario: Identical order on every dialect

- **WHEN** the same tasks are stored on PostgreSQL, MySQL and SQLite and queried under due-date ordering
- **THEN** each store returns them in the same order, with no-deadline tasks last on all three

#### Scenario: A token from another ordering is refused

- **WHEN** a host continues a query under priority ordering with a token obtained under creation ordering
- **THEN** the query fails with a validation error

### Requirement: A host can count what a query matches

The system SHALL count the tasks a query matches, applying every filter a query applies, including eligibility resolved for a candidate, and ignoring page size and continuation. The system SHALL also count a set of host-named buckets in one call, returning one count per bucket name.

#### Scenario: A count matches the query

- **WHEN** a host counts a query that matches twelve tasks, with a page size of five
- **THEN** the count is twelve

#### Scenario: Counting several buckets at once

- **WHEN** a host counts buckets named "mine", "available" and "overdue" in one call
- **THEN** it receives one count for each name, each equal to counting that bucket's query alone

#### Scenario: An empty bucket set

- **WHEN** a host counts an empty set of buckets
- **THEN** it receives an empty result without error

### Requirement: A host can list a group's queue

The system SHALL filter a query to tasks whose candidate pool names a given group. The filter SHALL NOT resolve group membership, so a supervisor sees the group's queue as configured, not as one member sees it. It SHALL combine with every other filter.

#### Scenario: A team queue

- **WHEN** a host queries for the group "finance-approvers" with status ready
- **THEN** every ready task whose pool names that group is returned, and no task whose pool does not name it

#### Scenario: A group filter combined with correlation

- **WHEN** a host queries for the group "finance-approvers" and owner type "invoice"
- **THEN** only tasks meeting both conditions are returned
