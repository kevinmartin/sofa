# Consumer configuration

A consumer keeps `.sofa.yml` on its trusted default branch. The
[version 1 JSON Schema](../schemas/sofa.schema.json) documents supported fields
and provides editor completion and diagnostics. It rejects unknown properties,
unsupported agent/recipe types, privileged model credential references, unsafe
paths, and values outside static limits. Retry counts default to zero when
omitted; `recipe` may be omitted or null. Credentials are environment variable
references, never secret values.

Milestone 02 adds an optional `lifecycle` block for consumers enabling Project
Discovery and completion reconciliation. `statuses` maps all ten canonical
stages to distinct names without surrounding whitespace in the Project's
built-in Status field. Its `ready` entry must equal the existing `ready_status`;
existing delivery-only callers may omit the block. `poll_minutes` defaults to
ten and accepts 60 for the hourly
economy preset. `discovery_wip` and `delivery_wip` default to two and one.
Optional `dependencies_field` and `priority_field` name owner-managed Project
fields. Dependencies use `none` or strict same-repository `#N` references;
malformed or unavailable field values block dependent work. When configured,
priority must be an exact owner-set `P0`, `P1`, `P2`, `P3`, or `P4` value, ordered
highest to lowest before issue number. Blank or unknown values hold that item;
the model never assigns priority.
The controller uses separate Project text fields named `Blocked reason` and
`Next action` by default. Create both fields on the private Project to enable
durable board advice. Existing Projects without either field retain their
current behavior; a partial pair is a setup error. To use other names, set
`blocked_reason_field` and `next_action_field` together; both must exist as
distinct text fields. Omitted names retain the default without changing the
trusted configuration digest of existing lifecycle consumers.
`spec_author_id` pins the trusted bot that publishes versioned specification
comments; hosted Discovery blocks when its identity is unavailable. This is a
public node ID, never a credential.
`release.required_checks` lists exact post-merge check names and their trusted
GitHub App IDs; an unavailable required check blocks Done. Configuration does not grant Project write access
or authorize moving items to Backlog or Ready.

The [consumer example](../examples/consumer/.sofa.yml) starts with a
[YAML language server](https://github.com/redhat-developer/vscode-yaml)
association that resolves within this toolkit checkout:

```yaml
# yaml-language-server: $schema=../../schemas/sofa.schema.json
```

After copying the example into a consumer repository, replace that relative
path with the raw schema URL at the **same published toolkit commit** used by
both reusable workflows and their `toolkit_sha` inputs:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/kevinmartin/sofa/TOOLKIT_COMMIT_SHA/schemas/sofa.schema.json
```

Replace `TOOLKIT_COMMIT_SHA` with the full reviewed commit SHA. For offline
editing, copy that commit's schema into the consumer's `schemas/` directory
and use `$schema=./schemas/sofa.schema.json` instead. Keep the copied schema in
step with the toolkit pin.

The [Go decoder](../internal/config/config.go) remains authoritative at runtime.
Some requirements depend on the complete configuration or the original YAML
and cannot be represented by this standard JSON Schema:

- Check IDs must be unique, and each timeout must fit `limits.attempt_seconds`.
- `max_total_bytes` must be at least `max_file_bytes`; recipe paths must be
  allowed and their count must fit `limits.max_files`.
- YAML must contain exactly one document, no unknown or duplicate fields,
  no anchors or aliases, and at most 64 KiB.
- Runtime string limits count UTF-8 bytes; JSON Schema lengths count Unicode
  characters, so non-ASCII values can reach a runtime limit sooner.
- Lifecycle status names must be unique and `statuses.ready` must match
  `ready_status`; the schema verifies keys and shape, while Go checks these
  semantic relationships.
- `blocked_reason_field` and `next_action_field` must be configured together.
  The effective advice fields, `dependencies_field`, and `priority_field` must
  have distinct names, none of which may be `Status`.
- Each `release.required_checks` name and App ID pair must be unique.

Run `go test ./internal/config` from the toolkit root to compile the schema,
validate the consumer example and invalid-policy cases, and verify these
runtime-only boundaries. Schema diagnostics assist editing; they do not grant
admission or replace trusted runtime checks.
