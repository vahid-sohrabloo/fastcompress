// Package deflatetest holds shared, deterministic test inputs for the
// DEFLATE-family packages (internal/deflate, flate, gzip, zlib) and their
// comparison against the libdeflate reference.
//
// It imports only the standard library, so any package may use it without
// creating a dependency cycle.
//
// Determinism guarantee: Cases returns byte-identical data on every call.
// Every generator draws only PCG Uint64 values from a math/rand/v2 PCG seeded
// from a fixed function of the case name, and reduces them with integer
// arithmetic of its own (no floating point, no derived rand methods, no time,
// global random source, map iteration order or environment). The bytes are
// therefore independent of platform and Go version as far as the PCG stream
// guarantee goes; the golden hashes in corpus_test.go pin them (checked on
// amd64). Changing a generator is a visible event and must be done
// deliberately, because recorded libdeflate reference outputs depend on these
// inputs.
//
// Not provided: the plan's "up to 4 GiB edge" sparse synthetic. It must not be
// materialised in memory; it is deferred to the Phase 2 compressor identity
// task, which can stream it.
//
// Silesia gives lazy access to the Silesia corpus under bench/corpus/silesia
// (override the corpus directory with DEFLATETEST_CORPUS).
package deflatetest
