#!/usr/bin/env bash
set -e

# build-ffi.sh compiles the go-auto-err-handling tool into a C-shared archive 
# for integration with bridle-ctl via FFI.

echo "Building libgoautoerr.a..."
go build -buildmode=c-archive -o libgoautoerr.a ./cmd/ffi/main.go
echo "Done! The archive and header are available at libgoautoerr.a and libgoautoerr.h"
