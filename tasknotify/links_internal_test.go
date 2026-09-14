package tasknotify

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk"
	"github.com/kartaladev/hmntsk/memstore"
	"github.com/kartaladev/hmntsk/notify"
)

func TestLinksFor(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		types  []hmntsk.TypeSpec
		opts   []Option
		event  hmntsk.Event
		assert func(t *testing.T, links map[string]string, err error)
	}

	invoice := hmntsk.TypeSpec{
		Name:     "invoice-approval",
		Metadata: map[string]string{hmntsk.MetadataRoute: "/invoices/{correlation.ownerRef}/approve?task={task.id}"},
	}

	cases := []testCase{
		{
			name:  "a type with a route gives both relations",
			types: []hmntsk.TypeSpec{invoice},
			event: hmntsk.Event{
				TaskID: "task-1", TaskType: "invoice-approval",
				Correlation: hmntsk.CorrelationData{OwnerType: "invoice", OwnerRef: "INV-42"},
			},
			assert: func(t *testing.T, links map[string]string, err error) {
				require.NoError(t, err)
				assert.Equal(t, map[string]string{
					RelationTask:    "/v1/tasks/task-1",
					RelationContext: "/invoices/INV-42/approve?task=task-1",
				}, links)
			},
		},
		{
			name:  "a type without a route gives only the task link",
			types: []hmntsk.TypeSpec{{Name: "plain"}},
			event: hmntsk.Event{TaskID: "task-1", TaskType: "plain"},
			assert: func(t *testing.T, links map[string]string, err error) {
				require.NoError(t, err)
				assert.Equal(t, map[string]string{RelationTask: "/v1/tasks/task-1"}, links)
			},
		},
		{
			name:  "an unregistered type gives only the task link",
			event: hmntsk.Event{TaskID: "task-1", TaskType: "unknown"},
			assert: func(t *testing.T, links map[string]string, err error) {
				require.NoError(t, err, "an instance serving some types still projects the rest")
				assert.Equal(t, map[string]string{RelationTask: "/v1/tasks/task-1"}, links)
			},
		},
		{
			name: "an extra correlation placeholder expands and an unknown one is left as written",
			types: []hmntsk.TypeSpec{{
				Name:     "extra",
				Metadata: map[string]string{hmntsk.MetadataRoute: "/r/{correlation.extra.region}/{host.page}"},
			}},
			event: hmntsk.Event{
				TaskID: "task-1", TaskType: "extra",
				Correlation: hmntsk.CorrelationData{Extra: map[string]string{"region": "eu"}},
			},
			assert: func(t *testing.T, links map[string]string, err error) {
				require.NoError(t, err)
				assert.Equal(t, "/r/eu/{host.page}", links[RelationContext])
			},
		},
		{
			name:  "a raw value is inserted without escaping",
			types: []hmntsk.TypeSpec{invoice},
			event: hmntsk.Event{
				TaskID: "task-1", TaskType: "invoice-approval",
				Correlation: hmntsk.CorrelationData{OwnerRef: "2026/INV 42"},
			},
			assert: func(t *testing.T, links map[string]string, err error) {
				require.NoError(t, err)
				assert.Equal(t, "/invoices/2026/INV 42/approve?task=task-1", links[RelationContext],
					"escaping is the host's, as ExpandRoute documents")
			},
		},
		{
			name:  "a custom task link template",
			opts:  []Option{WithTaskLinkTemplate("https://app.example/tasks/{task.type}/{task.id}")},
			event: hmntsk.Event{TaskID: "task-1", TaskType: "plain"},
			assert: func(t *testing.T, links map[string]string, err error) {
				require.NoError(t, err)
				assert.Equal(t, "https://app.example/tasks/plain/task-1", links[RelationTask])
			},
		},
		{
			name:  "WithLinks replaces both links",
			types: []hmntsk.TypeSpec{invoice},
			opts: []Option{WithLinks(func(_ context.Context, in DraftInput) (map[string]string, error) {
				return map[string]string{"inbox": "/inbox/" + in.Recipient + "/" + in.Kind}, nil
			})},
			event: hmntsk.Event{TaskID: "task-1", TaskType: "invoice-approval"},
			assert: func(t *testing.T, links map[string]string, err error) {
				require.NoError(t, err)
				assert.Equal(t, map[string]string{"inbox": "/inbox/alice/offer"}, links)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			engine, err := hmntsk.New(memstore.New())
			require.NoError(t, err)

			for _, spec := range tc.types {
				require.NoError(t, engine.Register(spec))
			}

			notifier, err := notify.New(notify.NewMemoryStore())
			require.NoError(t, err)

			projector, err := New(engine, notifier, tc.opts...)
			require.NoError(t, err)

			links, err := projector.linksFor(t.Context(), DraftInput{Event: tc.event, Kind: KindOffer, Recipient: "alice"},
				projector.contentFor(tc.event))
			tc.assert(t, links, err)
		})
	}
}
