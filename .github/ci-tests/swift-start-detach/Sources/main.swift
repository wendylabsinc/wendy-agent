#if canImport(Glibc)
import Glibc
#elseif canImport(Musl)
import Musl
#endif

// Long-running so a successful `apps start` is a stable RUNNING signal for the
// integration test that verifies a detached start returns only once the
// container is actually running.
print("swift-start-detach: running")
fflush(nil)
while true {
    sleep(10)
}
