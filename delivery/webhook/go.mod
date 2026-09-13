module github.com/kartaladev/hmntsk/delivery/webhook

go 1.26.0

// The dependencies on github.com/kartaladev/hmntsk and
// github.com/kartaladev/hmntsk/relaytest are supplied by the repository's
// go.work during development and are written in here, with real versions, when
// the modules are tagged. Core is tagged before this module; see the release
// order in docs/releasing.md.
require github.com/stretchr/testify v1.12.1

require go.yaml.in/yaml/v3 v3.0.5 // indirect
