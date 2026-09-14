package sqlstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/sqlkit"
)

// allKinds is the watermark kind recording a close of every kind, and the row
// that serialises a subject's writes.
const allKinds = "*"

// chunkSize caps the values one IN list or multi-row insert binds, keeping
// every statement well under each dialect's bind parameter limit.
const chunkSize = 100

// notificationColumns are the notifications table's columns, in the order rows
// are written and scanned.
var notificationColumns = []string{
	"id", "recipient", "source_id", "subject", "subject_version", "kind", "state",
	"closed_reason", "title", "links", "data", "created_at", "read_at", "closed_at", "inactive_at",
}

// quote quotes one identifier for the store's dialect.
func (s *Store) quote(identifier string) string { return s.dialect.Quote(identifier) }

// notificationsTable is the notifications table's quoted, prefixed name.
func (s *Store) notificationsTable() string { return s.quote(s.prefix + NotificationsTable) }

// watermarksTable is the watermarks table's quoted, prefixed name.
func (s *Store) watermarksTable() string { return s.quote(s.prefix + WatermarksTable) }

// columnList quotes and joins column names.
func (s *Store) columnList(names ...string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, s.quote(name))
	}

	return strings.Join(quoted, ", ")
}

// lockSuffix turns an insert of a watermark row into one that leaves an
// existing row unchanged but still takes its row lock, held to commit.
//
// sqlkit's UpsertSuffix cannot express this: it assigns the proposed row's
// values, and an assignment from the existing row is spelled differently on
// each dialect.
func (s *Store) lockSuffix() string {
	kind := s.quote("kind")

	switch s.dialect.Name() {
	case sqlkit.MySQL.Name():
		return " ON DUPLICATE KEY UPDATE " + kind + " = " + kind
	case sqlkit.PostgreSQL.Name():
		return " ON CONFLICT (" + s.columnList("subject", "kind") + ") DO UPDATE SET " +
			kind + " = " + s.watermarksTable() + "." + kind
	default:
		return " ON CONFLICT (" + s.columnList("subject", "kind") + ") DO UPDATE SET " + kind + " = " + kind
	}
}

// ignoreSuffix turns an insert of notifications into one that skips a row whose
// source and recipient already have one. MySQL has no DO NOTHING, and INSERT
// IGNORE would also swallow unrelated errors, so it assigns a column to itself.
func (s *Store) ignoreSuffix() string {
	if s.dialect.Name() == sqlkit.MySQL.Name() {
		return " ON DUPLICATE KEY UPDATE " + s.quote("id") + " = " + s.quote("id")
	}

	return " ON CONFLICT (" + s.columnList("source_id", "recipient") + ") DO NOTHING"
}

// ownContext carries a caller's cancellation and deadline but none of its
// values, so that an executor finds no transaction on it to join.
type ownContext struct{ parent context.Context }

// Deadline implements [context.Context].
func (c ownContext) Deadline() (time.Time, bool) { return c.parent.Deadline() }

// Done implements [context.Context].
func (c ownContext) Done() <-chan struct{} { return c.parent.Done() }

// Err implements [context.Context].
func (c ownContext) Err() error { return c.parent.Err() }

// Value implements [context.Context].
func (ownContext) Value(any) any { return nil }

// own returns a context on which the store runs its own transactions. When the
// caller already holds a transaction the executor would join, the store detaches
// from it: notification writes never join a caller's transaction. Values such
// as trace identifiers are lost only in that case.
func (s *Store) own(ctx context.Context) context.Context {
	if s.executor.InTransaction(ctx) {
		return ownContext{parent: ctx}
	}

	return ctx
}

// do runs fn in a transaction of the store's own.
func (s *Store) do(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.executor.Do(s.own(ctx), fn)
}

// chunks splits items into runs of at most size.
func chunks[T any](items []T, size int) [][]T {
	var out [][]T

	for start := 0; start < len(items); start += size {
		out = append(out, items[start:min(start+size, len(items))])
	}

	return out
}

// anys converts values to bind arguments.
func anys[T any](values []T) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}

	return out
}

// nullable binds an empty string as NULL.
func nullable(value string) any {
	if value == "" {
		return nil
	}

	return value
}

// encode renders a notification as bind arguments, in notificationColumns
// order.
func (s *Store) encode(n notify.Notification) ([]any, error) {
	var links any

	if len(n.Links) > 0 {
		encoded, err := json.Marshal(n.Links)
		if err != nil {
			return nil, fmt.Errorf("sqlstore: encode links: %w", err)
		}

		links = string(encoded)
	}

	return []any{
		n.ID, n.Recipient, n.SourceID, n.Subject, n.SubjectVersion, n.Kind, string(n.State),
		nullable(n.ClosedReason), nullable(n.Title), links, sqlkit.EncodeRaw(n.Data),
		sqlkit.EncodeTime(s.dialect, &n.CreatedAt), sqlkit.EncodeTime(s.dialect, n.ReadAt),
		sqlkit.EncodeTime(s.dialect, n.ClosedAt), sqlkit.EncodeTime(s.dialect, n.InactiveAt),
	}, nil
}

// decoder reads column values, keeping the first error.
type decoder struct{ err error }

// text reads a string column.
func (d *decoder) text(value any) string {
	if d.err != nil {
		return ""
	}

	out, err := sqlkit.DecodeString(value)
	d.err = err

	return out
}

// integer reads an integer column.
func (d *decoder) integer(value any) int64 {
	if d.err != nil {
		return 0
	}

	out, err := sqlkit.DecodeInt(value)
	d.err = err

	return out
}

// instant reads an optional timestamp column.
func (d *decoder) instant(value any) *time.Time {
	if d.err != nil {
		return nil
	}

	out, err := sqlkit.DecodeTime(value)
	if err != nil || out.IsZero() {
		d.err = err

		return nil
	}

	return &out
}

// raw reads a JSON column byte for byte.
func (d *decoder) raw(value any) json.RawMessage {
	if d.err != nil {
		return nil
	}

	out, err := sqlkit.DecodeJSON(value)
	d.err = err

	return out
}

// links reads the links column.
func (d *decoder) links(value any) map[string]string {
	encoded := d.raw(value)
	if d.err != nil || len(encoded) == 0 {
		return nil
	}

	var out map[string]string
	if err := json.Unmarshal(encoded, &out); err != nil {
		d.err = fmt.Errorf("sqlstore: decode links: %w", err)
	}

	return out
}

// scanNotification reads one row selected with notificationColumns.
func scanNotification(rows sqlkit.Rows) (notify.Notification, error) {
	values := make([]any, len(notificationColumns))
	dest := make([]any, len(values))

	for i := range values {
		dest[i] = &values[i]
	}

	if err := rows.Scan(dest...); err != nil {
		return notify.Notification{}, fmt.Errorf("sqlstore: scan a notification: %w", err)
	}

	return decodeNotification(values)
}

// decodeNotification reads column values selected with notificationColumns.
func decodeNotification(values []any) (notify.Notification, error) {
	var d decoder

	n := notify.Notification{
		ID:             d.text(values[0]),
		Recipient:      d.text(values[1]),
		SourceID:       d.text(values[2]),
		Subject:        d.text(values[3]),
		SubjectVersion: d.integer(values[4]),
		Kind:           d.text(values[5]),
		State:          notify.State(d.text(values[6])),
		ClosedReason:   d.text(values[7]),
		Title:          d.text(values[8]),
		Links:          d.links(values[9]),
		Data:           d.raw(values[10]),
		ReadAt:         d.instant(values[12]),
		ClosedAt:       d.instant(values[13]),
		InactiveAt:     d.instant(values[14]),
	}

	if created := d.instant(values[11]); created != nil {
		n.CreatedAt = *created
	}

	if d.err != nil {
		return notify.Notification{}, fmt.Errorf("sqlstore: decode a notification: %w", d.err)
	}

	return n, nil
}

// queryNotifications runs a statement selecting notificationColumns.
func (s *Store) queryNotifications(ctx context.Context, statement sqlkit.Statement) ([]notify.Notification, error) {
	var out []notify.Notification

	err := s.executor.Query(ctx, statement, func(rows sqlkit.Rows) error {
		for rows.Next() {
			n, err := scanNotification(rows)
			if err != nil {
				return err
			}

			out = append(out, n)
		}

		return rows.Err()
	})

	return out, err
}

// queryTexts runs a statement selecting string columns, calling visit with each
// row's values.
func (s *Store) queryTexts(ctx context.Context, statement sqlkit.Statement, width int, visit func(values []string)) error {
	return s.executor.Query(ctx, statement, func(rows sqlkit.Rows) error {
		values := make([]any, width)
		dest := make([]any, width)
		texts := make([]string, width)

		for i := range values {
			dest[i] = &values[i]
		}

		for rows.Next() {
			if err := rows.Scan(dest...); err != nil {
				return fmt.Errorf("sqlstore: scan a row: %w", err)
			}

			var d decoder

			for i, value := range values {
				texts[i] = d.text(value)
			}

			if d.err != nil {
				return d.err
			}

			visit(texts)
		}

		return rows.Err()
	})
}

// queryInt runs a statement selecting one integer.
func (s *Store) queryInt(ctx context.Context, statement sqlkit.Statement) (int64, error) {
	var out int64

	err := s.executor.Query(ctx, statement, func(rows sqlkit.Rows) error {
		for rows.Next() {
			var value any
			if err := rows.Scan(&value); err != nil {
				return fmt.Errorf("sqlstore: scan a count: %w", err)
			}

			decoded, err := sqlkit.DecodeInt(value)
			if err != nil {
				return err
			}

			out = decoded
		}

		return rows.Err()
	})

	return out, err
}
