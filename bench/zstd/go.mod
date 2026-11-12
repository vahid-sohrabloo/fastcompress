module github.com/vahid-sohrabloo/fastcompress/bench/zstd

go 1.25

require (
	github.com/klauspost/compress v1.20.3
	github.com/vahid-sohrabloo/fastcompress v0.0.0
)

// Kept in this nested module so the root module never depends on
// klauspost/compress (the library is used through its public API only).
replace github.com/vahid-sohrabloo/fastcompress => ../..
