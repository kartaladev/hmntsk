## REMOVED Requirements

### Requirement: A browser demo shows contextual tasks inside an application

**Reason**: The demo's order no longer starts an invoice review on an invoice page. It is approved first, issued as a sent or uploaded purchase order, and invoiced after a delay, on an order page. Its scenarios, "Placing an order starts a review" among them, no longer describe what the demo does.

**Migration**: Replaced by "A browser demo shows an order's purchasing workflow as contextual tasks" below, which keeps every scenario whose behaviour still holds (signing in, the sign-in redirect, only purchasing places orders, the matching and disputed reviews, completing a task from the form, the live notification) and adds the approval, purchase order and invoice scenarios. Links to `/invoices/{id}` become `/orders/{id}`.

## ADDED Requirements

### Requirement: A browser demo shows an order's purchasing workflow as contextual tasks

The examples SHALL include one browser demo, `contextual-ui`, served by a Go program, that embeds tasks in the pages of a small purchasing application rather than in a stand-alone inbox. The demo SHALL declare its own purchasing domain rather than reuse the shared invoicing domain. It SHALL show:

- a sign-in page that lists the demo users, and a signed-in user's avatar in the top-right corner of every page with a menu to sign out;
- an inbox page with buckets and their counts, ordered by urgency, with no user switcher on it;
- an orders page where a purchasing user places an order, which creates the order and its approval task;
- an order page, reached through the task types' route, that shows the order, its purchase order documents, its invoice once it has arrived, every step of its workflow and every task on it, and the form named by the viewer's task type through which the task is claimed, progressed and completed;
- a notification count that updates without a page reload when a notification is published for the viewer;
- every table as a data grid. A grid over a server-ordered, cursor-paged task list SHALL page on the server and SHALL NOT offer sorting or filtering of a single page in the browser.

An order SHALL move through approval, purchase order, invoice, invoice review and invoice approval:

- approving an order SHALL create its purchase order task, after the approval's completion is committed, and declining it SHALL end the workflow;
- the purchase order task SHALL be issued by sending the purchase order when the order's supplier is on the demo's supplier registry, and by uploading a purchase order document otherwise;
- issuing the purchase order SHALL store the document and complete the task together, or do neither;
- the supplier's invoice SHALL arrive, with its review task, a delay after the purchase order was issued;
- completing a review that finds the invoice matches its order SHALL create the invoice's approval task, after the review's completion is committed, and the invoice's approval SHALL decide whether the order is approved or rejected.

Every workflow step SHALL be safe to repeat. The demo SHALL state on the sign-in page that its identity mechanism is for demonstration only, and SHALL be labelled as an illustration and not as a reusable UI component.

#### Scenario: Signing in shows the user's own inbox

- **WHEN** a viewer signs in as one demo user, signs out, and signs in as another
- **THEN** the buckets and counts shown are those of the signed-in user, and the avatar names that user

#### Scenario: An unsigned viewer is sent to sign in

- **WHEN** a viewer with no demo session opens any page
- **THEN** the sign-in page is shown, and after signing in the viewer returns to the page they opened

#### Scenario: Placing an order asks for its approval

- **WHEN** a purchasing user places an order with a supplier, description and amount
- **THEN** the order and its approval task exist, no invoice exists, and the budget holder's notification count increases without a reload

#### Scenario: Only purchasing places orders

- **WHEN** a signed-in user outside purchasing places an order
- **THEN** the order is refused and nothing is created

#### Scenario: A declined order goes no further

- **WHEN** the budget holder completes an order's approval declining it
- **THEN** no purchase order task is created and the order is shown as declined

#### Scenario: A registered supplier's purchase order is sent

- **WHEN** an order with a supplier on the registry is approved, and purchasing sends its purchase order from the order page
- **THEN** a purchase order document addressed to the supplier's registry contact is stored, the purchase order task is completed, and the order waits for its invoice

#### Scenario: An unregistered supplier's purchase order is uploaded

- **WHEN** an order with a supplier not on the registry is approved, and purchasing uploads a PDF purchase order from the order page
- **THEN** the uploaded document is stored and can be downloaded, the purchase order task is completed, and the order waits for its invoice

#### Scenario: A purchase order issued the wrong way is refused

- **WHEN** purchasing sends the purchase order of an order whose supplier is not on the registry, uploads one for a registered supplier, or uploads a file that is not a PDF, PNG or JPEG or is larger than 5 MB
- **THEN** the request is refused, no document is stored and the task is not completed

#### Scenario: The invoice arrives after a delay

- **WHEN** an order's purchase order has been issued and the invoice delay has passed
- **THEN** the order's invoice and its review task exist, and receiving invoices again creates neither a second time

#### Scenario: A matching review leads to approval

- **WHEN** an approver completes an invoice's review from the order page stating that it matches its order
- **THEN** the invoice's approval task is created, and the order page shows the review completed and the approval waiting

#### Scenario: A disputed review ends the workflow

- **WHEN** an approver completes an invoice's review stating that it does not match its order
- **THEN** no approval task is created and the order is shown as disputed

#### Scenario: A repeated workflow step changes nothing

- **WHEN** the completion of any workflow task is delivered to the workflow more than once
- **THEN** the order's next task exists once and the order is not moved back

#### Scenario: Completing a task from the form

- **WHEN** a viewer claims a task on the order page, fills in the rendered form with valid output and submits it
- **THEN** the task is completed and leaves the viewer's active buckets, and the counts update

#### Scenario: A live notification

- **WHEN** a task that the viewing user may act on is created while the page is open
- **THEN** the notification count increases without the viewer reloading the page
