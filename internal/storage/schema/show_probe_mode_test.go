package schema

// The sqlmock expectations across this package spell the INFORMATION_SCHEMA
// probes. They keep testing that branch; the SHOW branch has its own tests in
// show_probe_test.go, which switch this back on for their duration.
func init() { useShowProbes = false }
