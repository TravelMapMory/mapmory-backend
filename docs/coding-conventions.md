# Backend Coding Conventions

This document describes the conventions followed in the MapMory Go backend. Its purpose is to keep contributions consistent, maintainable, and easy to review.

These conventions supplement Go's established practices. When in doubt, prefer idiomatic Go and simplicity over project-specific abstractions.

## 1. Formatting and static checks

All Go source files must be formatted with `gofmt`.

Before opening a pull request, run:

```bash
gofmt -l .
go vet ./...
go build ./...
go test ./...
```

`gofmt -l .` should produce no output. Fix formatting with:

```bash
gofmt -w .
```

CI enforces these checks and also verifies that the Docker image builds.

Formatting is mandatory. Other style recommendations in this document are primarily enforced through code review.

## 2. Naming

Follow standard Go naming conventions:

- Use short, lowercase package names without underscores.
- Use `MixedCaps` or `mixedCaps` for identifiers, not `snake_case`.
- Use exported names only when required outside a package.
- Prefer descriptive names, but avoid unnecessary repetition of package names.
- Use established initialisms consistently, such as `ID`, `URL`, `HTTP`, and `JSON`.

Avoid introducing abbreviations that are not obvious to other contributors.

## 3. Packages and project structure

- Keep executable entry points under `cmd/`.
- Use `internal/` for application-specific implementation packages when separation is useful.
- Group related code by responsibility rather than creating packages for every type or function.
- Avoid circular dependencies and unnecessary abstraction layers.
- Keep HTTP transport concerns separate from persistence and domain logic as these responsibilities grow.

Do not reorganize existing packages solely for stylistic consistency. Structural changes should have a clear implementation or maintenance benefit.

## 4. Error handling

- Handle errors explicitly; do not silently discard them.
- Return errors when callers can make a meaningful decision about recovery or response.
- Wrap errors with useful context using `fmt.Errorf` and `%w` when appropriate.
- Avoid `panic` for expected runtime failures, including invalid requests and external service errors.
- Do not expose internal implementation details or sensitive data in API responses.

Error messages should help identify what failed without unnecessarily repeating information.

## 5. HTTP handlers and API behavior

- Follow the shared OpenAPI contract for externally visible request and response behavior.
- Validate request inputs at the API boundary.
- Use appropriate HTTP status codes and consistent JSON response shapes.
- Keep authorization and ownership checks explicit where relevant.
- Do not assume that client-supplied user IDs establish ownership.
- Pass request contexts to downstream operations that support cancellation.

When an endpoint intentionally differs from the OpenAPI contract, document the discrepancy and resolve it with the team.

## 6. Data and concurrency

- Use clear ownership boundaries for mutable state.
- Protect shared mutable state against concurrent access.
- Prefer passing dependencies explicitly rather than relying on mutable global state.
- Use database transactions when operations must succeed or fail together.
- Keep migrations and persistent data changes reviewable and reproducible.

Do not introduce concurrency unless it is useful for the task.

## 7. Testing

- Add or update tests for meaningful behavioral changes and bug fixes.
- Prefer standard Go tests using `testing`.
- Cover successful cases, validation failures, and relevant edge cases.
- Keep unit tests deterministic and independent of external services where practical.
- Use integration tests for behavior that genuinely depends on databases or other services.
- Avoid tests that depend on execution order or shared mutable fixtures.

Run `go test ./...` before requesting review. Consider `go test -race ./...` when changing shared state or concurrent code.

## 8. Dependencies

- Prefer the standard library when it adequately solves the problem.
- Use external packages when they offer clear benefits in correctness, reliability, or maintainability.
- Avoid implementing complex, established protocols or data formats from scratch without documenting the rationale.
- Keep `go.mod` and `go.sum` consistent with dependency changes.

## 9. Documentation and code review

- Write comments that explain non-obvious behavior, constraints, or design decisions rather than restating code.
- Document exported APIs when needed for their intended consumers.
- Keep the README accurate when setup, commands, or architecture change.
- Keep pull requests focused and link related issues.
- Explain significant tradeoffs and known limitations in the PR description.

Coding conventions should evolve through team agreement as the backend architecture matures.
