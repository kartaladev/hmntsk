package sqlstore

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/sqlkit"
)

// Insert implements [notify.Store].
//
// It locks the subject's '*' watermark row first, so that every publish and
// close for the subject serialises on it without SELECT ... FOR UPDATE, and then
// decides each notification in the order the memory store does: suppression,
// coalescing, then idempotency.
func (s *Store) Insert(ctx context.Context, subject string, insertions []notify.Insertion) (notify.InsertResult, error) {
	if len(insertions) == 0 {
		return notify.InsertResult{}, nil
	}

	var result notify.InsertResult

	err := s.do(ctx, func(ctx context.Context) error {
		if err := s.ensureWatermark(ctx, subject, allKinds, insertions[0].Notification.CreatedAt); err != nil {
			return err
		}

		var err error

		result, err = s.insert(ctx, subject, insertions)

		return err
	})
	if err != nil {
		return notify.InsertResult{}, err
	}

	return result, nil
}

// ensureWatermark creates a subject's close record for a kind at version -1, or
// locks the existing one without changing it.
func (s *Store) ensureWatermark(ctx context.Context, subject, kind string, at time.Time) error {
	w := sqlkit.NewWriter(s.dialect)
	w.Write("INSERT INTO ", s.watermarksTable(), " (", s.columnList("subject", "kind", "version", "updated_at"), ") VALUES (",
		w.BindAll(subject, kind, int64(-1), sqlkit.EncodeTime(s.dialect, &at)), ")", s.lockSuffix())

	_, err := s.executor.Exec(ctx, w.Done())

	return err
}

// insert decides and writes notifications for a subject whose watermark row
// the transaction already holds.
func (s *Store) insert(ctx context.Context, subject string, insertions []notify.Insertion) (notify.InsertResult, error) {
	floors, err := s.floors(ctx, subject, insertions)
	if err != nil {
		return notify.InsertResult{}, err
	}

	open, err := s.openKinds(ctx, subject, insertions)
	if err != nil {
		return notify.InsertResult{}, err
	}

	sources, err := s.existingSources(ctx, insertions)
	if err != nil {
		return notify.InsertResult{}, err
	}

	var (
		result   notify.InsertResult
		accepted []notify.Notification
	)

	for _, insertion := range insertions {
		n := insertion.Notification
		n.Subject = subject

		if n.SubjectVersion < floorFor(floors, n.Kind) {
			result.Suppressed++

			continue
		}

		kind := pair(n.Recipient, n.Kind)
		if insertion.Coalesce && open[kind] {
			result.Coalesced++

			continue
		}

		source := pair(n.SourceID, n.Recipient)
		if sources[source] {
			result.Duplicates++

			continue
		}

		accepted = append(accepted, n)
		open[kind] = true
		sources[source] = true
	}

	created, err := s.writeNotifications(ctx, accepted)
	if err != nil {
		return notify.InsertResult{}, err
	}

	for _, n := range accepted {
		if created[n.ID] {
			result.Created = append(result.Created, n)
		} else {
			// A concurrent publish on another subject took the same source and
			// recipient between the check and the insert.
			result.Duplicates++
		}
	}

	return result, nil
}

// pair joins two values into one map key.
func pair(a, b string) string { return a + "\x00" + b }

// floorFor is the version below which a notification of a kind is suppressed.
func floorFor(floors map[string]int64, kind string) int64 {
	floor := int64(-1)

	for _, key := range []string{kind, allKinds} {
		if version, ok := floors[key]; ok && version > floor {
			floor = version
		}
	}

	return floor
}

// floors reads a subject's close records for every kind the insertions carry.
func (s *Store) floors(ctx context.Context, subject string, insertions []notify.Insertion) (map[string]int64, error) {
	kinds := []string{allKinds}
	for _, insertion := range insertions {
		if !slices.Contains(kinds, insertion.Notification.Kind) {
			kinds = append(kinds, insertion.Notification.Kind)
		}
	}

	floors := make(map[string]int64, len(kinds))

	for _, chunk := range chunks(kinds, chunkSize) {
		w := sqlkit.NewWriter(s.dialect)
		w.Write("SELECT ", s.columnList("kind", "version"), " FROM ", s.watermarksTable(),
			" WHERE ", s.quote("subject"), " = ", w.Bind(subject),
			" AND ", s.quote("kind"), " IN (", w.BindAll(anys(chunk)...), ")")

		// version is an integer column, so the row is scanned here rather than
		// through queryTexts, which reads every column as text.
		err := s.executor.Query(ctx, w.Done(), func(rows sqlkit.Rows) error {
			for rows.Next() {
				var kind, version any
				if err := rows.Scan(&kind, &version); err != nil {
					return fmt.Errorf("sqlstore: scan a watermark: %w", err)
				}

				var d decoder

				name, floor := d.text(kind), d.integer(version)
				if d.err != nil {
					return d.err
				}

				floors[name] = floor
			}

			return rows.Err()
		})
		if err != nil {
			return nil, err
		}
	}

	return floors, nil
}

// openKinds reads which coalescing insertions' recipients already have an
// ACTIVE or READ notification of their kind on the subject.
func (s *Store) openKinds(ctx context.Context, subject string, insertions []notify.Insertion) (map[string]bool, error) {
	open := make(map[string]bool)

	var coalescing []notify.Insertion

	for _, insertion := range insertions {
		if insertion.Coalesce {
			coalescing = append(coalescing, insertion)
		}
	}

	for _, chunk := range chunks(coalescing, chunkSize) {
		var recipients, kinds []any

		for _, insertion := range chunk {
			recipients = append(recipients, insertion.Notification.Recipient)
			kinds = append(kinds, insertion.Notification.Kind)
		}

		w := sqlkit.NewWriter(s.dialect)
		w.Write("SELECT ", s.columnList("recipient", "kind"), " FROM ", s.notificationsTable(),
			" WHERE ", s.quote("subject"), " = ", w.Bind(subject),
			" AND ", s.quote("state"), " <> ", w.Bind(string(notify.StateClosed)),
			" AND ", s.quote("recipient"), " IN (", w.BindAll(recipients...), ")",
			" AND ", s.quote("kind"), " IN (", w.BindAll(kinds...), ")")

		if err := s.queryTexts(ctx, w.Done(), 2, func(values []string) {
			open[pair(values[0], values[1])] = true
		}); err != nil {
			return nil, err
		}
	}

	return open, nil
}

// existingSources reads which insertions' source and recipient already have a
// notification. Each chunk binds the sources and recipients of its own
// insertions, which covers every insertion's own pair.
func (s *Store) existingSources(ctx context.Context, insertions []notify.Insertion) (map[string]bool, error) {
	existing := make(map[string]bool)

	for _, chunk := range chunks(insertions, chunkSize) {
		var sources, recipients []any

		for _, insertion := range chunk {
			sources = append(sources, insertion.Notification.SourceID)
			recipients = append(recipients, insertion.Notification.Recipient)
		}

		w := sqlkit.NewWriter(s.dialect)
		w.Write("SELECT ", s.columnList("source_id", "recipient"), " FROM ", s.notificationsTable(),
			" WHERE ", s.quote("source_id"), " IN (", w.BindAll(sources...), ")",
			" AND ", s.quote("recipient"), " IN (", w.BindAll(recipients...), ")")

		if err := s.queryTexts(ctx, w.Done(), 2, func(values []string) {
			existing[pair(values[0], values[1])] = true
		}); err != nil {
			return nil, err
		}
	}

	return existing, nil
}

// writeNotifications inserts notifications, skipping any whose source and
// recipient already have one, and reports which identifiers were written.
func (s *Store) writeNotifications(ctx context.Context, notifications []notify.Notification) (map[string]bool, error) {
	written := make(map[string]bool, len(notifications))

	// A row binds one value per column; keep a statement's binds near chunkSize.
	rowsPerStatement := max(1, chunkSize/len(notificationColumns))

	for _, chunk := range chunks(notifications, rowsPerStatement) {
		w := sqlkit.NewWriter(s.dialect)
		w.Write("INSERT INTO ", s.notificationsTable(), " (", s.columnList(notificationColumns...), ") VALUES ")

		for i, n := range chunk {
			values, err := s.encode(n)
			if err != nil {
				return nil, err
			}

			if i > 0 {
				w.Write(", ")
			}

			w.Write("(", w.BindAll(values...), ")")
		}

		w.Write(s.ignoreSuffix())

		if _, err := s.executor.Exec(ctx, w.Done()); err != nil {
			return nil, err
		}
	}

	for _, chunk := range chunks(notifications, chunkSize) {
		ids := make([]any, 0, len(chunk))
		for _, n := range chunk {
			ids = append(ids, n.ID)
		}

		w := sqlkit.NewWriter(s.dialect)
		w.Write("SELECT ", s.quote("id"), " FROM ", s.notificationsTable(),
			" WHERE ", s.quote("id"), " IN (", w.BindAll(ids...), ")")

		if err := s.queryTexts(ctx, w.Done(), 1, func(values []string) {
			written[values[0]] = true
		}); err != nil {
			return nil, err
		}
	}

	return written, nil
}
