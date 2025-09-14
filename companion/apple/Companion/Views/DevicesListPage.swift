//
//  DevicesListPage.swift
//  Companion
//
//  Created by Maximilian Alexander on 7/20/25.
//

import SwiftUI

struct DevicesListPage: View {
    #if os(macOS)
        @Binding var selectedDevice: Device?
    #else
        @State private var selectedDevice: Device?
    #endif

    #if os(macOS)
        init(selectedDevice: Binding<Device?>) {
            self._selectedDevice = selectedDevice
        }
    #else
        init() {}
    #endif

    let mockDevices = [
        Device(id: "1", name: "NVIDIA Jetson Orin Nano Alpha"),
        Device(id: "2", name: "Raspberry Pi 5"),
        Device(id: "3", name: "Raspberry Pi Zero 2 W Beta"),
        Device(id: "4", name: "NVIDIA Jetson Nano Developer Kit"),
        Device(id: "5", name: "Intel NUC 11 Pro"),
        Device(id: "6", name: "Raspberry Pi 4 Model B"),
        Device(id: "7", name: "NVIDIA Jetson Xavier NX"),
        Device(id: "8", name: "Orange Pi 5 Plus"),
        Device(id: "9", name: "Rock Pi 4C Plus"),
        Device(id: "10", name: "BeagleBone AI-64"),
    ]

    var body: some View {
        List(mockDevices, selection: $selectedDevice) { device in
            #if os(macOS)
                DeviceListItem(device: device)
                    .tag(device)
                    .contentShape(Rectangle())
                    .onTapGesture {
                        selectedDevice = device
                    }
            #else
                NavigationLink(destination: DeviceDetailsPage(device: device)) {
                    DeviceListItem(device: device)
                }
            #endif
        }
        .navigationTitle("Devices")
    }
}

struct DeviceListItem: View {
    let device: Device

    var body: some View {
        HStack {
            Image(systemName: "cpu")
                .foregroundColor(.accentColor)
            Text(device.name)
                .font(.body)
            Spacer()
        }
        .padding(.vertical, 4)
    }
}

#Preview {
    #if os(macOS)
        NavigationSplitView {
            DevicesListPage(selectedDevice: .constant(nil))
        } detail: {
            Text("Select a device")
        }
    #else
        NavigationStack {
            DevicesListPage()
        }
    #endif
}
