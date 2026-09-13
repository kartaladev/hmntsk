package sqlstore

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/sqlkit"
)

// Get implements [notify.Store].
func (s *Store) Get(ctx context.Context, recipient, id string) (notify.Notification, error) {
	w := sqlkit.NewWriter(s.dialect)
	w.Write("SELECT ", s.columnList(notificationColumns...), " FROM ", s.notificationsTable(),
		" WHERE ", s.quote("id"), " = ", w.Bind(id),
		" AND ", s.quote("recipient"), " = ", w.Bind(recipient))

	found, err := s.queryNotifications(s.own(ctx), w.Done())
	if err != nil {
		return notify.Notification{}, err
	}

	if len(found) == 0 {
		return notify.Notification{}, notify.ErrNotFound
	}

	return found[0], nil
}

// List implements [notify.Store].
func (s *Store) List(ctx context.Context, q notify.ListQuery) (notify.Page, error) {
	position, continued, err := notify.DecodeCursor(q)
	if err != nil {
		return notify.Page{}, err
	}

	createdAt, id := s.quote("created_at"), s.quote("id")
	limit := q.EffectiveLimit()

	w := sqlkit.NewWriter(s.dialect)
	w.Write("SELECT ", s.columnList(notificationColumns...), " FROM ", s.notificationsTable(),
		" WHERE ", s.quote("recipient"), " = ", w.Bind(q.Recipient))

	if len(q.States) > 0 {
		states := make([]any, 0, len(q.States))
		for _, state := range q.States {
			states = append(states, string(state))
		}

		w.Write(" AND ", s.quote("state"), " IN (", w.BindAll(states...), ")")
	}

	if len(q.Kinds) > 0 {
		w.Write(" AND ", s.quote("kind"), " IN (", w.BindAll(anys(q.Kinds)...), ")")
	}

	if q.Subject != "" {
		w.Write(" AND ", s.quote("subject"), " = ", w.Bind(q.Subject))
	}

	if continued {
		instant := sqlkit.EncodeTime(s.dialect, &position.CreatedAt)
		w.Write(" AND (", createdAt, " < ", w.Bind(instant),
			" OR (", createdAt, " = ", w.Bind(instant), " AND ", id, " < ", w.Bind(position.ID), "))")
	}

	// The limit is an integer the store computed, never caller text, so it is
	// written inline: not every driver binds a LIMIT.
	w.Write(" ORDER BY ", createdAt, " DESC, ", id, " DESC LIMIT ", strconv.Itoa(limit+1))

	found, err := s.queryNotifications(s.own(ctx), w.Done())
	if err != nil {
		return notify.Page{}, err
	}

	page := notify.Page{}

	if len(found) > limit {
		last := found[limit-1]
		page.NextCursor = notify.EncodeCursor(q, notify.CursorPosition{CreatedAt: last.CreatedAt, ID: last.ID})
		found = found[:limit]
	}

	page.Notifications = found

	return page, nil
}

// CountActive implements [notify.Store].
func (s *Store) CountActive(ctx context.Context, recipient string) (int64, error) {
	w := sqlkit.NewWriter(s.dialect)
	w.Write("SELECT COUNT(*) FROM ", s.notificationsTable(),
		" WHERE ", s.quote("recipient"), " = ", w.Bind(recipient),
		" AND ", s.quote("state"), " = ", w.Bind(string(notify.StateActive)))

	return s.queryInt(s.own(ctx), w.Done())
}

// MarkRead implements [notify.Store].
func (s *Store) MarkRead(ctx context.Context, recipient string, ids []string, at time.Time) (notify.MarkResult, error) {
	unique := slices.Compact(slices.Sorted(slices.Values(ids)))
	if len(unique) == 0 {
		return notify.MarkResult{}, nil
	}

	at = sqlkit.NormalizeTime(at)
	instant := sqlkit.EncodeTime(s.dialect, &at)

	var result notify.MarkResult

	err := s.do(ctx, func(ctx context.Context) error {
		for _, chunk := range chunks(unique, chunkSize) {
			w := sqlkit.NewWriter(s.dialect)
			w.Write("SELECT COUNT(*) FROM ", s.notificationsTable(),
				" WHERE ", s.quote("recipient"), " = ", w.Bind(recipient),
				" AND ", s.quote("id"), " IN (", w.BindAll(anys(chunk)...), ")")

			found, err := s.queryInt(ctx, w.Done())
			if err != nil {
				return err
			}

			if found < int64(len(chunk)) {
				return notify.ErrNotFound
			}
		}

		for _, chunk := range chunks(unique, chunkSize) {
			w := sqlkit.NewWriter(s.dialect)
			w.Write("UPDATE ", s.notificationsTable(), " SET ",
				s.quote("state"), " = ", w.Bind(string(notify.StateRead)), ", ",
				s.quote("read_at"), " = ", w.Bind(instant), ", ",
				s.quote("inactive_at"), " = ", w.Bind(instant),
				" WHERE ", s.quote("recipient"), " = ", w.Bind(recipient),
				" AND ", s.quote("state"), " = ", w.Bind(string(notify.StateActive)),
				" AND ", s.quote("id"), " IN (", w.BindAll(anys(chunk)...), ")")

			marked, err := s.executor.Exec(ctx, w.Done())
			if err != nil {
				return err
			}

			result.Marked += marked

			// A closed notification keeps its state and records when it was
			// first read.
			w = sqlkit.NewWriter(s.dialect)
			w.Write("UPDATE ", s.notificationsTable(), " SET ",
				s.quote("read_at"), " = ", w.Bind(instant),
				" WHERE ", s.quote("recipient"), " = ", w.Bind(recipient),
				" AND ", s.quote("state"), " = ", w.Bind(string(notify.StateClosed)),
				" AND ", s.quote("read_at"), " IS NULL",
				" AND ", s.quote("id"), " IN (", w.BindAll(anys(chunk)...), ")")

			if _, err := s.executor.Exec(ctx, w.Done()); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return notify.MarkResult{}, err
	}

	return result, nil
}

// MarkAllRead implements [notify.Store].
func (s *Store) MarkAllRead(ctx context.Context, recipient string, through, at time.Time) (notify.MarkResult, error) {
	at = sqlkit.NormalizeTime(at)
	through = sqlkit.NormalizeTime(through)
	instant := sqlkit.EncodeTime(s.dialect, &at)

	w := sqlkit.NewWriter(s.dialect)
	w.Write("UPDATE ", s.notificationsTable(), " SET ",
		s.quote("state"), " = ", w.Bind(string(notify.StateRead)), ", ",
		s.quote("read_at"), " = ", w.Bind(instant), ", ",
		s.quote("inactive_at"), " = ", w.Bind(instant),
		" WHERE ", s.quote("recipient"), " = ", w.Bind(recipient),
		" AND ", s.quote("state"), " = ", w.Bind(string(notify.StateActive)),
		" AND ", s.quote("created_at"), " <= ", w.Bind(sqlkit.EncodeTime(s.dialect, &through)))

	var marked int64

	err := s.do(ctx, func(ctx context.Context) error {
		var err error

		marked, err = s.executor.Exec(ctx, w.Done())

		return err
	})
	if err != nil {
		return notify.MarkResult{}, err
	}

	return notify.MarkResult{Marked: marked}, nil
}
