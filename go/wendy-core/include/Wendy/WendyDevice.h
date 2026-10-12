#ifndef WENDY_DEVICE_H
#define WENDY_DEVICE_H

#include <Wendy/WendyRuntime.h>

#ifdef __cplusplus
extern "C" {
#endif

/** @defgroup WendyDevice Device
 *  Representative device operations used to prove binding mechanics.
 *  @{ */

/** Adds one ownership reference and returns the same device. */
WENDY_API WendyDevice *WENDY_CALL WendyDeviceRetain(WendyDevice const *device);

/** Releases one ownership reference. Passing NULL has no effect. */
WENDY_API void WENDY_CALL WendyDeviceRelease(WendyDevice const *device);

/**
 * Requests current information from a connected device.
 *
 * The request bytes encode `wendy.sdk.v1.GetDeviceInfoRequest`. Successful
 * completion bytes encode `wendy.sdk.v1.DeviceInfo`.
 *
 * Binding pattern: async-unary.
 * Ownership: inputs are borrowed; the operation and callback result are owned.
 * Threading: safe for concurrent calls; completion runs on a Wendy worker thread.
 * Cancellation: completes exactly once with `WendyErrorCancelled`.
 * Availability: 0.1.0-draft.
 */
WENDY_API WendyOperation *WENDY_CALL WendyDeviceGetInfo(
    WendyDevice const *device,
    WendyDataView request,
    WendyCompletion completion,
    void *context
);

/** Callbacks for an ordered, demand-driven stream. */
typedef struct WendyStreamCallbacks {
    uint32_t structSize;
    void (WENDY_CALL *onItem)(void *context, WendyData *item);
    void (WENDY_CALL *onComplete)(void *context, WendyError *error);
} WendyStreamCallbacks;

/**
 * Watches complete state snapshots for a connected device.
 *
 * Request bytes encode `wendy.sdk.v1.WatchDeviceStateRequest`; every item
 * encodes `wendy.sdk.v1.DeviceState`. Item callbacks are serialized and consume
 * one unit of demand. `onComplete` runs exactly once; its error is NULL after
 * normal completion.
 *
 * Binding pattern: demand-driven-stream.
 * Ownership: inputs are borrowed; the stream and callback items are owned.
 * Threading: item callbacks are serialized on a Wendy worker thread.
 * Cancellation: completes exactly once with `WendyErrorCancelled`.
 * Availability: 0.1.0-draft.
 */
WENDY_API WendyStream *WENDY_CALL WendyDeviceWatchState(
    WendyDevice const *device,
    WendyDataView request,
    WendyStreamCallbacks const *callbacks,
    void *context
);

/** @} */

#ifdef __cplusplus
}
#endif

#endif
