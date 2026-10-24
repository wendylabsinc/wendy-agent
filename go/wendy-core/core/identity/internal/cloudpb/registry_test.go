package cloudpb

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoregistry"
)

func TestServiceDescriptorsStayPrivate(t *testing.T) {
	for _, path := range []string{"wendy/options.proto", "wendycloud/v2/signed_request.proto", "wendycloud/v2/organizations.proto"} {
		if _, err := privateFiles.FindFileByPath(path); err != nil {
			t.Fatalf("private descriptor %s: %v", path, err)
		}
		if _, err := protoregistry.GlobalFiles.FindFileByPath(path); err != protoregistry.NotFound {
			t.Fatalf("application descriptor registered globally: %s (%v)", path, err)
		}
	}
	if _, err := protoregistry.GlobalTypes.FindMessageByName("wendycloud.v2.Organization"); err != protoregistry.NotFound {
		t.Fatalf("application type registered globally: %v", err)
	}
	method := File_wendycloud_v2_organizations_proto.Services().ByName("OrganizationService").Methods().ByName("UpdateOrganization")
	if signed, ok := proto.GetExtension(method.Options(), E_SignedRequest).(bool); !ok || !signed {
		t.Fatal("private generation lost imported method options")
	}
}
