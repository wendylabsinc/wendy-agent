# Splice a new agent version into the manifest and update the channel pointer.
# Inputs: $version (string), $entry (version object), $is_release (bool).
.versions = ((.versions // {}) + { ($version): $entry })
| if $is_release then .latest = $version else .latest_nightly = $version end
