#ifndef WENDY_RUNTIME_H
#define WENDY_RUNTIME_H

#include <Wendy/WendyPlatform.h>

#ifdef __cplusplus
extern "C" {
#endif

/** @defgroup WendyRuntime Runtime
 *  Creating and managing an isolated Wendy SDK runtime.
 *  @{ */

/** Options for creating a runtime. Zero-initialize before use. */
typedef struct WendyRuntimeOptions {
    uint32_t structSize;
    uint32_t abiMajor;
    uint32_t abiMinor;
    WendyDataView configuration;
    WendyPlatform const *platform;
} WendyRuntimeOptions;

/**
 * Creates an isolated Wendy SDK runtime.
 *
 * `options` is borrowed and copied before this function returns. On failure,
 * `error` receives an owned error when non-NULL. The returned runtime is owned
 * by the caller.
 *
 * Binding pattern: owned-object.
 * Availability: 0.1.0-draft.
 */
WENDY_API WendyRuntime *WENDY_CALL WendyRuntimeCreate(
    WendyRuntimeOptions const *options,
    WendyError **error
);

/** Adds one ownership reference and returns the same runtime. */
WENDY_API WendyRuntime *WENDY_CALL WendyRuntimeRetain(WendyRuntime const *runtime);

/** Releases one ownership reference. Passing NULL has no effect. */
WENDY_API void WENDY_CALL WendyRuntimeRelease(WendyRuntime const *runtime);

/** @} */

#ifdef __cplusplus
}
#endif

#endif
