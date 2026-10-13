#ifndef WENDY_CORE_H
#define WENDY_CORE_H

#include <stdint.h>

#if defined(_WIN32)
#  define WENDY_CORE_CALL __cdecl
#else
#  define WENDY_CORE_CALL
#endif

#ifdef __cplusplus
extern "C" {
#endif

/**
 * Returns 42 from the shared Go engine.
 *
 * This is the only operation in the initial runnable Core contract. It is
 * synchronous, thread-safe, and requires no platform capability. It returns a
 * scalar without transferring resources; runtime initialization is separate.
 * There are no input pointers, owned objects, callbacks, or error conditions.
 *
 * Binding pattern: scalar-unary.
 * Ownership: returns a value; no resources are transferred.
 * Threading: safe for concurrent calls.
 * Availability: 0.1.0.
 */
int32_t WENDY_CORE_CALL WendyHelloWorld(void);

#ifdef __cplusplus
}
#endif

#endif
