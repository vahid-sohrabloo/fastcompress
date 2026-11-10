module github.com/vahid-sohrabloo/fastcompress/bench/gzip

go 1.25

require (
	github.com/klauspost/compress v1.20.3
	github.com/vahid-sohrabloo/fastcompress v0.0.0
)

// ours plugs in here in Phase 2/3 (see README.md): import
// github.com/vahid-sohrabloo/fastcompress/<pkg> from ours.go. Kept in this nested
// module so the root module never depends on klauspost/compress.
replace github.com/vahid-sohrabloo/fastcompress => ../..
