module github.com/kartaladev/hmntsk/transport/http

go 1.26

// The dependencies on github.com/kartaladev/hmntsk,
// github.com/kartaladev/hmntsk/transport/core and
// github.com/kartaladev/hmntsk/transporttest are supplied by the repository's
// go.work during development and are written in here, with real versions, when
// the modules are tagged.
require github.com/stretchr/testify v1.12.1

require go.yaml.in/yaml/v3 v3.0.5 // indirect
