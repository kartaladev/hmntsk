package invoicing_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
	"github.com/kartaladev/hmntsk/memstore"
	sqlstore "github.com/kartaladev/hmntsk/store/sql"
)

func TestRegister(t *testing.T) {
	t.Parallel()

	svc, err := hmntsk.New(memstore.New(), hmntsk.WithGroupResolver(invoicing.Directory()))
	require.NoError(t, err)

	require.NoError(t, invoicing.Register(svc))

	type testCase struct {
		name   string
		typ    string
		assert func(t *testing.T, spec hmntsk.TypeSpec)
	}

	cases := []testCase{
		{
			name: "review is offered to approvers with a deadline and a link",
			typ:  invoicing.ReviewType,
			assert: func(t *testing.T, spec hmntsk.TypeSpec) {
				assert.Equal(t, []string{invoicing.GroupApprovers}, spec.DefaultAssignment.Groups)
				assert.Positive(t, spec.DefaultDeadline)
				assert.Equal(t, invoicing.Route, spec.Metadata[hmntsk.MetadataRoute])
				assert.NotEmpty(t, spec.InputSchema)
				assert.NotEmpty(t, spec.OutputSchema)
			},
		},
		{
			name: "approval widens to managers when overdue",
			typ:  invoicing.ApproveType,
			assert: func(t *testing.T, spec hmntsk.TypeSpec) {
				assert.Equal(t, []string{invoicing.GroupApprovers}, spec.DefaultAssignment.Groups)
				require.NotNil(t, spec.DefaultEscalation)
				assert.Equal(t, hmntsk.EscalationWiden, spec.DefaultEscalation.Action)
				assert.Equal(t, []string{invoicing.GroupManagers}, spec.DefaultEscalation.AddGroups)
				assert.Equal(t, invoicing.Route, spec.Metadata[hmntsk.MetadataRoute])
				assert.Equal(t, invoicing.ApproveForm, spec.Metadata[hmntsk.MetadataFormKey])
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			spec, err := svc.Registry().Lookup(tc.typ)
			require.NoError(t, err, "type %s is registered", tc.typ)
			tc.assert(t, spec)
		})
	}
}

func TestDirectory(t *testing.T) {
	t.Parallel()

	svc, err := hmntsk.New(memstore.New(), hmntsk.WithGroupResolver(invoicing.Directory()))
	require.NoError(t, err)

	approvers, err := svc.ResolveCandidates(t.Context(), hmntsk.CandidatePool{Groups: []string{invoicing.GroupApprovers}})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{invoicing.Alice, invoicing.Bob}, approvers)

	managers, err := svc.ResolveCandidates(t.Context(), hmntsk.CandidatePool{Groups: []string{invoicing.GroupManagers}})
	require.NoError(t, err)
	assert.Equal(t, []string{invoicing.Carol}, managers)
}

func TestCorrelation(t *testing.T) {
	t.Parallel()

	assert.Equal(t, hmntsk.CorrelationData{
		OwnerType: "invoice", OwnerRef: "INV-42", ActivityKey: "approve",
	}, invoicing.Correlation("INV-42", invoicing.ActivityApprove))
}

func TestRepositories(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name string
		repo func(t *testing.T) invoicing.Repository
	}

	cases := []testCase{
		{
			name: "memory",
			repo: func(*testing.T) invoicing.Repository { return invoicing.NewMemoryRepository() },
		},
		{
			name: "sqlite",
			repo: func(t *testing.T) invoicing.Repository {
				db := openSQLite(t)
				repo := invoicing.NewSQLRepository(db)
				require.NoError(t, repo.Migrate(t.Context()))

				return repo
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := tc.repo(t)
			invoice := invoicing.Invoice{ID: "INV-42", Supplier: "Acme Paper", Amount: 1299}

			require.NoError(t, repo.Save(t.Context(), invoice))

			got, err := repo.Get(t.Context(), "INV-42")
			require.NoError(t, err)
			assert.Equal(t, invoice, got)

			_, err = repo.Get(t.Context(), "INV-404")
			assert.ErrorIs(t, err, invoicing.ErrNotFound)
		})
	}
}

func TestSQLRepositoryJoinsTheHostTransaction(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		finish func(tx *sql.Tx) error
		assert func(t *testing.T, got invoicing.Invoice, err error)
	}

	cases := []testCase{
		{
			name:   "commit keeps the invoice",
			finish: func(tx *sql.Tx) error { return tx.Commit() },
			assert: func(t *testing.T, got invoicing.Invoice, err error) {
				require.NoError(t, err)
				assert.Equal(t, "INV-7", got.ID)
			},
		},
		{
			name:   "rollback discards the invoice",
			finish: func(tx *sql.Tx) error { return tx.Rollback() },
			assert: func(t *testing.T, _ invoicing.Invoice, err error) {
				assert.ErrorIs(t, err, invoicing.ErrNotFound)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			db := openSQLite(t)
			repo := invoicing.NewSQLRepository(db)
			require.NoError(t, repo.Migrate(t.Context()))

			tx, err := db.BeginTx(t.Context(), nil)
			require.NoError(t, err)

			ctx := sqlstore.ContextWithTx(t.Context(), tx)
			require.NoError(t, repo.Save(ctx, invoicing.Invoice{ID: "INV-7", Supplier: "Acme", Amount: 10}))
			require.NoError(t, tc.finish(tx))

			got, err := repo.Get(context.WithoutCancel(t.Context()), "INV-7")
			tc.assert(t, got, err)
		})
	}
}

func openSQLite(t *testing.T) *sql.DB {
	t.Helper()

	db, err := invoicing.OpenSQLite(filepath.Join(t.TempDir(), "invoices.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	return db
}
