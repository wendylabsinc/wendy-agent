//
//  ContentView.swift
//  Companion
//
//  Created by Maximilian Alexander on 7/20/25.
//

import SwiftUI

struct ContentView: View {
    @State private var selectedDevice: Device?

    var body: some View {
        #if os(macOS)
            NavigationSplitView {
                DevicesListPage(selectedDevice: $selectedDevice)
                    .navigationSplitViewColumnWidth(min: 280, ideal: 320)
            } detail: {
                if let device = selectedDevice {
                    DeviceDetailsPage(device: device)
                } else {
                    Text("Select a device")
                        .foregroundColor(.secondary)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            }
        #else
            NavigationStack {
                DevicesListPage()
            }
        #endif
    }
}

#Preview {
    ContentView()
}
