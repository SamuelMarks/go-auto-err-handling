go-auto-err-handling
====================

`go-auto-err-handling` is a static analysis and refactoring tool for Go that automatically injects error handling code. It detects unchecked errors and rewrites your source code to handle them, optionally modifying function signatures to propagate errors up the call stack.

## Features

- **Level 0 (Local Preexisting)**: Injects `if err != nil { return ..., err }` into functions that *already* return an `error` but are ignoring specific calls.
- **Level 1 (Local Non-Existing)**: Modifies function signatures (e.g., `func Foo()` -> `func Foo() error`) to allow error propagation, then updates all callers.
- **Level 2 (Third Party)**: Handles errors returned by 3rd-party libraries (e.g., `os`, `fmt`) and modifies the calling code to propagate them.
- **Smart Generation**: Generates correct zero values (`nil`, `0`, `""`, etc.) for strictly typed returns.
- **Exclusion**: Supports excluding specific files or symbols via glob patterns.

## Installation

### From Source

```bash
go install github.com/SamuelMarks/go-auto-err-handling@latest
```

Or clone and build locally:

```bash
git clone https://github.com/SamuelMarks/go-auto-err-handling.git
cd go-auto-err-handling
go build -o auto-err
```

## Usage

The tool is primarily CLI-based.

```bash
# Basic usage on current directory
auto-err

# Enable signature modification (Level 1)
auto-err --local-nonexisting-err

# exclude specific files
auto-err --exclude-glob "*_test.go"
```

### Configuration Flags

| Flag | Description |
| :--- | :--- |
| `--local-preexisting-err` | Edit functions that already return an `error` in their signature to handle previously ignored errors. (Default: False) |
| `--local-nonexisting-err` | Edit functions that *do not* return an error by adding `error` to the signature, handling the error, and updating callers. (Default: False) |
| `--third-party-err` | Same as above, but specifically targets errors coming from dependencies outside your module (including stdlib). (Default: False) |
| `--exclude-glob` | Glob patterns to exclude specific file paths (e.g., `vendor/*`). |
| `--exclude-symbol-glob` | Glob patterns to exclude specific symbols (e.g., `fmt.*`, `strings.Builder.Write`). |
| `--help` | Show help context. |

### Examples

#### Level 0: Preexisting Error

**Before:**
```go
func doBusinessLogic() error {
    saveToDb() // returns error, currently ignored
    return nil
}
```

**After (`--local-preexisting-err`):**
```go
func doBusinessLogic() error {
    err := saveToDb()
    if err != nil {
        return err
    }
    return nil
}
```

#### Level 1: Signature Mutation

**Before:**
```go
func main() {
    process()
}

func process() {
    saveToDb() // returns error, ignored. 'process' returns nothing.
}
```

**After (`--local-nonexisting-err`):**
```go
func main() {
    // Caller updated to handle new return, or assigned to _ if main can't be changed
    _ = process() 
}

// Signature changed to return error
func process() error {
    err := saveToDb()
    if err != nil {
        return err
    }
    return nil
}
```

## Development

### Prerequisites

- Go 1.22 or higher.

### Running Tests

This project aims for 100% test coverage.

```bash
go test ./... -v
```

### Building

```bash
go build -o auto-err main.go config.go
```

### Updating Dependencies

This project relies on `golang.org/x/tools` for AST manipulation and `alecthomas/kong` for CLI parsing.

To update dependencies:

```bash
go get -u ./...
go mod tidy
```

### Project Structure

| Directory | Purpose |
| :--- | :--- |
| `pkg/loader` | Wraps `go/packages` to load syntax, types, and type info. |
| `pkg/analysis` | Uses `errcheck` to identify AST nodes where errors are ignored. |
| `pkg/astgen` | Generates AST expressions for zero values (e.g., `0`, `""`) derived from `types.Type`. |
| `pkg/filter` | Logic for excluding files and symbols based on CLI globs. |
| `pkg/rewrite` | Level 0 logic: Injecting `if err != nil` into existing ASTs. |
| `pkg/refactor` | Level 1 logic: Changing function signatures and propagating changes to callers. |
| `pkg/runner` | Integration loop that runs analysis -> refactor -> save until stable. |

## License

Apache-2.0

