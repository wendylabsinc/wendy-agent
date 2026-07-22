#ifndef CLinuxVideo_h
#define CLinuxVideo_h

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <fcntl.h>
#include <unistd.h>
#include <sys/ioctl.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <linux/videodev2.h>

enum io_method {
        IO_METHOD_READ,
        IO_METHOD_MMAP,
        IO_METHOD_USERPTR,
};

static const unsigned int WENDY_VIDIOC_QUERYBUF = VIDIOC_QUERYBUF;
static const unsigned int WENDY_VIDIOC_QBUF = VIDIOC_QBUF;
static const unsigned int WENDY_VIDIOC_DQBUF = VIDIOC_DQBUF;
static const unsigned int WENDY_VIDIOC_STREAMON = VIDIOC_STREAMON;
static const unsigned int WENDY_VIDIOC_STREAMOFF = VIDIOC_STREAMOFF;
static const unsigned int WENDY_VIDIOC_S_FMT = VIDIOC_S_FMT;
static const unsigned int WENDY_VIDIOC_REQBUFS = VIDIOC_REQBUFS;
static const unsigned int WENDY_VIDIOC_QUERYCAP = VIDIOC_QUERYCAP;

/// Sets stdout to unbuffered so `wendy device logs`/`wendy run` see log lines
/// as they happen rather than only on a buffer flush. Done here in C (rather
/// than a Swift `setvbuf(stdout, ...)` call) because Swift 6's strict
/// concurrency checking flags any Swift-level reference to the C global
/// `stdout` as "not concurrency-safe" — mirrors wendy-app-sdk's
/// wendy_kms_flush_stdout() shim, which solves the identical problem the
/// same way.
static inline void wendy_camserver_unbuffer_stdout(void) {
    setvbuf(stdout, NULL, _IONBF, 0);
}

#endif /* CLinuxVideo_h */
