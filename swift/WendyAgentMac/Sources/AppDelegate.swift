import AppKit
import OSLog
import SwiftUI
import WendyAgentCore

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate,
    StatusMenuControllerDelegate
{
    private let logger = Logger(
        subsystem: Bundle.main.bundleIdentifier!,
        category: "AppDelegate"
    )
    private let wendyAgent = WendyAgent(configuration: .default)
    private let localRuntime = WendyRuntimeVM()
    private let meshVPN = MeshVPNController()
    private let macDeploymentTargetSettings = MacDeploymentTargetSettings()
    private let welcomeAndPermissions = WelcomeAndPermissions()
    private var statusMenuController: StatusMenuController?
    private var welcomeAndPermissionsWindow: NSWindow?
    // HACK: As an LSUIElement/accessory app, macOS sometimes restores the previously active app
    // after dismissing TCC permission prompts, which leaves this window behind other apps.
    // We paper over that race by retrying activation/fronting a few times.
    // Real fix: make onboarding/permissions run in a regular foreground app instead.
    // See WDY-930: https://linear.app/wendylabsinc/issue/WDY-930/explore-more-packaging-and-process-architecture-options-for-wendy-on
    private var welcomeAndPermissionsPresentationTask: Task<Void, any Error>?
    private var macDeploymentTargetLifecycleTask: Task<Void, Never>?
    private var localRuntimeLifecycleTask: Task<Void, Never>?
    private var isQuitting = false

    func applicationDidFinishLaunching(_ notification: Notification) {
        self.welcomeAndPermissions.configureLaunchAtLoginOnStartup()

        if ProcessInfo.processInfo.environment["XCTestConfigurationFilePath"] == nil {
            self.localRuntimeLifecycleTask = Task {
                await self.localRuntime.start()
                self.localRuntimeLifecycleTask = nil
            }
            Task {
                do {
                    try await MeshSystemExtensionInstaller.shared.installOrUpdate {
                        self.openNetworkExtensionSettings()
                    }
                    await self.meshVPN.connectAutomatically()
                } catch {
                    self.logger.error(
                        "Failed to install WendyNet: \(error.localizedDescription, privacy: .public)"
                    )
                }
            }
        }

        self.macDeploymentTargetLifecycleTask = Task { [self] in
            let macDeploymentTargetEnabled = self.macDeploymentTargetSettings.isEnabled
            self.statusMenuController = await StatusMenuController(
                wendyAgent: self.wendyAgent,
                localRuntime: self.localRuntime,
                meshVPN: self.meshVPN,
                macDeploymentTargetEnabled: macDeploymentTargetEnabled,
                macDeploymentTargetIsUserEditable: self.macDeploymentTargetSettings.isUserEditable,
                delegate: self
            )

            // Registered before start() so the services the agent builds at
            // startup capture it. Invoked from a detached task after the
            // update RPC has returned, so stop()'s drain cannot deadlock on
            // the still-open update stream.
            await self.wendyAgent.setAgentTerminationHandler { [weak self] in
                await self?.performQuit()
            }

            if macDeploymentTargetEnabled {
                self.statusMenuController?.updateMacDeploymentTarget(
                    enabled: true,
                    isTransitioning: true
                )
                await self.startMacDeploymentTarget()
                guard !Task.isCancelled else { return }
                self.statusMenuController?.updateMacDeploymentTarget(
                    enabled: true,
                    isTransitioning: false
                )
            } else {
                self.logger.info(
                    "Mac deployment target is disabled; WendyAgent services will not start"
                )
            }

            self.macDeploymentTargetLifecycleTask = nil
        }

        if self.welcomeAndPermissions.shouldShowWelcomeAndPermissions {
            self.showWelcomeAndPermissionsWindow()
        }
    }

    func statusMenuControllerDidSelectAbout(_ controller: StatusMenuController) {
        NSApplication.shared.activate(ignoringOtherApps: true)
        NSApplication.shared.orderFrontStandardAboutPanel(options: [
            .applicationName: AppDisplayName.current
        ])
    }

    func statusMenuControllerDidSelectWelcomeAndPermissions(_ controller: StatusMenuController) {
        self.showWelcomeAndPermissionsWindow()
    }

    func statusMenuControllerDidSelectQuit(_ controller: StatusMenuController) {
        self.performQuit()
    }

    func statusMenuController(
        _ controller: StatusMenuController,
        didSetMacDeploymentTargetEnabled enabled: Bool
    ) {
        guard self.macDeploymentTargetSettings.isUserEditable else { return }

        self.macDeploymentTargetSettings.setEnabled(enabled)
        self.statusMenuController?.updateMacDeploymentTarget(
            enabled: enabled,
            isTransitioning: true
        )

        self.macDeploymentTargetLifecycleTask = Task {
            if enabled {
                await self.startMacDeploymentTarget()
            } else {
                await self.wendyAgent.stop()
            }

            guard !Task.isCancelled else { return }
            self.statusMenuController?.updateMacDeploymentTarget(
                enabled: enabled,
                isTransitioning: false
            )
            self.macDeploymentTargetLifecycleTask = nil
        }
    }

    func statusMenuController(
        _ controller: StatusMenuController,
        didSetMeshVPNEnabled enabled: Bool
    ) {
        Task {
            if enabled {
                await self.meshVPN.connect()
            } else {
                await self.meshVPN.disable()
            }
        }
    }

    func statusMenuControllerDidSelectNetworkExtensionSettings(
        _ controller: StatusMenuController
    ) {
        self.openNetworkExtensionSettings()
    }

    /// Shuts the agent down and terminates the app. Shared by the Quit menu
    /// item and the agent's post-update termination handler; re-entrant calls
    /// are ignored.
    private func performQuit() {
        guard !self.isQuitting else { return }
        self.isQuitting = true

        Task {
            let lifecycleTask = self.macDeploymentTargetLifecycleTask
            lifecycleTask?.cancel()
            await lifecycleTask?.value
            self.macDeploymentTargetLifecycleTask = nil
            await self.statusMenuController?.invalidate()
            await self.wendyAgent.stop()
            let localRuntimeLifecycleTask = self.localRuntimeLifecycleTask
            localRuntimeLifecycleTask?.cancel()
            await localRuntimeLifecycleTask?.value
            self.localRuntimeLifecycleTask = nil
            self.localRuntime.stop()
            NSApplication.shared.terminate(nil)
        }
    }

    func applicationWillTerminate(_ notification: Notification) {
        self.localRuntime.stop()
    }

    private func startMacDeploymentTarget() async {
        do {
            try await self.wendyAgent.start()
        } catch {
            self.logger.error(
                "Failed to start WendyAgent: \(String(describing: error), privacy: .public)"
            )
        }
    }

    private func openNetworkExtensionSettings() {
        guard let url = URL(
            string: "x-apple.systempreferences:com.apple.LoginItems-Settings.extension"
        ) else {
            return
        }
        NSWorkspace.shared.open(url)
    }

    func windowWillClose(_ notification: Notification) {
        guard let window = notification.object as? NSWindow,
            window === self.welcomeAndPermissionsWindow
        else {
            return
        }

        self.welcomeAndPermissionsPresentationTask?.cancel()
        self.welcomeAndPermissionsPresentationTask = nil
        self.welcomeAndPermissionsWindow = nil
    }

    private func makeWelcomeAndPermissionsWindow() -> NSWindow {
        let rootView = WelcomeAndPermissionsView(
            welcomeAndPermissions: self.welcomeAndPermissions,
            onPermissionRequestCompleted: { [weak self] in
                self?.reassertWelcomeAndPermissionsWindowPresentation()
            }
        )
        let hostingController = NSHostingController(rootView: rootView)

        let welcomeAndPermissionsWindow = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 620, height: 500),
            styleMask: [.titled, .closable],
            backing: .buffered,
            defer: false
        )

        welcomeAndPermissionsWindow.contentViewController = hostingController
        welcomeAndPermissionsWindow.delegate = self
        welcomeAndPermissionsWindow.isReleasedWhenClosed = false

        if let closeButton = welcomeAndPermissionsWindow.standardWindowButton(.closeButton) {
            closeButton.keyEquivalent = "w"
            closeButton.keyEquivalentModifierMask = [.command]
        }

        let contentView = welcomeAndPermissionsWindow.contentView!

        contentView.layoutSubtreeIfNeeded()
        let fittingSize = contentView.fittingSize
        let contentSize = NSSize(
            width: max(620, fittingSize.width),
            height: max(320, fittingSize.height)
        )
        welcomeAndPermissionsWindow.setContentSize(contentSize)

        return welcomeAndPermissionsWindow
    }

    private func showWelcomeAndPermissionsWindow() {
        self.welcomeAndPermissions.prepareForPresentation()

        if let welcomeAndPermissionsWindow = self.welcomeAndPermissionsWindow {
            self.presentWelcomeAndPermissionsWindow(welcomeAndPermissionsWindow)
            return
        }

        let welcomeAndPermissionsWindow = self.makeWelcomeAndPermissionsWindow()
        self.welcomeAndPermissionsWindow = welcomeAndPermissionsWindow
        welcomeAndPermissionsWindow.center()
        welcomeAndPermissionsWindow.setFrameAutosaveName("WelcomeAndPermissionsWindow")
        self.presentWelcomeAndPermissionsWindow(welcomeAndPermissionsWindow)
    }

    private func presentWelcomeAndPermissionsWindow(_ window: NSWindow) {
        NSApplication.shared.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
        window.orderFrontRegardless()
    }

    private func reassertWelcomeAndPermissionsWindowPresentation() {
        self.showWelcomeAndPermissionsWindow()

        self.welcomeAndPermissionsPresentationTask?.cancel()
        self.welcomeAndPermissionsPresentationTask = Task {
            // HACK: A single activate/orderFront call is racy here because the system permission
            // dialog may finish restoring the previously active app after our first attempt.
            // Retry a few times to keep the welcome window visible until WDY-930 is addressed by
            // moving this flow into a regular foreground app.
            let delays: [UInt64] = [150_000_000, 350_000_000, 750_000_000]

            for delay in delays {
                try await Task.sleep(nanoseconds: delay)

                guard
                    !Task.isCancelled,
                    let window = self.welcomeAndPermissionsWindow
                else {
                    return
                }

                self.presentWelcomeAndPermissionsWindow(window)
            }

            self.welcomeAndPermissionsPresentationTask = nil
        }
    }
}
