#ifndef WENDY_BASE_H
#define WENDY_BASE_H

#include <stddef.h>
#include <stdint.h>

#if defined(_WIN32)
#  if defined(WENDY_BUILDING)
#    define WENDY_API __declspec(dllexport)
#  else
#    define WENDY_API __declspec(dllimport)
#  endif
#  define WENDY_CALL __cdecl
#else
#  define WENDY_API __attribute__((visibility("default")))
#  define WENDY_CALL
#endif

#ifdef __cplusplus
extern "C" {
#endif

/** @defgroup WendyBase Base types
 *  Types and lifetime operations shared by every Wendy SDK module.
 *  @{ */

typedef struct WendyRuntime WendyRuntime;
typedef struct WendyDevice WendyDevice;
typedef struct WendyOperation WendyOperation;
typedef struct WendyStream WendyStream;
typedef struct WendyData WendyData;
typedef struct WendyError WendyError;

/** A borrowed byte range. The owner determines its lifetime. */
typedef struct WendyDataView {
    uint8_t const *bytes;
    size_t length;
} WendyDataView;

/** Stable error categories shared by every language projection. */
typedef int32_t WendyErrorCode;
enum {
    WendyErrorCancelled = 1,
    WendyErrorTimeout = 2,
    WendyErrorUnavailable = 3,
    WendyErrorUnauthenticated = 4,
    WendyErrorPermissionDenied = 5,
    WendyErrorNotFound = 6,
    WendyErrorConflict = 7,
    WendyErrorInvalidArgument = 8,
    WendyErrorInvalidResponse = 9,
    WendyErrorPlatform = 10,
    WendyErrorInternal = 11
};

/**
 * Completion callback for an asynchronous unary operation.
 *
 * Exactly one of `response` and `error` is non-NULL. The callback receives
 * ownership of that object and must release it. A completion is invoked exactly
 * once and never before the initiating function returns.
 */
typedef void (WENDY_CALL *WendyCompletion)(
    void *context,
    WendyData *response,
    WendyError *error
);

/** Returns the bridge ABI major version. */
WENDY_API uint32_t WENDY_CALL WendyABIVersionMajor(void);

/** Returns the bridge ABI minor version. */
WENDY_API uint32_t WENDY_CALL WendyABIVersionMinor(void);

/** Returns a borrowed view valid while `data` remains alive. */
WENDY_API WendyDataView WENDY_CALL WendyDataGetView(WendyData const *data);

/** Adds one ownership reference and returns the same data object. */
WENDY_API WendyData *WENDY_CALL WendyDataRetain(WendyData const *data);

/** Releases one ownership reference. Passing NULL has no effect. */
WENDY_API void WENDY_CALL WendyDataRelease(WendyData const *data);

/** Returns the stable category for `error`. */
WENDY_API WendyErrorCode WENDY_CALL WendyErrorGetCode(WendyError const *error);

/** Returns the human-readable UTF-8 message as a borrowed view. */
WENDY_API WendyDataView WENDY_CALL WendyErrorGetMessage(WendyError const *error);

/** Adds one ownership reference and returns the same error object. */
WENDY_API WendyError *WENDY_CALL WendyErrorRetain(WendyError const *error);

/** Releases one ownership reference. Passing NULL has no effect. */
WENDY_API void WENDY_CALL WendyErrorRelease(WendyError const *error);

/** Requests cancellation. Cancellation is idempotent. */
WENDY_API void WENDY_CALL WendyOperationCancel(WendyOperation const *operation);

/** Adds one ownership reference and returns the same operation. */
WENDY_API WendyOperation *WENDY_CALL WendyOperationRetain(WendyOperation const *operation);

/** Releases one ownership reference without implicitly cancelling. */
WENDY_API void WENDY_CALL WendyOperationRelease(WendyOperation const *operation);

/** Adds demand to a stream. Items are never delivered without demand. */
WENDY_API void WENDY_CALL WendyStreamRequest(WendyStream const *stream, uint64_t demand);

/** Requests stream cancellation. Cancellation is idempotent. */
WENDY_API void WENDY_CALL WendyStreamCancel(WendyStream const *stream);

/** Adds one ownership reference and returns the same stream. */
WENDY_API WendyStream *WENDY_CALL WendyStreamRetain(WendyStream const *stream);

/** Releases one ownership reference without implicitly cancelling. */
WENDY_API void WENDY_CALL WendyStreamRelease(WendyStream const *stream);

/** @} */

#ifdef __cplusplus
}
#endif

#endif
