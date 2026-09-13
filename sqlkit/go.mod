module github.com/kartaladev/hmntsk/sqlkit

go 1.26.0

// sqlkit names no other module of this repository and never will: it is the
// domain-free half that the task stores and the notifier both build on, and it
// moves to its own repository before its first tag.

require github.com/stretchr/testify v1.12.1

require go.yaml.in/yaml/v3 v3.0.5 // indirect
