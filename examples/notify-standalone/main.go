// Command notify-standalone uses notify on its own, with no tasks at all.
//
// notify is a generic, durable inbox: it knows nothing about hmntsk. Here the
// invoicing application publishes notifications about its own events (a
// comment on an invoice, a payment reminder, a decision) straight into it. The
// tasknotify adapter that the other scenarios use is built on exactly these
// calls.
//
//	go run ./notify-standalone
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kartaladev/hmntsk/examples/internal/demo"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	"github.com/kartaladev/hmntsk/notify"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "notify-standalone:", err)
		os.Exit(1)
	}
}

// subject is what every notification here is about. Subjects are the
// publisher's own strings; notify only groups and closes by them.
const subject = "invoice/INV-42"

// The publisher's kinds. notify defines none.
const (
	kindComment  = "comment"
	kindReminder = "reminder"
	kindDecision = "decision"
)

func run(ctx context.Context, w io.Writer) error {
	// The clock moves a minute between steps, so "newest first" is stable.
	clock := demo.NewClock(time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC))

	svc, err := notify.New(notify.NewMemoryStore(), notify.WithClock(clock))
	if err != nil {
		return fmt.Errorf("new notifier: %w", err)
	}

	demo.Default(w, "publish once per source, then read")

	if err := publishAndRead(ctx, w, svc, clock); err != nil {
		return err
	}

	demo.Override(w, "a coalescing draft")

	if err := coalescing(ctx, w, svc, clock); err != nil {
		return err
	}

	demo.Override(w, "closing a subject with a successor, and its watermark")

	return closeWithSuccessor(ctx, w, svc, clock)
}

// publishAndRead publishes a comment to each party. A notification is
// identified by its source and recipient, so publishing the same sources again,
// as an at-least-once publisher will, creates nothing.
func publishAndRead(ctx context.Context, w io.Writer, svc *notify.Service, clock *demo.Clock) error {
	comments := []notify.Draft{
		comment("comment-1", invoicing.Alice, "Bob commented on INV-42", 1),
		comment("comment-2", invoicing.Bob, "Alice commented on INV-42", 1),
	}

	for _, label := range []string{"published", "published the same sources again"} {
		result, err := svc.Publish(ctx, comments...)
		if err != nil {
			return fmt.Errorf("publish: %w", err)
		}

		fmt.Fprintf(w, "%s: created %d, duplicates %d\n", label, len(result.Created), result.Duplicates)
	}

	clock.Advance(time.Minute)

	listed, err := printList(ctx, w, svc, invoicing.Alice)
	if err != nil {
		return err
	}

	if err := printActive(ctx, w, svc, invoicing.Alice); err != nil {
		return err
	}

	marked, err := svc.MarkRead(ctx, invoicing.Alice, listed[0].ID)
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}

	fmt.Fprintf(w, "alice marked read: %d\n", marked.Marked)

	// A zero through marks everything up to now. A client passes the instant it
	// loaded its list, so that anything newer stays unread.
	all, err := svc.MarkAllRead(ctx, invoicing.Bob, time.Time{})
	if err != nil {
		return fmt.Errorf("mark all read: %w", err)
	}

	fmt.Fprintf(w, "bob marked all read: %d\n", all.Marked)

	aliceActive, err := svc.CountActive(ctx, invoicing.Alice)
	if err != nil {
		return fmt.Errorf("count: %w", err)
	}

	bobActive, err := svc.CountActive(ctx, invoicing.Bob)
	if err != nil {
		return fmt.Errorf("count: %w", err)
	}

	fmt.Fprintf(w, "alice active: %d, bob active: %d\n", aliceActive, bobActive)

	return nil
}

// coalescing stops a repeated reminder piling up: a coalescing draft creates
// nothing while its recipient already has an open notification of that kind on
// that subject.
func coalescing(ctx context.Context, w io.Writer, svc *notify.Service, clock *demo.Clock) error {
	clock.Advance(time.Minute)

	for i, label := range []string{"reminder for alice", "another reminder for alice, coalescing"} {
		result, err := svc.Publish(ctx, notify.Draft{
			Recipient:      invoicing.Alice,
			SourceID:       fmt.Sprintf("reminder-%d", i+1),
			Subject:        subject,
			Kind:           kindReminder,
			Title:          "INV-42 is due tomorrow",
			SubjectVersion: 1,
			Coalesce:       true,
		})
		if err != nil {
			return fmt.Errorf("publish reminder: %w", err)
		}

		fmt.Fprintf(w, "%s: created %d, coalesced %d\n", label, len(result.Created), result.Coalesced)
	}

	return nil
}

// closeWithSuccessor closes the subject's comments once the invoice is decided,
// spares bob, and tells everyone closed what was decided, in one transaction.
// The close also raises the subject's watermark, so a comment delivered late,
// for an older version, cannot reopen it.
func closeWithSuccessor(ctx context.Context, w io.Writer, svc *notify.Service, clock *demo.Clock) error {
	clock.Advance(time.Minute)

	closed, err := svc.Close(ctx, notify.CloseRequest{
		Subject: subject,
		Kinds:   []string{kindComment},
		Version: 2,
		Reason:  "approved",
		Except:  invoicing.Bob,
		Successor: &notify.Successor{
			SourceID:       "decision-1",
			Kind:           kindDecision,
			Title:          "INV-42 was approved",
			SubjectVersion: 2,
		},
	})
	if err != nil {
		return fmt.Errorf("close: %w", err)
	}

	fmt.Fprintf(w, "closed comments on %s at version 2, sparing bob: closed %d %v, successors %d\n",
		subject, closed.Closed, closed.Recipients, len(closed.Successors))

	clock.Advance(time.Minute)

	for _, recipient := range []string{invoicing.Alice, invoicing.Bob} {
		if _, err := printList(ctx, w, svc, recipient); err != nil {
			return err
		}
	}

	for _, late := range []struct {
		label   string
		source  string
		version int64
	}{
		{"a late comment at version 1 for alice", "comment-late", 1},
		{"a comment at version 3 for alice", "comment-3", 3},
	} {
		result, err := svc.Publish(ctx, comment(late.source, invoicing.Alice, "Carol commented on INV-42", late.version))
		if err != nil {
			return fmt.Errorf("publish: %w", err)
		}

		fmt.Fprintf(w, "%s: created %d, suppressed %d\n", late.label, len(result.Created), result.Suppressed)
	}

	return nil
}

func comment(source, recipient, title string, version int64) notify.Draft {
	return notify.Draft{
		Recipient:      recipient,
		SourceID:       source,
		Subject:        subject,
		Kind:           kindComment,
		Title:          title,
		SubjectVersion: version,
	}
}

// printList prints a recipient's notifications, newest first: an open one with
// its title, a closed one with its reason.
func printList(ctx context.Context, w io.Writer, svc *notify.Service, recipient string) ([]notify.Notification, error) {
	page, err := svc.List(ctx, notify.ListQuery{Recipient: recipient})
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", recipient, err)
	}

	parts := make([]string, 0, len(page.Notifications))

	for _, n := range page.Notifications {
		if n.State == notify.StateClosed {
			parts = append(parts, fmt.Sprintf("%s %s %s", n.Kind, n.State, n.ClosedReason))
		} else {
			parts = append(parts, fmt.Sprintf("%s %s %q", n.Kind, n.State, n.Title))
		}
	}

	fmt.Fprintf(w, "%s: %s\n", recipient, strings.Join(parts, ", "))

	return page.Notifications, nil
}

func printActive(ctx context.Context, w io.Writer, svc *notify.Service, recipient string) error {
	active, err := svc.CountActive(ctx, recipient)
	if err != nil {
		return fmt.Errorf("count %s: %w", recipient, err)
	}

	fmt.Fprintf(w, "%s active: %d\n", recipient, active)

	return nil
}
