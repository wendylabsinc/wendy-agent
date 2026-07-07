package services

import "testing"

func TestResourceBelongsToApp(t *testing.T) {
	cases := []struct {
		name            string
		resourceService string
		appName         string
		want            bool
	}{
		{
			name:            "exact bare app id",
			resourceService: "myapp",
			appName:         "myapp",
			want:            true,
		},
		{
			name:            "per-service container name of the app",
			resourceService: "myapp_llm",
			appName:         "myapp",
			want:            true,
		},
		{
			name:            "unrelated app sharing the prefix but not the underscore boundary",
			resourceService: "myapp2",
			appName:         "myapp",
			want:            false,
		},
		{
			name:            "suffix is not a valid service name (uppercase)",
			resourceService: "myapp_V2",
			appName:         "myapp",
			want:            false,
		},
		{
			name:            "empty suffix after underscore",
			resourceService: "myapp_",
			appName:         "myapp",
			want:            false,
		},
		{
			name:            "different app",
			resourceService: "otherapp_llm",
			appName:         "myapp",
			want:            false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resourceBelongsToApp(tc.resourceService, tc.appName); got != tc.want {
				t.Fatalf("resourceBelongsToApp(%q, %q) = %v, want %v",
					tc.resourceService, tc.appName, got, tc.want)
			}
		})
	}
}
