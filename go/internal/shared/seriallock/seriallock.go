package seriallock

import "errors"

// ErrLocked indicates Acquire found the device already locked by another
// process (e.g. idf.py monitor, esptool, or another WendyOS tool instance).
// Other Acquire failures — the device doesn't exist yet, permission
// denied, ... — do not satisfy errors.Is(err, ErrLocked); callers should
// treat those as their usual failure mode (retryable, permission hint, ...)
// rather than as "port busy".
var ErrLocked = errors.New("serial port is in use by another program")
