package sqlstore

import (
	"context"
	"slices"
	"time"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/sqlkit"
)

// Close implements [notify.Store].
//
// In one transaction, after locking the subject's '*' watermark row: raise the
// watermark for each kind closed, select the recipients the close will close,
// close them, and publish the successor to those recipients through the same
// insert path a publish takes, so that a successor below a newer watermark is
// suppressed like any late publish.
func (s *Store) Close(ctx context.Context, req notify.CloseRequest, at time.Time, ids notify.IDGenerator) (notify.CloseResult, error) {
	at = sqlkit.NormalizeTime(at)

	var result notify.CloseResult

	err := s.do(ctx, func(ctx context.Context) error {
		if err := s.ensureWatermark(ctx, req.Subject, allKinds, at); err != nil {
			return err
		}

		if err := s.raiseWatermarks(ctx, req, at); err != nil {
			return err
		}

		recipients, err := s.closingRecipients(ctx, req)
		if err != nil {
			return err
		}

		closed, err := s.closeNotifications(ctx, req, at)
		if err != nil {
			return err
		}

		result = notify.CloseResult{Closed: closed, Recipients: recipients}

		successors, err := req.SuccessorInsertions(recipients, at, ids)
		if err != nil || len(successors) == 0 {
			return err
		}

		inserted, err := s.insert(ctx, req.Subject, successors)
		if err != nil {
			return err
		}

		result.Successors = inserted.Created
		result.SuccessorsSuppressed = inserted.Suppressed

		return nil
	})
	if err != nil {
		return notify.CloseResult{}, err
	}

	return result, nil
}

// raiseWatermarks raises the close record of each kind closed, or of every kind,
// to the close's version, never lowering it.
func (s *Store) raiseWatermarks(ctx context.Context, req notify.CloseRequest, at time.Time) error {
	kinds := []string{allKinds}
	if len(req.Kinds) > 0 {
		kinds = slices.Compact(slices.Sorted(slices.Values(req.Kinds)))
	}

	for _, kind := range kinds {
		if kind != allKinds {
			if err := s.ensureWatermark(ctx, req.Subject, kind, at); err != nil {
				return err
			}
		}

		version := s.quote("version")

		w := sqlkit.NewWriter(s.dialect)
		w.Write("UPDATE ", s.watermarksTable(), " SET ",
			version, " = CASE WHEN ", version, " < ", w.Bind(req.Version), " THEN ", w.Bind(req.Version),
			" ELSE ", version, " END, ",
			s.quote("updated_at"), " = ", w.Bind(sqlkit.EncodeTime(s.dialect, &at)),
			" WHERE ", s.quote("subject"), " = ", w.Bind(req.Subject),
			" AND ", s.quote("kind"), " = ", w.Bind(kind))

		if _, err := s.executor.Exec(ctx, w.Done()); err != nil {
			return err
		}
	}

	return nil
}

// closingCondition writes the WHERE clause selecting the notifications a close
// closes.
func (s *Store) closingCondition(w *sqlkit.Writer, req notify.CloseRequest) {
	w.Write(" WHERE ", s.quote("subject"), " = ", w.Bind(req.Subject),
		" AND ", s.quote("state"), " <> ", w.Bind(string(notify.StateClosed)),
		" AND ", s.quote("subject_version"), " <= ", w.Bind(req.Version))

	if len(req.Kinds) > 0 {
		w.Write(" AND ", s.quote("kind"), " IN (", w.BindAll(anys(req.Kinds)...), ")")
	}

	if req.Except != "" {
		w.Write(" AND ", s.quote("recipient"), " <> ", w.Bind(req.Except))
	}
}

// closingRecipients reads, sorted, the recipients a close will close.
func (s *Store) closingRecipients(ctx context.Context, req notify.CloseRequest) ([]string, error) {
	w := sqlkit.NewWriter(s.dialect)
	w.Write("SELECT DISTINCT ", s.quote("recipient"), " FROM ", s.notificationsTable())
	s.closingCondition(w, req)
	w.Write(" ORDER BY ", s.quote("recipient"))

	var recipients []string

	err := s.queryTexts(ctx, w.Done(), 1, func(values []string) {
		recipients = append(recipients, values[0])
	})

	return recipients, err
}

// closeNotifications closes a close's notifications and counts them.
func (s *Store) closeNotifications(ctx context.Context, req notify.CloseRequest, at time.Time) (int64, error) {
	instant := sqlkit.EncodeTime(s.dialect, &at)
	inactive := s.quote("inactive_at")

	w := sqlkit.NewWriter(s.dialect)
	w.Write("UPDATE ", s.notificationsTable(), " SET ",
		s.quote("state"), " = ", w.Bind(string(notify.StateClosed)), ", ",
		s.quote("closed_reason"), " = ", w.Bind(nullable(req.Reason)), ", ",
		s.quote("closed_at"), " = ", w.Bind(instant), ", ",
		inactive, " = COALESCE(", inactive, ", ", w.Bind(instant), ")")
	s.closingCondition(w, req)

	return s.executor.Exec(ctx, w.Done())
}
