package liteclient

import (
	"testing"
	"time"

	pb "github.com/wendylabsinc/wendy/go/proto/gen/litepb"
)

func TestGetDeviceIdentityPreservesEnrollmentID(t *testing.T) {
	for _, enrolled := range []string{"", "lite-441bf6804824", "00000000-0000-4000-8000-000000000004"} {
		t.Run(enrolled, func(t *testing.T) {
			client, peer := connectedOverPipe(t)
			server := newDirectLink(peer)
			done := make(chan error, 1)
			go func() {
				msg, err := server.recv(time.Second)
				if err != nil {
					done <- err
					return
				}
				cmd := msg.GetCommand()
				if cmd.GetGetDeviceIdentity() == nil {
					t.Error("expected identity request")
				}
				done <- server.send(&pb.WendyComMessage{Msg: &pb.WendyComMessage_Response{Response: &pb.WendyComResponse{
					RequestId: cmd.RequestId,
					Result:    pb.WendyComResult_WENDY_COM_RESULT_OK,
					Data: &pb.WendyComResponse_DeviceIdentity{DeviceIdentity: &pb.WendyComDeviceIdentity{
						Id: "441bf6804824", Name: "camera", DisplayName: "Camera", EnrolledDeviceId: enrolled,
					}},
				}}})
			}()
			identity, err := client.GetDeviceIdentity(time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if identity.ID != "441bf6804824" || identity.Name != "camera" || identity.DisplayName != "Camera" || identity.EnrolledDeviceID != enrolled {
				t.Fatalf("identity fields lost: %+v", identity)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
