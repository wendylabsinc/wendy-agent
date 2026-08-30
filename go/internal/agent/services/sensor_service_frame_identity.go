package services

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	appspbv1 "github.com/wendylabsinc/wendy/go/proto/gen/appspb/v1"
)

// frameIdentityProvider is the optional half of a sensorProvider that also
// serves the two-plane data path's control plane. A provider that has no
// v4l2loopback data path simply does not implement it, and its sources report
// no node.
type frameIdentityProvider interface {
	// SubscribeFrameIdentity streams the identity of frames written to the
	// source's node. It must NOT create a data path as a side effect.
	SubscribeFrameIdentity(ctx context.Context, sourceID string) (frameIdentitySubscription, error)
	// TwoPlaneNodePath reports the node carrying the source's frames, if any.
	TwoPlaneNodePath(sourceID string) (string, bool)
}

// frameIdentityProviderFor resolves the provider that can stream a source's
// frame identities, or nil if none can.
func (s *SensorService) frameIdentityProviderFor(sourceID string) frameIdentityProvider {
	for _, provider := range s.providerSnapshot() {
		if !provider.SupportsSensorSource(sourceID) {
			continue
		}
		if fip, ok := provider.(frameIdentityProvider); ok {
			return fip
		}
	}
	return nil
}

// SubscribeFrameIdentity streams frame identities for the requested sources.
//
// This is the control plane of the two-plane camera path: it carries identity
// only, never pixels, so an app reading the v4l2loopback node with ordinary
// tooling can name the frames it scored. See the FrameIdentity proto comment for
// the join and for what dropped frames look like on each plane.
//
// # Why deliveries are not teed into the model-input ledger
//
// Subscribe records every sample it hands to a model, because handing a sample
// over the stream IS the delivery. That is not true here. Publishing an identity
// says the agent wrote a frame to the node; it does not say the app read it. An
// app that fell behind has frames fast-forwarded past it by v4l2loopback and
// never sees them, while the identity for each was published all the same.
// Recording those as model inputs would make the episode assert a delivery that
// may not have happened, which is exactly the kind of record that is worse than
// no record at all. An app that wants its consumption in the ledger must report
// what it actually read, which it can do precisely because this stream gives it
// the sample_id for each frame.
func (s *SensorService) SubscribeFrameIdentity(req *appspbv1.FrameIdentitySubscribeRequest, stream appspbv1.SensorService_SubscribeFrameIdentityServer) error {
	sourceIDs, err := normalizeSubscribeSources(req.GetSourceIds())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	identities := make(chan FrameIdentitySample, sensorFanInBuffer*len(sourceIDs))
	var wg sync.WaitGroup
	fatal := make(chan error, len(sourceIDs))

	for _, sourceID := range sourceIDs {
		if !s.permitted(sourceID) {
			cancel()
			wg.Wait()
			return status.Errorf(codes.PermissionDenied, "this app's sensor-read entitlement does not list source %q", sourceID)
		}
		provider := s.frameIdentityProviderFor(sourceID)
		if provider == nil {
			cancel()
			wg.Wait()
			return status.Errorf(codes.NotFound, "source %q is not available to model subscribers", sourceID)
		}
		subscription, err := provider.SubscribeFrameIdentity(ctx, sourceID)
		if err != nil {
			cancel()
			wg.Wait()
			return err
		}
		wg.Add(1)
		go func(sourceID string, subscription frameIdentitySubscription) {
			defer wg.Done()
			defer subscription.Close()
			for {
				identity, err := subscription.Next(ctx)
				if err != nil {
					if ctx.Err() == nil {
						fatal <- fmt.Errorf("source %s: %w", sourceID, err)
					}
					return
				}
				identity.SourceID = sourceID
				select {
				case identities <- identity:
				case <-ctx.Done():
					return
				}
				// Unlike Subscribe there is no non-blocking drop here. An
				// identity is a few dozen bytes, and losing one costs the app the
				// ability to name a frame it may have scored. The pump already
				// drops for a subscriber that falls badly behind
				// (hubLoopbackPump.publishIdentity), which is the bound that
				// keeps memory finite; adding a second silent drop on top of it
				// would only make the loss harder to explain.
			}
		}(sourceID, subscription)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(identities); close(done) }()
	defer func() { cancel(); <-done }()

	for {
		select {
		case err := <-fatal:
			return status.Error(codes.Unavailable, err.Error())
		case <-ctx.Done():
			return nil
		case identity, ok := <-identities:
			if !ok {
				select {
				case err := <-fatal:
					return status.Error(codes.Unavailable, err.Error())
				default:
					return nil
				}
			}
			if err := stream.Send(frameIdentityMessage(identity)); err != nil {
				return err
			}
		}
	}
}

// frameIdentityMessage converts one identity to the wire form.
func frameIdentityMessage(identity FrameIdentitySample) *appspbv1.FrameIdentity {
	return &appspbv1.FrameIdentity{
		SourceId:                  identity.SourceID,
		SampleId:                  identity.SampleID,
		BoottimeNanos:             identity.BootNanos,
		TimestampUncertaintyNanos: identity.UncertaintyNanos,
		LoopbackSequence:          identity.LoopbackSequence,
		DroppedBefore:             identity.DroppedBefore,
		BootId:                    data.BootID(),
		NodePath:                  identity.NodePath,
	}
}

// loopbackNodePathFor reports the data path node for a source, for Sources to
// advertise. An empty result is the normal case and not an error.
func (s *SensorService) loopbackNodePathFor(sourceID string) string {
	provider := s.frameIdentityProviderFor(sourceID)
	if provider == nil {
		return ""
	}
	path, ok := provider.TwoPlaneNodePath(sourceID)
	if !ok {
		return ""
	}
	return path
}
