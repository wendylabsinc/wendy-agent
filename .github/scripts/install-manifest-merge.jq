# Update the channel pointer for the install manifest.
# Inputs: $version (string), $is_release (bool).
if $is_release then .latest = $version else .latest_nightly = $version end
