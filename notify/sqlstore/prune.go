package sqlstore

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/sqlkit"
)

// defaultPruneBatch is the batch a request that names none gets. The Pruner
// always names one; this only guards a direct caller.
const defaultPruneBatch = 1000

// Prune implements [notify.Store].
//
// Every step selects identifiers with an ordinary SELECT ... LIMIT and then
// deletes by identifier. That is the one shape all three dialects share:
// PostgreSQL has no DELETE ... LIMIT, SQLite has it only behind a compile flag,
// and MySQL refuses a LIMIT subquery on the table it deletes from. Each delete
// is its own statement, so a pass holds no long transaction, and passes on
// several instances at once can only over-delete up to a bound, never below it.
func (s *Store) Prune(ctx context.Context, req notify.PruneRequest) (notify.PruneResult, error) {
	ctx = s.own(ctx)

	batch := req.Batch
	if batch <= 0 {
		batch = defaultPruneBatch
	}

	var result notify.PruneResult

	if req.MaxAge > 0 {
		deleted, err := s.pruneAge(ctx, req.Now.Add(-req.MaxAge), batch)
		result.DeletedForAge = deleted

		if err != nil {
			return result, err
		}
	}

	if req.MaxPerRecipient > 0 {
		if err := s.pruneCount(ctx, req, batch, &result); err != nil {
			return result, err
		}
	}

	if req.WatermarkRetention > 0 {
		deleted, err := s.pruneWatermarks(ctx, req.Now.Add(-req.WatermarkRetention), batch)
		result.WatermarksDeleted = deleted

		if err != nil {
			return result, err
		}
	}

	return result, nil
}

// pruneAge deletes inactive notifications that became inactive before cutoff,
// oldest first. The age bound never touches an ACTIVE notification.
func (s *Store) pruneAge(ctx context.Context, cutoff time.Time, batch int) (int64, error) {
	cutoff = sqlkit.NormalizeTime(cutoff)

	var deleted int64

	for {
		w := sqlkit.NewWriter(s.dialect)
		w.Write("SELECT ", s.quote("id"), " FROM ", s.notificationsTable(),
			" WHERE ", s.quote("state"), " <> ", w.Bind(string(notify.StateActive)),
			" AND ", s.quote("inactive_at"), " < ", w.Bind(sqlkit.EncodeTime(s.dialect, &cutoff)),
			" ORDER BY ", s.quote("inactive_at"), ", ", s.quote("id"),
			" LIMIT ", strconv.Itoa(batch))

		ids, err := s.selectIDs(ctx, w.Done())
		if err != nil {
			return deleted, err
		}

		removed, err := s.deleteIDs(ctx, ids, false)
		deleted += removed

		if err != nil || len(ids) < batch {
			return deleted, err
		}
	}
}

// overBound is a recipient holding more notifications than the count bound.
type overBound struct {
	recipient string
	count     int64
}

// pruneCount brings every recipient within the count bound: their oldest
// inactive notifications first, and then, under [notify.EvictOldestActive]
// only, their oldest ACTIVE ones.
func (s *Store) pruneCount(ctx context.Context, req notify.PruneRequest, batch int, result *notify.PruneResult) error {
	bound := int64(req.MaxPerRecipient)
	after := ""

	for {
		over, err := s.recipientsOver(ctx, bound, after, batch)
		if err != nil {
			return err
		}

		for _, holder := range over {
			excess := holder.count - bound

			deleted, err := s.deleteOldest(ctx, holder.recipient, false, excess, batch)
			result.DeletedForCount += deleted
			excess -= deleted

			if err != nil {
				return err
			}

			if excess <= 0 || req.Strategy != notify.EvictOldestActive {
				continue
			}

			evicted, err := s.deleteOldest(ctx, holder.recipient, true, excess, batch)
			result.EvictedActive += evicted

			if evicted > 0 {
				result.Recipients = append(result.Recipients, holder.recipient)
			}

			if err != nil {
				return err
			}
		}

		if len(over) < batch {
			return nil
		}

		after = over[len(over)-1].recipient
	}
}

// recipientsOver reads, in recipient order after a keyset position, the
// recipients holding more notifications than bound.
func (s *Store) recipientsOver(ctx context.Context, bound int64, after string, batch int) ([]overBound, error) {
	recipient := s.quote("recipient")

	w := sqlkit.NewWriter(s.dialect)
	w.Write("SELECT ", recipient, ", COUNT(*) FROM ", s.notificationsTable(),
		" WHERE ", recipient, " > ", w.Bind(after),
		" GROUP BY ", recipient,
		" HAVING COUNT(*) > ", w.Bind(bound),
		" ORDER BY ", recipient,
		" LIMIT ", strconv.Itoa(batch))

	var over []overBound

	err := s.executor.Query(ctx, w.Done(), func(rows sqlkit.Rows) error {
		for rows.Next() {
			var name, count any
			if err := rows.Scan(&name, &count); err != nil {
				return fmt.Errorf("sqlstore: scan a recipient count: %w", err)
			}

			var d decoder

			holder := overBound{recipient: d.text(name), count: d.integer(count)}
			if d.err != nil {
				return d.err
			}

			over = append(over, holder)
		}

		return rows.Err()
	})

	return over, err
}

// deleteOldest deletes up to n of a recipient's oldest notifications, ACTIVE
// ones or inactive ones, and counts what it deleted.
func (s *Store) deleteOldest(ctx context.Context, recipient string, active bool, n int64, batch int) (int64, error) {
	var deleted int64

	for deleted < n {
		take := min(n-deleted, int64(batch))

		comparison := " <> "
		if active {
			comparison = " = "
		}

		w := sqlkit.NewWriter(s.dialect)
		w.Write("SELECT ", s.quote("id"), " FROM ", s.notificationsTable(),
			" WHERE ", s.quote("recipient"), " = ", w.Bind(recipient),
			" AND ", s.quote("state"), comparison, w.Bind(string(notify.StateActive)),
			" ORDER BY ", s.quote("created_at"), ", ", s.quote("id"),
			" LIMIT ", strconv.FormatInt(take, 10))

		ids, err := s.selectIDs(ctx, w.Done())
		if err != nil {
			return deleted, err
		}

		removed, err := s.deleteIDs(ctx, ids, active)
		deleted += removed

		if err != nil || int64(len(ids)) < take {
			return deleted, err
		}
	}

	return deleted, nil
}

// pruneWatermarks deletes close records last changed before cutoff whose
// subject has no notifications left.
func (s *Store) pruneWatermarks(ctx context.Context, cutoff time.Time, batch int) (int64, error) {
	cutoff = sqlkit.NormalizeTime(cutoff)

	marks := s.watermarksTable()
	subject := marks + "." + s.quote("subject")
	empty := " AND NOT EXISTS (SELECT 1 FROM " + s.notificationsTable() +
		" WHERE " + s.notificationsTable() + "." + s.quote("subject") + " = " + subject + ")"

	var deleted int64

	for {
		w := sqlkit.NewWriter(s.dialect)
		w.Write("SELECT DISTINCT ", subject, " FROM ", marks,
			" WHERE ", s.quote("updated_at"), " < ", w.Bind(sqlkit.EncodeTime(s.dialect, &cutoff)), empty,
			" ORDER BY ", subject,
			" LIMIT ", strconv.Itoa(batch))

		var subjects []string

		if err := s.queryTexts(ctx, w.Done(), 1, func(values []string) {
			subjects = append(subjects, values[0])
		}); err != nil {
			return deleted, err
		}

		for _, chunk := range chunks(subjects, chunkSize) {
			d := sqlkit.NewWriter(s.dialect)
			d.Write("DELETE FROM ", marks,
				" WHERE ", s.quote("subject"), " IN (", d.BindAll(anys(chunk)...), ")",
				" AND ", s.quote("updated_at"), " < ", d.Bind(sqlkit.EncodeTime(s.dialect, &cutoff)), empty)

			removed, err := s.executor.Exec(ctx, d.Done())
			deleted += removed

			if err != nil {
				return deleted, err
			}
		}

		if len(subjects) < batch {
			return deleted, nil
		}
	}
}

// selectIDs runs a statement selecting notification identifiers.
func (s *Store) selectIDs(ctx context.Context, statement sqlkit.Statement) ([]string, error) {
	var ids []string

	err := s.queryTexts(ctx, statement, 1, func(values []string) {
		ids = append(ids, values[0])
	})

	return ids, err
}

// deleteIDs deletes notifications by identifier, re-checking that each is still
// ACTIVE or still inactive, as the selection found it, and counts the rows
// deleted.
func (s *Store) deleteIDs(ctx context.Context, ids []string, active bool) (int64, error) {
	comparison := " <> "
	if active {
		comparison = " = "
	}

	var deleted int64

	for _, chunk := range chunks(ids, chunkSize) {
		w := sqlkit.NewWriter(s.dialect)
		w.Write("DELETE FROM ", s.notificationsTable(),
			" WHERE ", s.quote("id"), " IN (", w.BindAll(anys(chunk)...), ")",
			" AND ", s.quote("state"), comparison, w.Bind(string(notify.StateActive)))

		removed, err := s.executor.Exec(ctx, w.Done())
		deleted += removed

		if err != nil {
			return deleted, err
		}
	}

	return deleted, nil
}
