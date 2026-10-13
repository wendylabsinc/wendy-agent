#include <WendyCore/WendyCore.h>
#include <stdio.h>

int main(void) {
    int32_t const result = WendyHelloWorld();
    printf("WendyHelloWorld() = %d\n", result);
    return result == 42 ? 0 : 1;
}
