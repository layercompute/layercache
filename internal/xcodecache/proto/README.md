# Xcode compilation cache wire protocol

Vendored from swiftlang/llvm-project at `ffadf47113c0e26722cca4c8b7ccabde48c302cc`, under
Apache-2.0 WITH LLVM-exception. Only `go_package` is added.

Generate with protoc and pinned Go plugins:

```sh
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
protoc --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  internal/xcodecache/proto/casv1/*.proto internal/xcodecache/proto/kvv1/*.proto
```
