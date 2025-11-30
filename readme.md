# funcdiff

`funcdiff` is a Go CLI tool that compares **functions and methods** between two Git refs (branches, tags, or commits) and generates a **Markdown report**.

You can use it, for example, to compare:

- `development` vs `master` (default)
- `feature/*` branches vs `master`
- Previous release tags vs the new one
- `HEAD~1` vs `HEAD` in a PR

The report includes:

- High-level summary of function counts.
- Per-package counts of **new**, **removed**, and **changed** functions.
- Detailed sections:
  - New functions in `from` (not in `to`)
  - Removed functions (only in `to`)
  - Changed functions:
    - Function headers for both sides
    - Line ranges and LOC
    - **Collapsible, full function bodies** for each side

---

## Features

- Compare any two Git refs (`--from`, `--to`).
- Default comparison: `development` → `master`.
- Understand changes to the **codebase map**:
  - Which functions were added/removed/changed?
  - In which packages and files?
  - How large are those functions?
- Supports:
  - All Go functions and methods (exported & unexported).
  - Optional filtering to only exported functions.
  - Optional filtering by package path substring.
- Output is **Markdown**, ready to paste into:
  - Pull Request descriptions
  - Changelogs
  - Design or review docs

---

## Installation

1. Make sure you have Go installed (Go 1.20+ recommended).

2. Put the `main.go` source file of `funcdiff` somewhere inside your Go workspace or any directory.

3. Build the binary:

   ```bash
   go build -o funcdiff main.go


If you prefer to call `funcdiff` from *outside* the repo, use `--dir` to point at it.  
For example, if your project lives in:

`/Users/user/Projects/go/service-ticket`

you can run:

```bash
./funcdiff \
  --dir /Users/user/Projects/go/jaklingko-service-ticket \
  --out-dir /Users/user/Projects/go/funcdiff/changed_funcs \
  --summary-only > ./jaklingko-service-ticket-report.md