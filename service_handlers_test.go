package hmntsk_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
)

// callLog records, in order, what the in-process consumers and factories of one
// case saw.
type callLog struct {
	mu      sync.Mutex
	entries []string
	built   *hmntsk.Service
	output  ApprovalOutput
}

func (l *callLog) add(entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries = append(l.entries, entry)
}

func (l *callLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.entries...)
}

// named is a consumer that logs its name for every event.
func (l *callLog) named(name string) hmntsk.EventHandler {
	return hmntsk.EventHandlerFunc(func(context.Context, hmntsk.Event) error {
		l.add(name)

		return nil
	})
}

// createPooled creates a task of taskType that alice may work on.
func createPooled(t *testing.T, svc *hmntsk.Service, taskType, input string) hmntsk.TaskID {
	t.Helper()

	result, err := svc.Create(t.Context(), hmntsk.CreateRequest{
		Type:       taskType,
		Input:      json.RawMessage(input),
		Candidates: &hmntsk.CandidatePool{Users: []string{"alice"}},
	})
	require.NoError(t, err)

	return result.Task.ID
}

func TestWithEventHandlerFactory(t *testing.T) {
	t.Parallel()

	errFactory := errors.New("the directory could not be reached")

	type testCase struct {
		name    string
		store   func() hmntsk.Store
		options func(log *callLog) []hmntsk.Option
		assert  func(t *testing.T, svc *hmntsk.Service, err error, log *callLog)
	}

	cases := []testCase{
		{
			name: "the factory receives the service being built and its consumers get events",
			options: func(log *callLog) []hmntsk.Option {
				return []hmntsk.Option{
					hmntsk.WithEventHandlerFactory(func(svc *hmntsk.Service) ([]hmntsk.EventHandler, error) {
						log.built = svc

						return []hmntsk.EventHandler{log.named("built")}, nil
					}),
				}
			},
			assert: func(t *testing.T, svc *hmntsk.Service, err error, log *callLog) {
				require.NoError(t, err)
				assert.Same(t, svc, log.built, "the factory is given the service New returns")

				createPooled(t, svc, "freeform", `{}`)
				assert.Equal(t, []string{"built"}, log.snapshot())
			},
		},
		{
			name: "a factory error fails construction and keeps its cause",
			options: func(*callLog) []hmntsk.Option {
				return []hmntsk.Option{
					hmntsk.WithEventHandlerFactory(func(*hmntsk.Service) ([]hmntsk.EventHandler, error) {
						return nil, errFactory
					}),
				}
			},
			assert: func(t *testing.T, svc *hmntsk.Service, err error, _ *callLog) {
				require.ErrorIs(t, err, errFactory)
				assert.Nil(t, svc)
				assert.Contains(t, err.Error(), "build event handlers")
			},
		},
		{
			name: "a configuration error from Define inside a factory is still found by errors.As",
			options: func(*callLog) []hmntsk.Option {
				return []hmntsk.Option{
					hmntsk.WithEventHandlerFactory(func(svc *hmntsk.Service) ([]hmntsk.EventHandler, error) {
						_, err := hmntsk.Define[ApprovalInput, ApprovalOutput](svc, hmntsk.TypeSpec{})

						return nil, err
					}),
				}
			},
			assert: func(t *testing.T, svc *hmntsk.Service, err error, _ *callLog) {
				var configuration *hmntsk.ConfigurationError
				require.ErrorAs(t, err, &configuration)
				assert.Nil(t, svc)
			},
		},
		{
			name: "a nil factory and nil consumers in its result are ignored",
			options: func(log *callLog) []hmntsk.Option {
				return []hmntsk.Option{
					hmntsk.WithEventHandlerFactory(nil),
					hmntsk.WithEventHandlerFactory(func(*hmntsk.Service) ([]hmntsk.EventHandler, error) {
						return []hmntsk.EventHandler{nil, log.named("kept"), nil}, nil
					}),
				}
			},
			assert: func(t *testing.T, svc *hmntsk.Service, err error, log *callLog) {
				require.NoError(t, err)

				createPooled(t, svc, "freeform", `{}`)
				assert.Equal(t, []string{"kept"}, log.snapshot())
			},
		},
		{
			name:  "a refused store never calls the factory",
			store: func() hmntsk.Store { return leakySink{Store: memstore.New()} },
			options: func(log *callLog) []hmntsk.Option {
				return []hmntsk.Option{
					hmntsk.WithEventHandlerFactory(func(*hmntsk.Service) ([]hmntsk.EventHandler, error) {
						log.add("factory called")

						return nil, nil
					}),
				}
			},
			assert: func(t *testing.T, svc *hmntsk.Service, err error, log *callLog) {
				require.ErrorIs(t, err, hmntsk.ErrConfiguration)
				assert.Nil(t, svc)
				assert.Empty(t, log.snapshot())
			},
		},
		{
			name: "consumers are dispatched in registration order, factories included",
			options: func(log *callLog) []hmntsk.Option {
				return []hmntsk.Option{
					hmntsk.WithEventHandlers(log.named("first")),
					hmntsk.WithEventHandlerFactory(func(*hmntsk.Service) ([]hmntsk.EventHandler, error) {
						return []hmntsk.EventHandler{log.named("second"), log.named("third")}, nil
					}),
					hmntsk.WithEventHandlers(log.named("fourth")),
				}
			},
			assert: func(t *testing.T, svc *hmntsk.Service, err error, log *callLog) {
				require.NoError(t, err)

				createPooled(t, svc, "freeform", `{}`)
				assert.Equal(t, []string{"first", "second", "third", "fourth"}, log.snapshot())
			},
		},
		{
			name: "a typed completion consumer is built from the service it observes",
			options: func(log *callLog) []hmntsk.Option {
				return []hmntsk.Option{
					hmntsk.WithEventHandlerFactory(func(svc *hmntsk.Service) ([]hmntsk.EventHandler, error) {
						kind, err := hmntsk.Define[ApprovalInput, ApprovalOutput](svc, hmntsk.TypeSpec{Name: "approval"})
						if err != nil {
							return nil, err
						}

						return []hmntsk.EventHandler{kind.OnCompleted(
							func(_ context.Context, _ hmntsk.Event, output ApprovalOutput) error {
								log.mu.Lock()
								log.output = output
								log.mu.Unlock()
								log.add("completed")

								return nil
							},
						)}, nil
					}),
				}
			},
			assert: func(t *testing.T, svc *hmntsk.Service, err error, log *callLog) {
				require.NoError(t, err)

				ctx := t.Context()
				id := createPooled(t, svc, "approval", `{"amount":1,"justification":"x"}`)

				_, err = svc.Start(ctx, hmntsk.TaskRequest{TaskID: id, Actor: "alice"})
				require.NoError(t, err)

				_, err = svc.Complete(ctx, hmntsk.CompleteRequest{
					TaskRequest: hmntsk.TaskRequest{TaskID: id, Actor: "alice"},
					Output:      json.RawMessage(`{"approved":true,"note":"fine"}`),
				})
				require.NoError(t, err)

				assert.Equal(t, []string{"completed"}, log.snapshot())
				assert.Equal(t, ApprovalOutput{Approved: true, Note: "fine"}, log.output)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := hmntsk.Store(memstore.New())
			if tc.store != nil {
				store = tc.store()
			}

			log := &callLog{}

			svc, err := hmntsk.New(store, append(baseOptions(t), tc.options(log)...)...)
			tc.assert(t, svc, err, log)
		})
	}
}
