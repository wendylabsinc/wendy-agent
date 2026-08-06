/// Serializes agent updates across main-server rebuilds. Deliberately NOT
/// released after a successful commit — the process is about to exit, and a
/// retry must see "an update is already in progress".
actor AgentUpdateLock {
    private var isHeld = false

    /// Attempts to acquire the lock. Returns `false` if it is already held.
    func tryAcquire() -> Bool {
        guard !isHeld else { return false }
        isHeld = true
        return true
    }

    /// Releases the lock. A successful update commit must NOT call this —
    /// the process exits shortly afterward, so the lock only needs to be
    /// released on abandoned/failed attempts. See the type-level doc comment.
    func release() {
        isHeld = false
    }
}
