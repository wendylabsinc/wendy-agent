package commands

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

func validNotificationSendOptions() notificationSendOptions {
	return notificationSendOptions{
		organization: 42,
		users:        []string{"user-1"},
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
	if request.GetOrganizationId() != 42 || !slices.Equal(request.GetAudience().GetUserIds(), []string{"user-1"}) {
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

func TestBuildNotificationSendRequestNormalizesAndUnionsAudience(t *testing.T) {
	options := validNotificationSendOptions()
	options.users = []string{" user-1 ", "user-1", "user-2"}
	options.teams = []int32{9, 9, 10}
	options.roles = []string{"billing-manager", "billing_manager", "viewer"}

	request, err := buildNotificationSendRequest(options)
	if err != nil {
		t.Fatal(err)
	}
	audience := request.GetAudience()
	if !slices.Equal(audience.GetUserIds(), []string{"user-1", "user-2"}) {
		t.Fatalf("user_ids = %v", audience.GetUserIds())
	}
	if !slices.Equal(audience.GetTeamIds(), []int32{9, 10}) {
		t.Fatalf("team_ids = %v", audience.GetTeamIds())
	}
	wantRoles := []cloudpb.OrganizationRole{
		cloudpb.OrganizationRole_ORGANIZATION_ROLE_BILLING_MANAGER,
		cloudpb.OrganizationRole_ORGANIZATION_ROLE_VIEWER,
	}
	if !slices.Equal(audience.GetRoles(), wantRoles) {
		t.Fatalf("roles = %v", audience.GetRoles())
	}
}

func TestBuildNotificationSendRequestRequiresAudience(t *testing.T) {
	options := validNotificationSendOptions()
	options.users = nil
	_, err := buildNotificationSendRequest(options)
	if err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildNotificationSendRequestBoundsUniqueAudienceSelectors(t *testing.T) {
	options := validNotificationSendOptions()
	options.users = make([]string, maxNotificationAudienceSelectors-5)
	for index := range options.users {
		options.users[index] = fmt.Sprintf("user-%d", index)
	}
	options.teams = []int32{7, 8}
	options.roles = []string{"owner", "admin", "viewer"}
	if _, err := buildNotificationSendRequest(options); err != nil {
		t.Fatalf("100 selectors across categories rejected: %v", err)
	}

	options.teams = append(options.teams, 9)
	if _, err := buildNotificationSendRequest(options); err == nil || !strings.Contains(err.Error(), "at most 100") {
		t.Fatalf("101-selector error = %v", err)
	}

	options.users = make([]string, maxNotificationAudienceSelectors+1)
	for index := range options.users {
		options.users[index] = "same-user"
	}
	options.teams = nil
	options.roles = nil
	request, err := buildNotificationSendRequest(options)
	if err != nil {
		t.Fatalf("duplicates should be normalized before the bound: %v", err)
	}
	if got := request.GetAudience().GetUserIds(); !slices.Equal(got, []string{"same-user"}) {
		t.Fatalf("deduplicated user_ids = %v", got)
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
		{"empty user", func(o *notificationSendOptions) { o.users = []string{" "} }, "--user"},
		{"invalid team", func(o *notificationSendOptions) { o.teams = []int32{0} }, "--team"},
		{"invalid role", func(o *notificationSendOptions) { o.roles = []string{"operator"} }, "role"},
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

func TestNotificationAudienceFlagsAreRepeatable(t *testing.T) {
	cmd := newNotificationSendCmd()
	if err := cmd.Flags().Parse([]string{
		"--user", "user-1",
		"--user", "user-2",
		"--team", "7",
		"--team", "8",
		"--role", "admin",
		"--role", "viewer",
	}); err != nil {
		t.Fatal(err)
	}
	users, _ := cmd.Flags().GetStringArray("user")
	teams, _ := cmd.Flags().GetInt32Slice("team")
	roles, _ := cmd.Flags().GetStringArray("role")
	if !slices.Equal(users, []string{"user-1", "user-2"}) ||
		!slices.Equal(teams, []int32{7, 8}) ||
		!slices.Equal(roles, []string{"admin", "viewer"}) {
		t.Fatalf("repeatable flags: users=%v teams=%v roles=%v", users, teams, roles)
	}
}

func TestNotificationCreateAlias(t *testing.T) {
	cmd := newNotificationSendCmd()
	if len(cmd.Aliases) != 1 || cmd.Aliases[0] != "create" {
		t.Fatalf("aliases = %v", cmd.Aliases)
	}
}
