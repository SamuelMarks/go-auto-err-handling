go-auto-err-handling
====================

[![License: Apache-2.0](https://img.shields.io/badge/license-Apache%202.0-blue)](https://opensource.org/license/apache-2-0)
[![go test](https://github.com/SamuelMarks/go-auto-err-handling/actions/workflows/ci.yml/badge.svg)](https://github.com/SamuelMarks/go-auto-err-handling/actions/workflows/ci.yml)

`go-auto-err-handling` is a powerful static analysis and refactoring tool designed to eliminate technical debt associated with unhandled errors in Go.

Unlike simple linters that only report ignored errors, this tool **automatically injects the necessary handling code**. It is capable of refactoring function signatures, propagating errors up the call stack, handling deferred errors using `errors.Join`, and intelligently generating zero-values for return statements.

## 🚀 Key Features

*   **Iterative Refactoring**: Runs in a loop (up to 5 passes) to ensure that changes introduced in one pass (like adding an error return to a function) are correctly handled by callers in the next pass.
*   **Intelligent Zero-Value Generation**: using `go/types`, it generates correct zero values for return statements (e.g., `return 0, "", nil, err`) based on the specific types defined in your signatures.
*   **Signature Propagation**: Automatically converts `func Do()` to `func Do() error` if an unhandled error is detected inside, and updates all call sites (`x := Do()` becomes `x, err := Do(); ...`).
*   **Defer Handling**: Detects ignored errors in `defer` statements and rewrites them to use `errors.Join` (Go 1.20+) to ensure deferred errors are captured in named return variables.
*   **Main/Init Safety**: Automatically detects entry points (`main()`, `init()`) and injects terminal handling (Log/Fatal/Panic) instead of attempting to change their signatures.
*   **Custom Templates**: Fully customizable return handling via templates (e.g., wrapping errors with `fmt.Errorf` or generic library wrappers).
*   **Smart Defaults**: Automatically ignores common "safe" ignored errors (like `fmt.Println` or `bytes.Buffer.Write`) unless configured otherwise.

## 📦 Installation

**Prerequisites**: Go 1.22 or higher.

```bash
# Install directly
go install github.com/SamuelMarks/go-auto-err-handling@latest
```

Or build from source:

```bash
git clone https://github.com/SamuelMarks/go-auto-err-handling.git
cd go-auto-err-handling
go build -o auto-err
```

## 🛠 Usage

The tool operates in three primary "Levels" of aggressiveness.

```bash
auto-err [flags] [path/to/packages]
```

### Refactoring Strategies

#### Level 0: Local Pre-existing Errors (`--local-preexisting-err`)
Targets functions that **already return an error** but ignore a specific call inside.
*   *Action*: Injects `if err := call(); err != nil { return ..., err }`.

#### Level 1: Local Non-existing Errors (`--local-nonexisting-err`)
Targets functions that **do not** currently return an error.
*   *Action*:
    1. Modifies signature to append `error`.
    2. Updates all `return` statements in the function to include `nil`.
    3. Injects error handling for the ignored call.
    4. Recursively updates all callers of this function to handle the new return signature.

#### Level 2: Third Party Errors (`--third-party-err`)
Targets ignored errors coming from external modules or the standard library (e.g., `os.WriteFile`).
*   *Action*: Treats 3rd party calls as critical failures and triggers the Level 1 propagation logic to hoist the error up the stack.

### Deferred Error Handling
The tool automatically scans for `defer f()` calls where `f` returns an error. It refactors the enclosing function to use **named returns** and injects `errors.Join`.

**Before:**
```go
func process() error {
    defer file.Close() // returns error, currently ignored
    return nil
}
```

**After:**
```go
func process() (err error) {
    defer func() {
        err = errors.Join(err, file.Close())
    }()
    return nil
}
```

### Command Line Flags

| Flag | Type | Description |
| :--- | :--- | :--- |
| `--dry-run` | Bool | Print diffs to stdout instead of modifying files on disk. |
| `--local-preexisting-err` | Bool | Enable Level 0 analysis. |
| `--local-nonexisting-err` | Bool | Enable Level 1 analysis (Signature rewriting). |
| `--third-party-err` | Bool | Enable Level 2 analysis (External libs). |
| `--no-default-exclusion` | Bool | Analyze ALL calls, including `fmt.Print` and `log.Print`. |
| `--main-handler` | String | Strategy for `main`/`init`. Options: `log-fatal` (default), `os-exit`, `panic`. |
| `--error-template` | String | Custom return template. Default: `"{return-zero}, err"`. |
| `--exclude-glob` | List | File patterns to skip (e.g. `*_test.go`, `vendor/*`). |
| `--exclude-symbol-glob` | List | Symbol patterns to skip (e.g. `path/to/pkg.Func`). |

### Examples

#### 1. Preview changes (Dry Run)
```bash
auto-err --local-nonexisting-err --dry-run ./pkg/...
```

#### 2. Full Refactor with Custom Wrapping
Injects `fmt.Errorf` wrapping context instead of raw returns.

```bash
auto-err \
  --local-nonexisting-err \
  --third-party-err \
  --error-template '{return-zero}, fmt.Errorf("failed in {func_name}: %w", err)' \
  ./...
```

#### 3. Strict Mode (Check everything)
Checks standard library calls usually ignored (like `fmt.Println`).

```bash
auto-err --no-default-exclusion --local-preexisting-err .
```

## ⚙️ Configuration & templates

### Error Templates
The `--error-template` string supports specific placeholders:
- `{return-zero}`: Generates the comma-separated zero values for non-error returns (e.g., `0, "", nil`).
- `{func_name}`: The name of the function being called that generated the error.
- `err`: The variable name for the error (automatically resolved to avoid shadowing, e.g., `err`, `err1`).

### Main Handler Strategies
When an error propagates up to `main()` or `init()`, the signature cannot be changed (Go spec). You can choose how these are handled:
- `log-fatal`: `if err != nil { log.Fatal(err) }`
- `os-exit`: `if err != nil { fmt.Println(err); os.Exit(1) }`
- `panic`: `if err != nil { panic(err) }`

## 🏗 Architecture

The tool is built on `golang.org/x/tools/go/packages` and proceeds in the following phases:

1.  **Loader**: Parses AST, Type info, and Module structure.
2.  **Filter**: Applies glob patterns and exclusion defaults (e.g., ignoring `strings.Builder.Write`).
3.  **Analysis**:
    *   Walks the AST looking for `ExprStmt` (bare calls) or `AssignStmt` (assignment to `_`) where the underlying type is `error`.
    *   Determines if the call is internal or third-party.
4.  **Refactor & Rewrite**:
    *   **Injector**: Calculates unique variable names (`err` vs `err1`) based on scope.
    *   **Signature**: Uses `pkg/astgen` to calculate zero values for return types.
    *   **Defer**: Inspects defers and applies `errors.Join` transformations.
    *   **Propagate**: If a function signature changes, it crawls the Graph of usage to update all call sites.
5.  **Format**: Runs `ast.Format` and `golang.org/x/tools/imports` (goimports) to fix spacing and missing imports.
6.  **Loop**: The runner repeats this process (default 5 passes) until the codebase stabilizes, handling deep call stacks recursively.

## 🛡 Exclusions

By default, the tool ignores functions that are widely accepted as safe to ignore in Go. These are defined in `pkg/filter/defaults.go`:

*   `fmt.Print*`, `fmt.Sprint*`, `fmt.Scan*`
*   `log.Print*`, `log.Output`
*   `strings.Builder.Write*`
*   `bytes.Buffer.Write*`

disable this list with `--no-default-exclusion`.

## 🤝 Contributing

This project aims for **100% test coverage** on logic packages.

1.  Clone the repo.
2.  Run tests:
    ```bash
    go test ./... -v
    ```
3.  Submit a PR.

## 📄 License

Licensed under the [Apache 2.0 License](LICENSE-APACHE).
