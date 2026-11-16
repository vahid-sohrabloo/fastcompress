module github.com/vahid-sohrabloo/fastcompress/bench/lz4

go 1.25

require (
	github.com/pierrec/lz4/v4 v4.1.33
	github.com/vahid-sohrabloo/fastcompress v0.0.0-00010101000000-000000000000
)

// Kept in this nested module so the root module never depends on
// pierrec/lz4 (the library is used through its public API only). Our
// package is imported to produce the frames that every decoder times.
replace github.com/vahid-sohrabloo/fastcompress => ../..
