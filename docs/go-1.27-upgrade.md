# Go 1.27 upgrade review

The toolkit and disposable consumer example require Go 1.27.1. CI selects the
toolchain from each module's `go.mod`. Go 1.27.1 includes fixes to `go fix`,
`encoding/json`, and `net/http` after the initial 1.27 release.
The shared quality gate pins Staticcheck 2026.2.1, which supports Go 1.27.

| Release | Relevant changes | Decision in this repository |
| --- | --- | --- |
| [1.22](https://go.dev/doc/go1.22) | Loop variable semantics, range over integers, `math/rand/v2`, and improved HTTP routing. | Use range over integers when it simplifies count-only loops. The toolkit has no random-number or HTTP router use that warrants migration. |
| [1.23](https://go.dev/doc/go1.23) | Range over iterator functions and synchronous timer channels. | The tests do not rely on old timer channel behavior. Iterator syntax enables the later string iterator improvements. |
| [1.24](https://go.dev/doc/go1.24) | `os.Root`, string iterators, `testing.T.Context`, `T.Chdir`, and `B.Loop`. | Workspace confinement already uses `os.Root`. Use `strings.SplitSeq` and `FieldsSeq` in scans that do not need a slice, and `t.Context()` for bounded work in agent, worker, and process tests. No benchmark uses `B.N`; `T.Chdir` does not help tests that use explicit paths. |
| [1.25](https://go.dev/doc/go1.25) | Stable `testing/synctest`, `sync.WaitGroup.Go`, test attributes, and an experimental garbage collector. | Use `WaitGroup.Go` in concurrency tests. The tests that wait for actual subprocesses need real clocks, so `synctest` is not a fit. Test attributes add no useful metadata to current CI output. |
| [1.26](https://go.dev/doc/go1.26) | The new `go fix`, `new(expr)`, test artifact directories, and the default Green Tea garbage collector. | Apply reviewed `go fix` improvements. No optional-value helper or retained test artifact needs these new APIs. Runtime GC improvements require no source changes. |
| [1.27](https://go.dev/doc/go1.27) | Generic methods, stable JSON v2, stricter JSON defaults, the goroutine leak profile, and automatic `stdversion` vetting during tests. | Use JSON v2 for strict candidate decoding while keeping legacy encoding and digests stable. No repeated generic operation merits a new generic method. The leak profile is available for future diagnosis; the CLI exposes no profiling server. |

The [Go modernizers](https://go.dev/blog/gofix) simplify bounded loops, string
parsing, and collection operations here. The release review did not identify a
case for introducing a new public API, generic abstraction, or benchmark merely
to exercise a language feature.

## Testify assessment

[Testify](https://github.com/stretchr/testify) provides assertions, fatal
requirements, mocks, and test suites. This repository has focused fakes for ACP,
GitHub, and workflows, plus explicit failure messages tied to security and
publication invariants. Only eight test assertions use `reflect.DeepEqual`.
Adding Testify would mostly replace existing `t.Fatal` and `t.Errorf` calls
without increasing coverage. Its [suite package](https://github.com/stretchr/testify#suite-package)
does not support parallel tests. Keep the standard `testing` package and revisit
Testify if repeated assertion boilerplate or mock setup becomes a concrete
maintenance problem.
