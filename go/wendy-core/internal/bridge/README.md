# C bridge adapter

This directory is reserved for the thin adapter from `include/Wendy` into the public Go SDK and shared Go core.

A bridge operation will:

1. resolve an opaque C handle without exposing a Go pointer;
2. decode its versioned SDK protobuf request;
3. derive a cancellable Go context;
4. invoke the same Go operation used by the CLI;
5. encode the response once into an owned C buffer; and
6. complete exactly once with a response or structured error.

The adapter owns no product validation, routing, authorization, retry, or presentation behavior.
