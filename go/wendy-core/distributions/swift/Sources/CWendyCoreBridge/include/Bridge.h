#include <stdint.h>

typedef struct WendyCoreLibrary WendyCoreLibrary;
WendyCoreLibrary *WendyCoreBridgeOpen(char const *path);
int32_t WendyCoreBridgeHelloWorld(WendyCoreLibrary const *library);
