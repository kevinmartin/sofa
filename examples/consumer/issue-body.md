Update `fixture.Greeting(name string) string` in `fixture/greeting.go` so it
trims surrounding whitespace from `name` before formatting the greeting.
Return `Hello, friend` when the trimmed name is empty. Keep `Hello, Ada` for
`Greeting("Ada")` and `Greeting("  Ada  ")`. Change only
`fixture/greeting.go`. Do not edit tests, workflow files, or configuration.
