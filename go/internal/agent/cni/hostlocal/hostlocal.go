// Copyright 2015 CNI authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Vendored from github.com/containernetworking/plugins v1.9.1,
// plugins/ipam/host-local/main.go (+ dns.go in this same directory).
// Upstream ships this as `package main` (a standalone CNI plugin binary),
// which cannot be imported directly, so the source is copied here with the
// package renamed and its `func main()` removed. The command functions
// (renamed cmdAdd/cmdCheck/cmdDel -> CmdAdd/CmdCheck/CmdDel) are exported so
// cmd/wendy-agent can dispatch to them directly: the vendored bridge plugin
// (internal/agent/cni/bridge) delegates IPAM by exec'ing "host-local" from
// its CNI_PATH, which the agent points at a directory of symlinks back to
// itself (see internal/agent/containerd/cni.go), so this delegated exec also
// resolves into the agent binary instead of a third-party binary.
//
// Keep this file in sync with upstream on CNI plugin version bumps: re-copy
// plugins/ipam/host-local/main.go and dns.go, re-apply the package rename +
// main() removal + exports below, and diff against the previous vendored
// copy to review upstream behavioural changes.

package hostlocal

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/plugins/plugins/ipam/host-local/backend/allocator"
	"github.com/containernetworking/plugins/plugins/ipam/host-local/backend/disk"
)

func CmdCheck(args *skel.CmdArgs) error {
	ipamConf, _, err := allocator.LoadIPAMConfig(args.StdinData, args.Args)
	if err != nil {
		return err
	}

	// Look to see if there is at least one IP address allocated to the container
	// in the data dir, irrespective of what that address actually is
	store, err := disk.New(ipamConf.Name, ipamConf.DataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	containerIPFound := store.FindByID(args.ContainerID, args.IfName)
	if !containerIPFound {
		return fmt.Errorf("host-local: Failed to find address added by container %v", args.ContainerID)
	}

	return nil
}

// Allocate runs the host-local IPAM allocation logic and returns the result
// directly, WITHOUT printing it to stdout — unlike CmdAdd (which prints,
// per the CNI plugin contract of being invoked as a standalone subprocess).
// The bridge plugin (internal/agent/cni/bridge) calls this in-process instead
// of exec'ing host-local as a second self-exec: the previous double self-exec
// (agent -> bridge -> host-local, resolved through the same CNI_PATH
// symlinks) captured host-local's subprocess exit status and stdout via
// containernetworking/cni's RawExec, which on any nonzero exit status
// unmarshals stdout into a CNI error struct — even when stdout holds a
// perfectly valid SUCCESS result with no "code"/"msg" fields, which decodes
// silently into a zero-valued {"code":0,"msg":""} error. This masked the
// real failure and made every CNI ADD look identical regardless of cause.
// Calling this Go function directly removes that subprocess/stdout boundary
// entirely — there is no exit status or output stream to misinterpret.
func Allocate(args *skel.CmdArgs) (*current.Result, string, error) {
	ipamConf, confVersion, err := allocator.LoadIPAMConfig(args.StdinData, args.Args)
	if err != nil {
		return nil, "", err
	}

	result := &current.Result{CNIVersion: current.ImplementedSpecVersion}

	if ipamConf.ResolvConf != "" {
		dns, err := parseResolvConf(ipamConf.ResolvConf)
		if err != nil {
			return nil, "", err
		}
		result.DNS = *dns
	}

	store, err := disk.New(ipamConf.Name, ipamConf.DataDir)
	if err != nil {
		return nil, "", err
	}
	defer store.Close()

	// Keep the allocators we used, so we can release all IPs if an error
	// occurs after we start allocating
	allocs := []*allocator.IPAllocator{}

	// Store all requested IPs in a map, so we can easily remove ones we use
	// and error if some remain
	requestedIPs := map[string]net.IP{} // net.IP cannot be a key

	for _, ip := range ipamConf.IPArgs {
		requestedIPs[ip.String()] = ip
	}

	for idx, rangeset := range ipamConf.Ranges {
		allocator := allocator.NewIPAllocator(&rangeset, store, idx)

		// Check to see if there are any custom IPs requested in this range.
		var requestedIP net.IP
		for k, ip := range requestedIPs {
			if rangeset.Contains(ip) {
				requestedIP = ip
				delete(requestedIPs, k)
				break
			}
		}

		ipConf, err := allocator.Get(args.ContainerID, args.IfName, requestedIP)
		if err != nil {
			// Deallocate all already allocated IPs
			for _, alloc := range allocs {
				_ = alloc.Release(args.ContainerID, args.IfName)
			}
			return nil, "", fmt.Errorf("failed to allocate for range %d: %v", idx, err)
		}

		allocs = append(allocs, allocator)

		result.IPs = append(result.IPs, ipConf)
	}

	// If an IP was requested that wasn't fulfilled, fail
	if len(requestedIPs) != 0 {
		for _, alloc := range allocs {
			_ = alloc.Release(args.ContainerID, args.IfName)
		}
		errstr := "failed to allocate all requested IPs:"
		for _, ip := range requestedIPs {
			errstr = errstr + " " + ip.String()
		}
		return nil, "", errors.New(errstr)
	}

	result.Routes = ipamConf.Routes

	return result, confVersion, nil
}

func CmdAdd(args *skel.CmdArgs) error {
	result, confVersion, err := Allocate(args)
	if err != nil {
		return err
	}
	return types.PrintResult(result, confVersion)
}

func CmdDel(args *skel.CmdArgs) error {
	ipamConf, _, err := allocator.LoadIPAMConfig(args.StdinData, args.Args)
	if err != nil {
		return err
	}

	store, err := disk.New(ipamConf.Name, ipamConf.DataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	// Loop through all ranges, releasing all IPs, even if an error occurs
	var errs []string
	for idx, rangeset := range ipamConf.Ranges {
		ipAllocator := allocator.NewIPAllocator(&rangeset, store, idx)

		err := ipAllocator.Release(args.ContainerID, args.IfName)
		if err != nil {
			errs = append(errs, err.Error())
		}
	}

	if errs != nil {
		return errors.New(strings.Join(errs, ";"))
	}
	return nil
}
