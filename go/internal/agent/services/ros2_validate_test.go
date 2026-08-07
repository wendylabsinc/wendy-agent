package services

import "testing"

// Extends TestValidateROS2GraphName / TestValidateROS2ParamName in
// ros2_parse_test.go, which cover the shell-metacharacter cases. These cover the
// *ROS 2 grammar* cases the old patterns let through: `//foo`, `/foo/` and
// `/1camera` are all illegal ROS 2 names that were forwarded to the CLI and came
// back as an rclpy traceback instead of a clear InvalidArgument.

func TestValidateROS2GraphName_ROSGrammar(t *testing.T) {
	valid := []string{
		"/talker",
		"/camera/driver",
		"/a/b/c/d",
		"talker",              // relative
		"~/local_param_topic", // private namespace
		"/_hidden",            // hidden topics start with an underscore
		"/ns/_hidden",
		"/T",
		"/with_underscores_123",
	}
	for _, name := range valid {
		if err := validateROS2GraphName(name); err != nil {
			t.Errorf("validateROS2GraphName(%q) = %v, want nil", name, err)
		}
	}

	invalid := map[string]string{
		"/":         "bare slash is not a name",
		"//foo":     "double slash is not a legal ROS 2 name",
		"/foo//bar": "double slash mid-name",
		"/foo/":     "trailing slash is not a legal ROS 2 name",
		"/1camera":  "a ROS 2 name segment may not start with a digit",
		"1camera":   "a relative name may not start with a digit",
		"/foo/2bar": "no segment may start with a digit",
		"/foo-bar":  "hyphens are not legal in ROS 2 names",
		"~foo":      "~ is only valid as ~/",
		"/foo\nbar": "newline",
		"/foo|bar":  "pipe",
	}
	for name, why := range invalid {
		if err := validateROS2GraphName(name); err == nil {
			t.Errorf("validateROS2GraphName(%q) = nil, want an error (%s)", name, why)
		}
	}
}

func TestValidateROS2ParamName_ROSGrammar(t *testing.T) {
	for _, ok := range []string{"my_int", "robot.wheel.radius", "a", "A1", "x_1.y_2", "_leading_underscore"} {
		if err := validateROS2ParamName(ok); err != nil {
			t.Errorf("validateROS2ParamName(%q) = %v, want nil", ok, err)
		}
	}
	invalid := map[string]string{
		"1abc":      "leading digit",
		".leading":  "leading dot",
		"trailing.": "trailing dot",
		"a..b":      "empty segment",
		"a/b":       "slashes are not param separators",
	}
	for name, why := range invalid {
		if err := validateROS2ParamName(name); err == nil {
			t.Errorf("validateROS2ParamName(%q) = nil, want an error (%s)", name, why)
		}
	}
}
