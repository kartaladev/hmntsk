// Test doubles for the ports a projector calls through. Their only consumers are
// this package's tests, so they are generated into _test.go files here and never
// reach a production build.
//
//go:generate mockgen -source=../notify/store.go -package=tasknotify -destination=notify_store_mock_test.go -typed
//go:generate mockgen -destination=group_resolver_mock_test.go -package=tasknotify -typed github.com/kartaladev/hmntsk GroupResolver

package tasknotify
