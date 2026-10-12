#include <Wendy/Wendy.h>

static void infoCompleted(void *context, WendyData *response, WendyError *error) {
    (void)context;

    if (response != NULL) {
        WendyDataView const view = WendyDataGetView(response);
        (void)view;
        WendyDataRelease(response);
    }
    if (error != NULL) {
        WendyError const *letError = error;
        (void)WendyErrorGetCode(letError);
        WendyErrorRelease(error);
    }
}

static void compileOnly(WendyDevice const *device) {
    WendyDataView const request = {0};
    WendyOperation *operation = WendyDeviceGetInfo(
        device,
        request,
        infoCompleted,
        NULL
    );
    WendyOperationCancel(operation);
    WendyOperationRelease(operation);
}

int main(void) {
    (void)compileOnly;
    return 0;
}
