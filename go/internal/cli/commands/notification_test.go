package commands

import (
	"strings"
	"testing"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

func validNotificationSendOptions() notificationSendOptions {
	return notificationSendOptions{
		organization: 42,
		user:         "user-1",
		title:        "Temperature warning",
		body:         "Device temperature is high.",
		severity:     "warning",
		deepLink:     "wendy://organizations/42/notifications/7",
		sourceID:     "sensor-reading-7",
		metadataJSON: `{"temperature":91.5}`,
	}
}

func TestBuildNotificationSendRequest(t *testing.T) {
	request, err := buildNotificationSendRequest(validNotificationSendOptions())
	if err != nil {
		t.Fatal(err)
	}
	if request.GetOrganizationId() != 42 || request.GetAudience().GetUserId() != "user-1" {
		t.Fatalf("unexpected organization/audience: %+v", request)
	}
	if request.GetSeverity() != cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_WARNING {
		t.Fatalf("severity = %v", request.GetSeverity())
	}
	if request.GetSourceId() != "sensor-reading-7" {
		t.Fatalf("source_id = %q", request.GetSourceId())
	}
	if got := request.GetMetadata().GetFields()["temperature"].GetNumberValue(); got != 91.5 {
		t.Fatalf("metadata temperature = %v", got)
	}
}

func TestBuildNotificationSendRequestRequiresOneAudience(t *testing.T) {
	options := validNotificationSendOptions()
	options.team = 9
	_, err := buildNotificationSendRequest(options)
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("error = %v", err)
	}

	options = validNotificationSendOptions()
	options.user = ""
	_, err = buildNotificationSendRequest(options)
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildNotificationSendRequestSupportsTeamAndRole(t *testing.T) {
	team := validNotificationSendOptions()
	team.user = ""
	team.team = 9
	request, err := buildNotificationSendRequest(team)
	if err != nil {
		t.Fatal(err)
	}
	if request.GetAudience().GetOrgTeamId() != 9 {
		t.Fatalf("team = %d", request.GetAudience().GetOrgTeamId())
	}

	role := validNotificationSendOptions()
	role.user = ""
	role.role = "billing-manager"
	request, err = buildNotificationSendRequest(role)
	if err != nil {
		t.Fatal(err)
	}
	if request.GetAudience().GetOrganizationRole() != cloudpb.OrganizationRole_ORGANIZATION_ROLE_BILLING_MANAGER {
		t.Fatalf("role = %v", request.GetAudience().GetOrganizationRole())
	}
}

func TestBuildNotificationSendRequestGeneratesSourceID(t *testing.T) {
	options := validNotificationSendOptions()
	options.sourceID = ""
	request, err := buildNotificationSendRequest(options)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(request.GetSourceId(), "wendy-cli-") || len(request.GetSourceId()) != len("wendy-cli-")+32 {
		t.Fatalf("source_id = %q", request.GetSourceId())
	}
}

func TestBuildNotificationSendRequestRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*notificationSendOptions)
		want   string
	}{
		{"organization", func(o *notificationSendOptions) { o.organization = 0 }, "organization"},
		{"title", func(o *notificationSendOptions) { o.title = " " }, "title"},
		{"body", func(o *notificationSendOptions) { o.body = "" }, "body"},
		{"deep link", func(o *notificationSendOptions) { o.deepLink = "https://example.com" }, "wendy://"},
		{"severity", func(o *notificationSendOptions) { o.severity = "debug" }, "severity"},
		{"metadata array", func(o *notificationSendOptions) { o.metadataJSON = "[]" }, "JSON object"},
		{"metadata null", func(o *notificationSendOptions) { o.metadataJSON = "null" }, "JSON object"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := validNotificationSendOptions()
			test.mutate(&options)
			_, err := buildNotificationSendRequest(options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestNotificationCreateAlias(t *testing.T) {
	cmd := newNotificationSendCmd()
	if len(cmd.Aliases) != 1 || cmd.Aliases[0] != "create" {
		t.Fatalf("aliases = %v", cmd.Aliases)
	}
}
