

## 2023-05-22

Networking

- https://pkg.go.dev/github.com/pawelgaczynski/gain
    - Very new io_uring networking lib
- https://github.com/xtaci/gaio
    - Cool and minimal memory efficient tcp networking lib. Zero deps. No windows support
- These are all attempts at performant and featureful networking libs. gnet is most popular/mature
    - https://github.com/Allenxuxu/gev
    - https://github.com/panjf2000/gnet
    - https://github.com/lesismal/nbio (minimal deps)
    - https://github.com/tidwall/evio (minimal deps)
        - no backpressure mechanism

## 2023-05-23

- [witx wasi types](https://github.com/WebAssembly/WASI/blob/a206794fea66118945a520f6e0af3754cc51860b/phases/snapshot/witx/typenames.witx)
- Wasi socket networking blogpost: https://radu-matei.com/blog/towards-sockets-networking-wasi/
- [wazero greet example](https://github.com/tetratelabs/wazero/blob/4aca6fbd0e6404b30e86d4cfd97f7a465926fe7c/examples/allocation/tinygo/greet.go)
- [wazero discussion of efficient memory copy/read](https://github.com/tetratelabs/wazero/issues/525)

Initial design
- Listening on a port on all interfaces
- Proxy requests to each application, keep track of connection ids, send bytes back to a connection. Connection use is owned by either the http handler goroutine or the goroutine of the running application.
- Sleeping and resuming memory from disk (skip for now!)
- Hosting static web page and and info page

## 2023-05-28

Instantiate is much faster with a module filecache. 0.6ms vs 3ms.
```
BenchmarkInstance/filecache
BenchmarkInstance/filecache-8         	    1683	    661342 ns/op	  623543 B/op	    1574 allocs/op
BenchmarkInstance/no_cache
BenchmarkInstance/no_cache-8          	     403	   3026209 ns/op	 3222913 B/op	    2580 allocs/op
```

## 2023-05-29

A few things to do:

1. Hash input not output, figure out how to include tinygo version.
2. Capture logs and events
3. Fix deployed subdomain

## 2023-06-01

Set up a benchmark. 100 invocations of OnConnRead that trigger a wasm call of conn_write.

- `452 ns` with 0 allocs without `WithCloseOnContextDone(true)`
- `854 ns` with 3 allocs with `WithCloseOnContextDone(true)`

```
$ go test -bench=BenchmarkRead -benchmem -memprofile memprofile.out
goos: darwin
goarch: arm64
pkg: github.com/maxmcd/256kb.cloud
BenchmarkRead-8            26292             45205 ns/op             135 B/op          0 allocs/op
BenchmarkRead-8            14179             85431 ns/op           16287 B/op        300 allocs/op
```

It looks like the overhead of `WithCloseOnContextDone(true)` is [a goroutine on every](https://github.com/tetratelabs/wazero/blob/51d0271755282839abe56cab368642babcf75bc6/internal/wasm/module_instance.go#L40) `.Call`. Maybe it's possible to run a single supervisor goroutine that detects long execution time more generally and halts the application if it is spinning for a long time. This is also good because a single `.Call` can be ok to take a long time as long as it is producing a lot of syscall activity, so we're a little beyond using simple timeouts already.
