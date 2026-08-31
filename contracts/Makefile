# Generación de código desde los contratos.
#
# El core (Rust) genera en su build.rs automáticamente al compilar.
# Go necesita este paso explícito: ejecutar tras cambiar cualquier .proto.
#
#   make -C contracts generate
#
# Requiere: protoc, protoc-gen-go, protoc-gen-go-grpc
#   brew install protobuf
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

PROTO_DIR  := proto
GO_MODULE  := github.com/aibank/aibank/clients/go
GO_OUT     := ../clients/go
PROTOS     := $(shell find $(PROTO_DIR) -name '*.proto')

.PHONY: generate lint clean

generate:
	protoc \
		--proto_path=$(PROTO_DIR) \
		--go_out=$(GO_OUT) --go_opt=module=$(GO_MODULE) \
		--go-grpc_out=$(GO_OUT) --go-grpc_opt=module=$(GO_MODULE) \
		$(PROTOS)
	@echo "Go generado en $(GO_OUT). El core Rust se regenera solo con 'cargo build'."

# Verifica que los .proto compilan sin generar salida.
lint:
	protoc --proto_path=$(PROTO_DIR) -o /dev/null $(PROTOS)
	@echo "contratos válidos"

clean:
	rm -rf $(GO_OUT)/corev1
