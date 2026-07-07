//go:build amd64

package foxglovebridge

import _ "embed"

//go:embed bin/amd64/humble/wendy-ros2-bridge
var binHumble []byte

//go:embed bin/amd64/jazzy/wendy-ros2-bridge
var binJazzy []byte

func init() { binaries = map[string][]byte{"humble": binHumble, "jazzy": binJazzy} }
