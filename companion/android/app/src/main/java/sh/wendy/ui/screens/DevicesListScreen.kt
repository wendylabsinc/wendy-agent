package sh.wendy.ui.screens

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Memory
import androidx.compose.material3.*
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import sh.wendy.model.Device

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun DevicesListScreen(
    onDeviceClick: (Device) -> Unit
) {
    val mockDevices = listOf(
        Device("1", "NVIDIA Jetson Orin Nano Alpha"),
        Device("2", "Raspberry Pi 5"),
        Device("3", "Raspberry Pi Zero 2 W Beta"),
        Device("4", "NVIDIA Jetson Nano Developer Kit"),
        Device("5", "Intel NUC 11 Pro"),
        Device("6", "Raspberry Pi 4 Model B"),
        Device("7", "NVIDIA Jetson Xavier NX"),
        Device("8", "Orange Pi 5 Plus"),
        Device("9", "Rock Pi 4C Plus"),
        Device("10", "BeagleBone AI-64")
    )

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Devices") }
            )
        }
    ) { paddingValues ->
        LazyColumn(
            modifier = Modifier
                .fillMaxSize()
                .padding(paddingValues),
            contentPadding = PaddingValues(vertical = 8.dp)
        ) {
            items(mockDevices) { device ->
                DeviceListItem(
                    device = device,
                    onClick = { onDeviceClick(device) }
                )
            }
        }
    }
}

@Composable
fun DeviceListItem(
    device: Device,
    onClick: () -> Unit
) {
    Card(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 4.dp)
            .clickable { onClick() },
        elevation = CardDefaults.cardElevation(defaultElevation = 2.dp)
    ) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(16.dp),
            verticalAlignment = Alignment.CenterVertically
        ) {
            Icon(
                imageVector = Icons.Default.Memory,
                contentDescription = null,
                tint = MaterialTheme.colorScheme.primary,
                modifier = Modifier.size(24.dp)
            )
            Spacer(modifier = Modifier.width(16.dp))
            Text(
                text = device.name,
                style = MaterialTheme.typography.bodyLarge
            )
        }
    }
}