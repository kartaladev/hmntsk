module github.com/kartaladev/hmntsk/relaytest

// The dependency on github.com/kartaladev/hmntsk is supplied by the
// repository's go.work during development and is written in here, with a real
// version, when the modules are tagged. Core is tagged before this module; see
// the release order in docs/releasing.md.

go 1.26.0

require github.com/stretchr/testify v1.12.1

require go.yaml.in/yaml/v3 v3.0.5 // indirect
