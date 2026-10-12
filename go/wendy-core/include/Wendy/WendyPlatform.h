#ifndef WENDY_PLATFORM_H
#define WENDY_PLATFORM_H

#include <Wendy/WendyBase.h>

#ifdef __cplusplus
extern "C" {
#endif

/** @defgroup WendyPlatform Platform capabilities
 *  Versioned host capabilities supplied when a platform needs native behavior.
 *  @{ */

/**
 * Versioned platform capability table.
 *
 * The initial draft deliberately defines no security, networking, or device
 * capability. Future additive revisions append narrowly scoped capability
 * table pointers after `context`; unsupported capabilities remain NULL.
 */
typedef struct WendyPlatform {
    uint32_t structSize;
    uint32_t version;
    void *context;
} WendyPlatform;

/** @} */

#ifdef __cplusplus
}
#endif

#endif
