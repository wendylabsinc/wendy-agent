#include "dnssd_darwin.h"
#include "_cgo_export.h"

#include <arpa/inet.h>

// mDNSResponder invokes these on the goroutine that called
// DNSServiceProcessResult, so forwarding straight into Go is safe. Strings
// borrowed from the callback are only valid for its duration; the Go side
// copies them before returning.

static void browse_reply(DNSServiceRef ref, DNSServiceFlags flags,
                         uint32_t ifIndex, DNSServiceErrorType err,
                         const char *name, const char *regtype,
                         const char *domain, void *ctx) {
  (void)ref;
  (void)regtype;
  wendyDNSSDBrowseReply((uintptr_t)ctx, (uint32_t)flags, ifIndex, (int32_t)err,
                        (char *)name, (char *)domain);
}

DNSServiceErrorType wendy_dnssd_browse(DNSServiceRef *ref, const char *regtype,
                                       uintptr_t handle) {
  return DNSServiceBrowse(ref, 0, kDNSServiceInterfaceIndexAny, regtype, NULL,
                          browse_reply, (void *)handle);
}

static void resolve_reply(DNSServiceRef ref, DNSServiceFlags flags,
                          uint32_t ifIndex, DNSServiceErrorType err,
                          const char *fullname, const char *hosttarget,
                          uint16_t port, uint16_t txtLen,
                          const unsigned char *txt, void *ctx) {
  (void)ref;
  (void)flags;
  (void)ifIndex;
  (void)fullname;
  // port arrives in network byte order.
  wendyDNSSDResolveReply((uintptr_t)ctx, (int32_t)err, (char *)hosttarget,
                         ntohs(port), (void *)txt, txtLen);
}

DNSServiceErrorType wendy_dnssd_resolve(DNSServiceRef *ref, const char *name,
                                        const char *regtype, const char *domain,
                                        uintptr_t handle) {
  // kDNSServiceInterfaceIndexAny matches the previous `dns-sd -L` behaviour,
  // which did not pin the resolve to the interface the browse replied on.
  return DNSServiceResolve(ref, 0, kDNSServiceInterfaceIndexAny, name, regtype,
                           domain, resolve_reply, (void *)handle);
}
