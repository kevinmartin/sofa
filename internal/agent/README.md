# Private Copilot ACP adapter

`Run` performs one initialization, one new session, and one prompt through Caelis
ACP Go SDK v1.4.0. The default command is `copilot --acp --stdio`; an explicit
command is useful for the fake peer. Only the supplied environment is passed to
the child. Executable lookup uses its explicit absolute PATH entries.

The caller supplies a positive timeout, an absolute workspace, and an exact list
of permitted relative files. Hidden paths and agent-policy files are excluded.
Filesystem callbacks reject traversal, symlinks, hard links, nonregular files,
unknown sessions, and files outside that list. Each directory is opened with
`openat` and `O_NOFOLLOW` on Linux/macOS. File contents are capped at 1 MiB.
Permission requests allow only scoped read/edit operations with an `allow_once`
option; execution, network, unscoped, and unknown requests are denied. Native
agent tools can bypass ACP callbacks, and permissions remain advisory for those
tools. A read/edit permission's path check is a point-in-time check, not a lease
over native tool execution.

**The caller must provide the operating-system isolation.** Its container must
expose only the disposable workspace and the configured provider credential,
without Git/Projects/publication credentials, host homes, Docker sockets, or
other repositories. The parent of the mounted workspace must be immutable to
the agent. Native tool access is limited by that container, not this Go client.
Process-group cleanup kills ordinary descendants on completion/cancellation;
container teardown must also kill descendants that deliberately detach into
another process group. Candidate tests run later without provider credentials.

Raw streamed text and stderr are discarded. Protocol queues and frames are
bounded. Session metadata contains a SHA-256 session identifier fingerprint,
counts of streamed updates and permission requests, a validated stop reason,
and the number of prompt requests attempted. `ModelCalls` remains unknown: one
ACP turn can make multiple internal calls. The caller must require `end_turn`
and its independent candidate validations before accepting a checkpoint.

Authentication and recognized structured quota errors are classified without
returning remote messages. Unknown errors fail closed as protocol errors; this
does not claim recognition of undocumented Copilot quota formats. Retries and
aggregate limits belong to the durable orchestrator, not this adapter.

The Go test helper is a real child process exchanging ACP JSON-RPC over stdio.
It validates negotiation, streaming, permission denial, malformed responses,
authentication/quota errors, explicit environment isolation, cancellation,
timeout, descendant cleanup, and filesystem boundary cases without inference.
These tests do not establish live Copilot interoperability.
