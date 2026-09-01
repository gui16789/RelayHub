# Contributing

## Commit Message Convention

Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/):

```
feat: add gemini relay
fix: handle 429 Retry-After
docs: update README
chore: update dependencies
```

The bot uses this prefix to auto-generate the changelog on release.

## Branch Flow

1. Create a feature branch from `main`: `git checkout -b feat/your-feature`
2. Write or update tests where applicable
3. Open a PR with a clear description of what changed and why

## Development

```bash
# Run tests
go test ./...

# Desktop dev (requires Node.js)
go run main.go

# Headless dev
go run ./cmd/headless
```

## Versioning

- Bump `wails.json → info.productVersion` on release
- Update `CHANGELOG.md` by promoting `[Unreleased]` to `[x.y.z]`
- Tag the release: `git tag v1.0.1 && git push origin v1.0.1`
- The GitHub Actions release workflow builds all targets automatically
