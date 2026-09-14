package sqlstore

import (
	"context"
	"embed"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/sqlkit"
)

// EmailDeliveriesTable holds email delivery state, one row per notification,
// before any prefix. It exists only where a host applied the email schema.
const EmailDeliveriesTable = "notify_email_deliveries"

// emailDocuments holds the published email DDL, one document per dialect.
//
//go:embed ddl/email/*.sql
var emailDocuments embed.FS

// emailDocumentFiles names each dialect's email DDL document.
var emailDocumentFiles = map[string]string{
	sqlkit.PostgreSQL.Name(): "ddl/email/postgres.sql",
	sqlkit.MySQL.Name():      "ddl/email/mysql.sql",
	sqlkit.SQLite.Name():     "ddl/email/sqlite.sql",
}

var _ notify.EmailStore = (*Store)(nil)

// emailSchemaExpectation is what [Store.VerifyEmailSchema] requires, before the
// table prefix.
var emailSchemaExpectation = sqlkit.SchemaExpectation{
	EmailDeliveriesTable: {
		Columns: []string{
			"notification_id", "recipient", "status", "batch_id", "owner", "lease_until", "attempts",
			"next_attempt_at", "reason", "sent_at", "updated_at",
		},
		IdentifierColumns: []string{"notification_id", "recipient", "status", "batch_id", "owner"},
		Indexes:           []string{"notify_email_deliveries_lease_idx", "notify_email_deliveries_retry_idx"},
	},
}

// emailDocument reads the email DDL document for the store's dialect. New only
// builds stores in dialects both documents are published for.
func (s *Store) emailDocument() string {
	raw, err := emailDocuments.ReadFile(emailDocumentFiles[s.dialect.Name()])
	if err != nil {
		return ""
	}

	return string(raw)
}

// EmailTables returns the email delivery tables, prefixed.
func (s *Store) EmailTables() []string { return []string{s.prefix + EmailDeliveriesTable} }

// EmailSchema returns the email delivery DDL for the store's dialect, with the
// table prefix applied. A host that emails notifications applies it through its
// own migration pipeline, in addition to [Store.Schema]; one that does not never
// needs it.
func (s *Store) EmailSchema() string {
	return strings.ReplaceAll(s.emailDocument(), sqlkit.PrefixToken, s.prefix)
}

// MigrateEmail applies the email delivery schema. Like [Store.Migrate] it exists
// for tests and development.
func (s *Store) MigrateEmail(ctx context.Context) error {
	return sqlkit.ApplySchema(ctx, s.execer, s.dialect, sqlkit.RenderSchema(s.emailDocument(), s.prefix))
}

// VerifyEmailSchema compares the live database with what email delivery
// requires, reporting every discrepancy at once as a [*sqlkit.SchemaError]. A
// host that emails calls it at startup, alongside [Store.VerifySchema], which
// does not require the email table.
func (s *Store) VerifyEmailSchema(ctx context.Context) error {
	return sqlkit.VerifySchema(s.own(ctx), s.querier, s.dialect, s.prefix, emailSchemaExpectation)
}

// emailTable is the email deliveries table's quoted, prefixed name.
func (s *Store) emailTable() string { return s.quote(s.prefix + EmailDeliveriesTable) }

// dueEmail is a notification a claim selected, and whether it already has a
// delivery record.
type dueEmail struct {
	id       string
	recorded bool
}

// ClaimEmails implements [notify.EmailStore].
//
// It follows the escalation sweep's lease pattern, with no row locking: select
// the due notifications, insert delivery records for the unrecorded ones while
// ignoring a record a concurrent claim wrote first, take over lapsed records
// with an update that repeats the selection's condition, and read back only the
// records this claim's owner and lease now hold.
func (s *Store) ClaimEmails(ctx context.Context, claim notify.EmailClaim) ([]notify.EmailCandidate, error) {
	if claim.Limit <= 0 {
		return nil, nil
	}

	now := sqlkit.NormalizeTime(claim.Now)
	until := sqlkit.NormalizeTime(now.Add(claim.Lease))

	var claimed []notify.EmailCandidate

	err := s.do(ctx, func(ctx context.Context) error {
		due, err := s.dueEmails(ctx, claim, now)
		if err != nil || len(due) == 0 {
			return err
		}

		var fresh, held, ids []string

		for _, d := range due {
			ids = append(ids, d.id)

			if d.recorded {
				held = append(held, d.id)
			} else {
				fresh = append(fresh, d.id)
			}
		}

		slices.Sort(fresh)
		slices.Sort(held)

		if err := s.insertDeliveries(ctx, fresh, claim.Owner, now, until); err != nil {
			return err
		}

		if err := s.takeOverDeliveries(ctx, held, claim.Owner, now, until); err != nil {
			return err
		}

		claimed, err = s.claimedEmails(ctx, claim.Owner, until, ids)

		return err
	})
	if err != nil {
		return nil, err
	}

	return claimed, nil
}

// statusList renders email statuses as bind arguments.
func statusList(statuses ...notify.EmailStatus) []any {
	out := make([]any, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, string(status))
	}

	return out
}

// writeLapsed writes the condition under which an existing delivery record may
// be claimed: an unleased or lapsed CLAIMED or RETRY record that is due, or a
// lapsed SENDING record. column qualifies a column name.
func (s *Store) writeLapsed(w *sqlkit.Writer, column func(string) string, now any) {
	w.Write("((", column("status"), " IN (", w.BindAll(statusList(notify.EmailStatusClaimed, notify.EmailStatusRetry)...), ")",
		" AND (", column("lease_until"), " IS NULL OR ", column("lease_until"), " <= ", w.Bind(now), ")",
		" AND (", column("next_attempt_at"), " IS NULL OR ", column("next_attempt_at"), " <= ", w.Bind(now), "))",
		" OR (", column("status"), " = ", w.Bind(string(notify.EmailStatusSending)),
		" AND (", column("lease_until"), " IS NULL OR ", column("lease_until"), " <= ", w.Bind(now), ")))")
}

// dueEmails selects, oldest first, up to the claim's limit of notifications due
// for email.
func (s *Store) dueEmails(ctx context.Context, claim notify.EmailClaim, now time.Time) ([]dueEmail, error) {
	n := func(name string) string { return "n." + s.quote(name) }
	d := func(name string) string { return "d." + s.quote(name) }

	instant := sqlkit.EncodeTime(s.dialect, &now)
	createdUntil := sqlkit.NormalizeTime(claim.CreatedUntil)
	createdFrom := sqlkit.NormalizeTime(claim.CreatedFrom)

	qualifies := func(w *sqlkit.Writer) {
		w.Write(n("state"), " = ", w.Bind(string(notify.StateActive)),
			" AND ", n("created_at"), " <= ", w.Bind(sqlkit.EncodeTime(s.dialect, &createdUntil)),
			" AND ", n("created_at"), " >= ", w.Bind(sqlkit.EncodeTime(s.dialect, &createdFrom)))
	}

	w := sqlkit.NewWriter(s.dialect)
	w.Write("SELECT ", n("id"), ", COALESCE(", d("notification_id"), ", '')",
		" FROM ", s.notificationsTable(), " n LEFT JOIN ", s.emailTable(), " d ON ", d("notification_id"), " = ", n("id"),
		" WHERE (", d("notification_id"), " IS NULL AND ")
	qualifies(w)
	w.Write(") OR (", d("status"), " = ", w.Bind(string(notify.EmailStatusSending)), " AND ")
	s.writeLapsed(w, d, instant)
	w.Write(") OR (", d("status"), " <> ", w.Bind(string(notify.EmailStatusSending)), " AND ")
	s.writeLapsed(w, d, instant)
	w.Write(" AND ")
	qualifies(w)
	w.Write(") ORDER BY ", n("created_at"), ", ", n("id"), " LIMIT ", strconv.Itoa(claim.Limit))

	var due []dueEmail

	err := s.queryTexts(ctx, w.Done(), 2, func(values []string) {
		due = append(due, dueEmail{id: values[0], recorded: values[1] != ""})
	})

	return due, err
}

// insertDeliveries writes CLAIMED delivery records leased to owner for
// notifications that had none, leaving any record a concurrent claim wrote
// first untouched.
func (s *Store) insertDeliveries(ctx context.Context, ids []string, owner string, now, until time.Time) error {
	columns := []string{"notification_id", "recipient", "status", "owner", "lease_until", "attempts", "updated_at"}

	for _, chunk := range chunks(ids, chunkSize) {
		w := sqlkit.NewWriter(s.dialect)
		w.Write("INSERT INTO ", s.emailTable(), " (", s.columnList(columns...), ") SELECT ",
			s.quote("id"), ", ", s.quote("recipient"), ", ", w.Bind(string(notify.EmailStatusClaimed)), ", ",
			w.Bind(owner), ", ", w.Bind(sqlkit.EncodeTime(s.dialect, &until)), ", 0, ",
			w.Bind(sqlkit.EncodeTime(s.dialect, &now)),
			" FROM ", s.notificationsTable(), " WHERE ", s.quote("id"), " IN (", w.BindAll(anys(chunk)...), ")")

		if s.dialect.Name() == sqlkit.MySQL.Name() {
			w.Write(" ON DUPLICATE KEY UPDATE ", s.quote("notification_id"), " = ", s.emailTable(), ".", s.quote("notification_id"))
		} else {
			// SQLite needs a WHERE before ON CONFLICT on an INSERT ... SELECT, or
			// it reads ON as a join constraint.
			w.Write(" AND 1 = 1 ON CONFLICT (", s.quote("notification_id"), ") DO NOTHING")
		}

		if _, err := s.executor.Exec(ctx, w.Done()); err != nil {
			return err
		}
	}

	return nil
}

// takeOverDeliveries leases lapsed delivery records to owner, keeping a SENDING
// record's status so that its send stays in doubt, and setting CLAIMED
// otherwise. A record another claim took in the meantime no longer matches the
// condition and is left alone.
func (s *Store) takeOverDeliveries(ctx context.Context, ids []string, owner string, now, until time.Time) error {
	status := s.quote("status")
	plain := func(name string) string { return s.quote(name) }

	for _, chunk := range chunks(ids, chunkSize) {
		w := sqlkit.NewWriter(s.dialect)
		w.Write("UPDATE ", s.emailTable(), " SET ",
			status, " = CASE WHEN ", status, " = ", w.Bind(string(notify.EmailStatusSending)),
			" THEN ", status, " ELSE ", w.Bind(string(notify.EmailStatusClaimed)), " END, ",
			s.quote("owner"), " = ", w.Bind(owner), ", ",
			s.quote("lease_until"), " = ", w.Bind(sqlkit.EncodeTime(s.dialect, &until)), ", ",
			s.quote("updated_at"), " = ", w.Bind(sqlkit.EncodeTime(s.dialect, &now)),
			" WHERE ", s.quote("notification_id"), " IN (", w.BindAll(anys(chunk)...), ") AND ")
		s.writeLapsed(w, plain, sqlkit.EncodeTime(s.dialect, &now))

		if _, err := s.executor.Exec(ctx, w.Done()); err != nil {
			return err
		}
	}

	return nil
}

// claimedEmails reads back the notifications among ids whose delivery records
// owner now holds under this claim's lease, oldest first.
func (s *Store) claimedEmails(ctx context.Context, owner string, until time.Time, ids []string) ([]notify.EmailCandidate, error) {
	var out []notify.EmailCandidate

	columns := make([]string, 0, len(notificationColumns)+3)
	for _, column := range notificationColumns {
		columns = append(columns, "n."+s.quote(column))
	}

	columns = append(columns, "d."+s.quote("status"), "COALESCE(d."+s.quote("batch_id")+", '')", "d."+s.quote("attempts"))

	for _, chunk := range chunks(ids, chunkSize) {
		w := sqlkit.NewWriter(s.dialect)
		w.Write("SELECT ", strings.Join(columns, ", "),
			" FROM ", s.emailTable(), " d JOIN ", s.notificationsTable(), " n ON n.", s.quote("id"), " = d.", s.quote("notification_id"),
			" WHERE d.", s.quote("owner"), " = ", w.Bind(owner),
			" AND d.", s.quote("lease_until"), " = ", w.Bind(sqlkit.EncodeTime(s.dialect, &until)),
			" AND d.", s.quote("notification_id"), " IN (", w.BindAll(anys(chunk)...), ")")

		err := s.executor.Query(ctx, w.Done(), func(rows sqlkit.Rows) error {
			for rows.Next() {
				values := make([]any, len(columns))
				dest := make([]any, len(values))

				for i := range values {
					dest[i] = &values[i]
				}

				if err := rows.Scan(dest...); err != nil {
					return fmt.Errorf("sqlstore: scan a claimed email: %w", err)
				}

				n, err := decodeNotification(values[:len(notificationColumns)])
				if err != nil {
					return err
				}

				var dec decoder

				rest := values[len(notificationColumns):]
				candidate := notify.EmailCandidate{
					Notification: n,
					Status:       notify.EmailStatus(dec.text(rest[0])),
					BatchID:      dec.text(rest[1]),
					Attempts:     int(dec.integer(rest[2])),
				}

				if dec.err != nil {
					return fmt.Errorf("sqlstore: decode a claimed email: %w", dec.err)
				}

				out = append(out, candidate)
			}

			return rows.Err()
		})
		if err != nil {
			return nil, err
		}
	}

	slices.SortFunc(out, func(a, b notify.EmailCandidate) int {
		if c := a.Notification.CreatedAt.Compare(b.Notification.CreatedAt); c != 0 {
			return c
		}

		return strings.Compare(a.Notification.ID, b.Notification.ID)
	})

	return out, nil
}

// RecordEmails implements [notify.EmailStore]. It changes only the records
// record.Owner still holds.
func (s *Store) RecordEmails(ctx context.Context, record notify.EmailRecord) (int64, error) {
	ids := slices.Compact(slices.Sorted(slices.Values(record.IDs)))
	if len(ids) == 0 {
		return 0, nil
	}

	at := sqlkit.NormalizeTime(record.At)
	instant := sqlkit.EncodeTime(s.dialect, &at)

	var changed int64

	err := s.do(ctx, func(ctx context.Context) error {
		for _, chunk := range chunks(ids, chunkSize) {
			w := sqlkit.NewWriter(s.dialect)
			w.Write("UPDATE ", s.emailTable(), " SET ",
				s.quote("status"), " = ", w.Bind(string(record.Status)), ", ",
				s.quote("updated_at"), " = ", w.Bind(instant))

			var next any

			if record.Status == notify.EmailStatusRetry && record.NextAttemptAt != nil {
				due := sqlkit.NormalizeTime(*record.NextAttemptAt)
				next = sqlkit.EncodeTime(s.dialect, &due)
			}

			w.Write(", ", s.quote("next_attempt_at"), " = ", w.Bind(next))

			if record.BatchID != "" {
				w.Write(", ", s.quote("batch_id"), " = ", w.Bind(record.BatchID))
			}

			if record.Reason != "" {
				w.Write(", ", s.quote("reason"), " = ", w.Bind(record.Reason))
			}

			if record.Attempt {
				w.Write(", ", s.quote("attempts"), " = ", s.quote("attempts"), " + 1")
			}

			if record.Status == notify.EmailStatusSent {
				w.Write(", ", s.quote("sent_at"), " = ", w.Bind(instant))
			}

			if record.Status != notify.EmailStatusSending {
				w.Write(", ", s.quote("owner"), " = NULL, ", s.quote("lease_until"), " = NULL")
			}

			w.Write(" WHERE ", s.quote("owner"), " = ", w.Bind(record.Owner),
				" AND ", s.quote("notification_id"), " IN (", w.BindAll(anys(chunk)...), ")")

			n, err := s.executor.Exec(ctx, w.Done())
			if err != nil {
				return err
			}

			changed += n
		}

		return nil
	})
	if err != nil {
		return 0, err
	}

	return changed, nil
}

// PurgeEmailRecords implements [notify.EmailStore]. It selects orphaned records
// and then deletes them by identifier, re-checking that each is still orphaned.
func (s *Store) PurgeEmailRecords(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}

	ctx = s.own(ctx)

	orphaned := func(table string) string {
		return " NOT EXISTS (SELECT 1 FROM " + s.notificationsTable() + " n WHERE n." + s.quote("id") +
			" = " + table + "." + s.quote("notification_id") + ")"
	}

	w := sqlkit.NewWriter(s.dialect)
	w.Write("SELECT d.", s.quote("notification_id"), " FROM ", s.emailTable(), " d WHERE", orphaned("d"),
		" ORDER BY d.", s.quote("notification_id"), " LIMIT ", strconv.Itoa(limit))

	var ids []string

	if err := s.queryTexts(ctx, w.Done(), 1, func(values []string) {
		ids = append(ids, values[0])
	}); err != nil {
		return 0, err
	}

	var deleted int64

	for _, chunk := range chunks(ids, chunkSize) {
		d := sqlkit.NewWriter(s.dialect)
		d.Write("DELETE FROM ", s.emailTable(),
			" WHERE ", s.quote("notification_id"), " IN (", d.BindAll(anys(chunk)...), ") AND", orphaned(s.emailTable()))

		removed, err := s.executor.Exec(ctx, d.Done())
		deleted += removed

		if err != nil {
			return deleted, err
		}
	}

	return deleted, nil
}
