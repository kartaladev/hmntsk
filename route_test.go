package hmntsk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kartaladev/hmntsk"
)

func TestExpandRoute(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name     string
		template string
		task     hmntsk.Task
		assert   func(t *testing.T, expanded string)
	}

	task := hmntsk.Task{
		ID:   "019243af-9f1c-7000-8000-000000000042",
		Type: "approval",
		Correlation: hmntsk.CorrelationData{
			OwnerType:   "invoice",
			OwnerRef:    "INV-42",
			ActivityKey: "approve",
			Extra:       map[string]string{"tenant": "acme", "path": "eu west/a&b?c"},
		},
	}

	expands := func(expected string) func(t *testing.T, expanded string) {
		return func(t *testing.T, expanded string) {
			assert.Equal(t, expected, expanded)
		}
	}

	cases := []testCase{
		{
			name:     "a route template expands for a task",
			template: "/invoices/{correlation.ownerRef}/approve?task={task.id}",
			task:     task,
			assert:   expands("/invoices/INV-42/approve?task=019243af-9f1c-7000-8000-000000000042"),
		},
		{
			name:     "every task and correlation field has a placeholder",
			template: "{task.id}|{task.type}|{correlation.ownerType}|{correlation.ownerRef}|{correlation.activityKey}",
			task:     task,
			assert:   expands("019243af-9f1c-7000-8000-000000000042|approval|invoice|INV-42|approve"),
		},
		{
			name:     "an extra correlation key has a placeholder of its own",
			template: "https://{correlation.extra.tenant}.example/tasks",
			task:     task,
			assert:   expands("https://acme.example/tasks"),
		},
		{
			name:     "an unknown placeholder is left alone",
			template: "/{tenant}/tasks/{task.id}",
			task:     task,
			assert:   expands("/{tenant}/tasks/019243af-9f1c-7000-8000-000000000042"),
		},
		{
			name:     "a missing extra key is left alone",
			template: "/{correlation.extra.region}/{correlation.extra.tenant}",
			task:     task,
			assert:   expands("/{correlation.extra.region}/acme"),
		},
		{
			name:     "values are inserted raw, because escaping depends on where the template points",
			template: "/files?p={correlation.extra.path}",
			task:     task,
			assert:   expands("/files?p=eu west/a&b?c"),
		},
		{
			name:     "a placeholder used twice is replaced twice",
			template: "{task.type}/{task.type}",
			task:     task,
			assert:   expands("approval/approval"),
		},
		{
			name:     "a known field with no value becomes empty",
			template: "/process/{correlation.activityKey}",
			task:     hmntsk.Task{ID: "t-1", Type: "approval"},
			assert:   expands("/process/"),
		},
		{
			name:     "a placeholder inside extra braces still expands",
			template: "{{task.type}}",
			task:     task,
			assert:   expands("{approval}"),
		},
		{
			name:     "an unclosed brace is left alone",
			template: "/tasks/{task.id",
			task:     task,
			assert:   expands("/tasks/{task.id"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, hmntsk.ExpandRoute(tc.template, tc.task))
		})
	}
}
