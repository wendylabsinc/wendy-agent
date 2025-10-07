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

#endif /* CLinuxVideo_h */ 