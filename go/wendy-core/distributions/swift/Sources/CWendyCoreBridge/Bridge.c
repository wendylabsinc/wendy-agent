#include "Bridge.h"
#include "native_loader.h"

WendyCoreLibrary *WendyCoreBridgeOpen(char const *path) {
    return WendyCoreLibraryOpen(path);
}

int32_t WendyCoreBridgeHelloWorld(WendyCoreLibrary const *library) {
    return library->hello();
}
