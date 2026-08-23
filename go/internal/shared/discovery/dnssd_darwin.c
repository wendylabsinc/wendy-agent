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
                                        uint32_t interface_index,
                                        uintptr_t handle) {
  return DNSServiceResolve(ref, 0, interface_index, name, regtype,
                           domain, resolve_reply, (void *)handle);
}

static void addr_reply(DNSServiceRef ref, DNSServiceFlags flags,
                       uint32_t ifIndex, DNSServiceErrorType err,
                       const char *hostname, const struct sockaddr *address,
                       uint32_t ttl, void *ctx) {
  (void)ref;
  (void)hostname;
  (void)ttl;
  char text[INET6_ADDRSTRLEN] = {0};
  if (err == kDNSServiceErr_NoError && address != NULL) {
    if (address->sa_family == AF_INET) {
      inet_ntop(AF_INET, &((const struct sockaddr_in *)address)->sin_addr,
                text, sizeof(text));
    } else if (address->sa_family == AF_INET6) {
      inet_ntop(AF_INET6, &((const struct sockaddr_in6 *)address)->sin6_addr,
                text, sizeof(text));
    }
  }
  wendyDNSSDAddrReply((uintptr_t)ctx, (uint32_t)flags, ifIndex, (int32_t)err,
                      text);
}

DNSServiceErrorType wendy_dnssd_getaddrinfo(DNSServiceRef *ref,
                                            const char *hostname,
                                            uint32_t interface_index,
                                            uintptr_t handle) {
  return DNSServiceGetAddrInfo(ref, 0, interface_index,
                               kDNSServiceProtocol_IPv4 |
                                   kDNSServiceProtocol_IPv6,
                               hostname, addr_reply, (void *)handle);
}
