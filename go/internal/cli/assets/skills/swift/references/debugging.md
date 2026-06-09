# Debugging

## Terminal UI on Linux

For clean terminal UIs that don't pollute scroll history, use the alternate screen buffer:

```swift
// Enter alternate screen and hide cursor
print("\u{001B}[?1049h", terminator: "")
print("\u{001B}[?25l", terminator: "")
fflush(stdout)

defer {
    // Show cursor and exit alternate screen
    print("\u{001B}[?25h", terminator: "")
    print("\u{001B}[?1049l", terminator: "")
    fflush(stdout)
}

// For redraws, move cursor home and clear lines individually
print("\u{001B}[H", terminator: "")  // Cursor to home position
// Print each line with "\u{001B}[K" at end to clear rest of line
print("\u{001B}[J", terminator: "")  // Clear from cursor to end of screen
```

## Debugging GitHub Actions Failures

Use the GitHub CLI to fetch and search logs:

```bash
# Get job logs via API
gh api repos/OWNER/REPO/actions/jobs/JOB_ID/logs > /tmp/logs.txt

# Search for errors
grep -E "error:|\.swift:[0-9]+:[0-9]+: error" /tmp/logs.txt

# Check which commit CI ran on (may differ from branch HEAD for PR merge commits)
gh run view RUN_ID --json headSha,headBranch
```

Note: PR CI runs may test a merge commit that differs from the branch HEAD.
