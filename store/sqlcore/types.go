package sqlcore

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kartaladev/hmntsk"
)

// UpsertType renders the write that publishes a registered task type to the
// database.
//
// The registry the engine validates against lives in memory; this table is how
// a host that is not written in Go, and an inbox rendering a form from JSON
// Schema, get at the same information. Writing it is therefore optional, and
// nothing in the engine reads it back.
func (b *Builder) UpsertType(spec hmntsk.TypeSpec, now time.Time) Statement {
	s := b.begin()

	updatedAt := hmntsk.NormalizeTime(now)

	args := []any{
		spec.Name,
		nullString(spec.Title),
		nullString(spec.Description),
		b.encodeRaw(spec.InputSchema),
		b.encodeRaw(spec.OutputSchema),
		int64(spec.DefaultPriority),
		spec.DefaultDeadline.Milliseconds(),
		b.encodeValue(spec.DefaultEscalation),
		b.encodeValue(spec.DefaultAssignment),
		b.encodeTime(&updatedAt),
	}

	s.write("INSERT INTO ", b.Table(TypesTable), " (", b.quoteList("", typeColumns), ") VALUES (")
	s.write(s.bindAll(args...))
	s.write(")")
	s.write(b.dialect.UpsertSuffix([]string{"name"}, typeColumns[1:]))

	return s.done()
}

// SelectType renders the read of one registered type.
func (b *Builder) SelectType(name string) Statement {
	s := b.begin()

	s.write("SELECT ", b.quoteList("", typeColumns), " FROM ", b.Table(TypesTable))
	s.write(" WHERE ", b.dialect.Quote("name"), " = ", s.bind(name))

	return s.done()
}

// SelectTypes renders the read of every registered type, by name.
func (b *Builder) SelectTypes() Statement {
	s := b.begin()

	s.write("SELECT ", b.quoteList("", typeColumns), " FROM ", b.Table(TypesTable))
	s.write(" ORDER BY ", b.dialect.Quote("name"))

	return s.done()
}

// DeleteType renders the removal of a registered type.
func (b *Builder) DeleteType(name string) Statement {
	s := b.begin()

	s.write("DELETE FROM ", b.Table(TypesTable))
	s.write(" WHERE ", b.dialect.Quote("name"), " = ", s.bind(name))

	return s.done()
}

// ScanTypes reads registered task types.
func ScanTypes(rows Rows) ([]hmntsk.TypeSpec, error) {
	var specs []hmntsk.TypeSpec

	values := make([]any, len(typeColumns))
	dest := make([]any, len(typeColumns))

	for i := range values {
		dest[i] = &values[i]
	}

	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("sqlcore: scan task type row: %w", err)
		}

		spec, err := typeSpec(values)
		if err != nil {
			return nil, err
		}

		specs = append(specs, spec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlcore: read task type rows: %w", err)
	}

	return specs, nil
}

// typeSpec converts one scanned task type row.
func typeSpec(values []any) (hmntsk.TypeSpec, error) {
	var (
		spec hmntsk.TypeSpec
		err  error
	)

	readString := func(index int, target *string) {
		if err != nil {
			return
		}

		*target, err = DecodeString(values[index])
	}

	readRaw := func(index int, target *json.RawMessage) {
		if err != nil {
			return
		}

		*target, err = DecodeJSON(values[index])
	}

	readString(0, &spec.Name)
	readString(1, &spec.Title)
	readString(2, &spec.Description)
	readRaw(3, &spec.InputSchema)
	readRaw(4, &spec.OutputSchema)

	if err != nil {
		return hmntsk.TypeSpec{}, err
	}

	priority, err := DecodeInt(values[5])
	if err != nil {
		return hmntsk.TypeSpec{}, err
	}

	spec.DefaultPriority = hmntsk.Priority(priority)

	deadline, err := DecodeInt(values[6])
	if err != nil {
		return hmntsk.TypeSpec{}, err
	}

	spec.DefaultDeadline = time.Duration(deadline) * time.Millisecond

	escalation, err := DecodeJSON(values[7])
	if err != nil {
		return hmntsk.TypeSpec{}, err
	}

	if len(escalation) > 0 {
		policy := &hmntsk.EscalationPolicy{}
		if unmarshalErr := json.Unmarshal(escalation, policy); unmarshalErr != nil {
			return hmntsk.TypeSpec{}, fmt.Errorf("sqlcore: column default_escalation: %w", unmarshalErr)
		}

		spec.DefaultEscalation = policy
	}

	assignment, err := DecodeJSON(values[8])
	if err != nil {
		return hmntsk.TypeSpec{}, err
	}

	if len(assignment) > 0 {
		if unmarshalErr := json.Unmarshal(assignment, &spec.DefaultAssignment); unmarshalErr != nil {
			return hmntsk.TypeSpec{}, fmt.Errorf("sqlcore: column default_assignment: %w", unmarshalErr)
		}
	}

	return spec, nil
}

// ConflictError builds the error an adapter returns when a conditional update
// matched no row.
//
// Every mutation in this package is conditional on the version the caller read,
// and no dialect here can report what it did from the write itself — MySQL has
// no RETURNING — so a rows-affected count of zero is the whole signal. The
// adapter re-reads the current version so the loser is told what to re-read.
func ConflictError(id hmntsk.TaskID, expected, current int64) error {
	return &hmntsk.ConflictError{TaskID: id, Expected: expected, Current: current}
}

// CheckAffected turns a conditional update's rows-affected count into the
// engine's conflict semantics.
//
// It is the whole of what an adapter has to do after a write, and it is here
// rather than in each adapter so that all of them agree. Zero rows means the
// version predicate did not match: either somebody else wrote first, or the
// task is gone. current is what the adapter re-read, and is zero when the row
// no longer exists.
func CheckAffected(affected int64, id hmntsk.TaskID, expected, current int64) error {
	if affected > 0 {
		return nil
	}

	if current == 0 {
		return &hmntsk.NotFoundError{TaskID: id}
	}

	return ConflictError(id, expected, current)
}

// TrimSQL collapses a statement's whitespace, for readable test failures.
func TrimSQL(sql string) string { return strings.Join(strings.Fields(sql), " ") }
