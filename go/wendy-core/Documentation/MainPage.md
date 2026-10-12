# Wendy SDK

The Wendy SDK is a Go cross-platform core exposed to other languages through a small, stable C bridge ABI.

This draft is an architecture scaffold. It intentionally contains only runtime/lifetime primitives, one representative unary device operation, and one representative stream. It does not replace the currently shipped Companion SDK.

## Layers

```text
Swift / Python / Kotlin / optional full C SDK
                         |
              bridge-optimized C ABI
                         |
                 public Go SDK
                         |
              shared Go implementation
                         |
             narrow platform capabilities
```

The Go CLI imports the public Go SDK directly. It never calls into C and back into Go.

## Normative contract

The cross-language contract consists of:

- the public declarations and comments in `include/Wendy`;
- the SDK request, response, and event schemas in `proto/wendy/sdk/v1`;
- the inherited behavior in `Documentation/CContract`; and
- the projection rules in `Documentation/BindingPatterns`.

Generated HTML, protobuf code, and API manifests are derived artifacts.

## Reference workflow

```sh
make generate                 # regenerate Go protobuf values
make documentation-manifest   # create descriptor, API JSON, and model reference
make documentation            # additionally render Doxygen HTML
make compare-clean            # compare the draft snapshot with a fresh build
make check                     # generation, Go tests, and C/C++ header checks
```

The documentation manifest is designed to become the common input for reference pages, compatibility reports, and binding generators.
